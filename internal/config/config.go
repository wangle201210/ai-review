package config

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LLM    LLMConfig    `yaml:"llm"    json:"llm"`
	VCS    VCSConfig    `yaml:"vcs"    json:"vcs"`
	Agent  AgentConfig  `yaml:"agent"  json:"agent"`
	Review ReviewConfig `yaml:"review" json:"review"`
}

type LLMConfig struct {
	Model       string  `yaml:"model"       json:"model"`
	MaxTokens   int     `yaml:"max_tokens"  json:"max_tokens"`
	Temperature float64 `yaml:"temperature" json:"temperature"`
	APIURL      string  `yaml:"api_url"     json:"api_url"`
	APIToken    string  `yaml:"api_token"   json:"api_token"`
	Timeout     int     `yaml:"timeout"     json:"timeout"`
}

type VCSConfig struct {
	Provider string        `yaml:"provider"    json:"provider"`
	Pipeline VCSPipeline   `yaml:"pipeline"    json:"pipeline"`
	HTTP     VCSHTTPConfig `yaml:"http_client" json:"http_client"`
}

type VCSPipeline struct {
	ProjectID      string `yaml:"project_id"       json:"project_id"`
	MergeRequestID string `yaml:"merge_request_id" json:"merge_request_id"`
	Owner          string `yaml:"owner"            json:"owner"`
	Repo           string `yaml:"repo"             json:"repo"`
	PullNumber     int    `yaml:"pull_number"      json:"pull_number"`
}

type VCSHTTPConfig struct {
	APIURL   string `yaml:"api_url"  json:"api_url"`
	APIToken string `yaml:"api_token" json:"api_token"`
	Timeout  int    `yaml:"timeout"  json:"timeout"`
}

type AgentConfig struct {
	Enabled              bool `yaml:"enabled"                json:"enabled"`
	MaxIterations        int  `yaml:"max_iterations"         json:"max_iterations"`
	MaxTotalContextChars int  `yaml:"max_total_context_chars" json:"max_total_context_chars"`
	CommandTimeout       int  `yaml:"command_timeout"        json:"command_timeout"`
}

type ReviewConfig struct {
	DryRun  bool     `yaml:"dry_run"        json:"dry_run"`
	Ignore  []string `yaml:"ignore_changes" json:"ignore_changes"`
	Allow   []string `yaml:"allow_changes"  json:"allow_changes"`
	Mode    string   `yaml:"mode"           json:"mode"`
}

var varRe = regexp.MustCompile(`\$\{([^}]+)\}`)

func expandEnv(s string) string {
	return varRe.ReplaceAllStringFunc(s, func(match string) string {
		key := match[2 : len(match)-1]
		if v := os.Getenv(key); v != "" {
			return v
		}
		return match
	})
}

func Load() (*Config, error) {
	cfg := &Config{
		LLM: LLMConfig{
			Model:       "claude-sonnet-4-20250514",
			MaxTokens:   4096,
			Temperature: 0.3,
			Timeout:     120,
		},
		Agent: AgentConfig{
			MaxIterations:        25,
			MaxTotalContextChars: 40000,
			CommandTimeout:       10,
		},
		Review: ReviewConfig{
			Mode: "ONLY_ADDED",
		},
	}

	// Try YAML config files
	for _, path := range []string{".ai-review.yaml", ".ai-review.yml", ".ai-review.json"} {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
			break
		}
	}

	// Expand ${VAR} in all string fields
	expandStrings(reflect.ValueOf(cfg).Elem())

	// ENV override with __ delimiter
	applyEnvOverrides(cfg)

	return cfg, nil
}

func expandStrings(v reflect.Value) {
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString(expandEnv(f.String()))
		case reflect.Struct:
			expandStrings(f)
		case reflect.Ptr:
			if !f.IsNil() {
				expandStrings(f)
			}
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	envMap := map[string]string{}
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	setters := map[string]func(string){
		"LLM__MODEL":                func(v string) { cfg.LLM.Model = v },
		"LLM__MAX_TOKENS":           func(v string) { cfg.LLM.MaxTokens, _ = strconv.Atoi(v) },
		"LLM__TEMPERATURE":          func(v string) { cfg.LLM.Temperature, _ = strconv.ParseFloat(v, 64) },
		"LLM__API_URL":              func(v string) { cfg.LLM.APIURL = v },
		"LLM__APITOKEN":             func(v string) { cfg.LLM.APIToken = v },
		"LLM__API_TOKEN":            func(v string) { cfg.LLM.APIToken = v },
		"LLM__HTTP_CLIENT__API_URL": func(v string) { cfg.LLM.APIURL = v },
		"LLM__HTTP_CLIENT__API_TOKEN": func(v string) { cfg.LLM.APIToken = v },
		"ANTHROPIC_API_KEY":         func(v string) { if cfg.LLM.APIToken == "" { cfg.LLM.APIToken = v } },
		"VCS__PROVIDER":             func(v string) { cfg.VCS.Provider = v },
		"VCS__PIPELINE__PROJECT_ID":       func(v string) { cfg.VCS.Pipeline.ProjectID = v },
		"VCS__PIPELINE__MERGE_REQUEST_ID": func(v string) { cfg.VCS.Pipeline.MergeRequestID = v },
		"VCS__PIPELINE__OWNER":            func(v string) { cfg.VCS.Pipeline.Owner = v },
		"VCS__PIPELINE__REPO":             func(v string) { cfg.VCS.Pipeline.Repo = v },
		"VCS__PIPELINE__PULL_NUMBER":      func(v string) { cfg.VCS.Pipeline.PullNumber, _ = strconv.Atoi(v) },
		"VCS__HTTP_CLIENT__API_URL":   func(v string) { cfg.VCS.HTTP.APIURL = v },
		"VCS__HTTP_CLIENT__API_TOKEN": func(v string) { cfg.VCS.HTTP.APIToken = v },
		"AGENT__ENABLED":              func(v string) { cfg.Agent.Enabled = v == "true" || v == "1" },
		"AGENT__MAX_ITERATIONS":       func(v string) { cfg.Agent.MaxIterations, _ = strconv.Atoi(v) },
		"REVIEW__DRY_RUN":            func(v string) { cfg.Review.DryRun = v == "true" || v == "1" },
	}

	for key, val := range envMap {
		if setter, ok := setters[key]; ok && val != "" {
			setter(val)
		}
	}
}

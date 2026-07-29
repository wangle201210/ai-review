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
	Codex  CodexConfig  `yaml:"codex"  json:"codex"`
	HTTP   HTTPConfig   `yaml:"http"   json:"http"`
	Lark   LarkConfig   `yaml:"lark"   json:"lark"`
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
	APIURL   string `yaml:"api_url"   json:"api_url"`
	APIToken string `yaml:"api_token" json:"api_token"`
}

type AgentConfig struct {
	MaxIterations int `yaml:"max_iterations" json:"max_iterations"`
}

type ReviewConfig struct {
	DryRun bool `yaml:"dry_run" json:"dry_run"`
}

type CodexConfig struct {
	Binary           string `yaml:"binary"              json:"binary"`
	WorkDir          string `yaml:"work_dir"            json:"work_dir"`
	Sandbox          string `yaml:"sandbox"             json:"sandbox"`
	TimeoutSeconds   int    `yaml:"timeout_seconds"     json:"timeout_seconds"`
	SkipGitRepoCheck bool   `yaml:"skip_git_repo_check" json:"skip_git_repo_check"`
	NetworkAccess    bool   `yaml:"network_access"      json:"network_access"`
}

type HTTPConfig struct {
	ListenAddr      string `yaml:"listen_addr"       json:"listen_addr"`
	AuthToken       string `yaml:"auth_token"        json:"auth_token"`
	MaxConcurrent   int    `yaml:"max_concurrent"    json:"max_concurrent"`
	MaxRequestBytes int64  `yaml:"max_request_bytes" json:"max_request_bytes"`
}

type LarkConfig struct {
	AppID               string   `yaml:"app_id"                json:"app_id"`
	AppSecret           string   `yaml:"app_secret"            json:"app_secret"`
	BaseURL             string   `yaml:"base_url"              json:"base_url"`
	AllowedChatIDs      []string `yaml:"allowed_chat_ids"      json:"allowed_chat_ids"`
	StatePath           string   `yaml:"state_path"            json:"state_path"`
	QueueSize           int      `yaml:"queue_size"            json:"queue_size"`
	RequireReply        bool     `yaml:"require_reply"         json:"require_reply"`
	CodexURL            string   `yaml:"codex_url"             json:"codex_url"`
	CodexAuthToken      string   `yaml:"codex_auth_token"      json:"codex_auth_token"`
	CodexTimeoutSeconds int      `yaml:"codex_timeout_seconds" json:"codex_timeout_seconds"`
	BusyRetrySeconds    int      `yaml:"busy_retry_seconds"    json:"busy_retry_seconds"`
	MaxPromptBytes      int      `yaml:"max_prompt_bytes"      json:"max_prompt_bytes"`
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
			MaxIterations: 25,
		},
		Codex: CodexConfig{
			Binary:         "codex",
			Sandbox:        "workspace-write",
			TimeoutSeconds: 1800,
		},
		HTTP: HTTPConfig{
			ListenAddr:      "127.0.0.1:8787",
			MaxConcurrent:   1,
			MaxRequestBytes: 64 * 1024,
		},
		Lark: LarkConfig{
			BaseURL:             "https://open.larksuite.com",
			StatePath:           ".ai-review-lark-state.json",
			QueueSize:           32,
			RequireReply:        true,
			CodexURL:            "http://127.0.0.1:8787/v1/codex",
			CodexTimeoutSeconds: 1860,
			BusyRetrySeconds:    5,
			MaxPromptBytes:      48 * 1024,
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
		"LLM__MODEL":       func(v string) { cfg.LLM.Model = v },
		"LLM__MAX_TOKENS":  func(v string) { cfg.LLM.MaxTokens, _ = strconv.Atoi(v) },
		"LLM__TEMPERATURE": func(v string) { cfg.LLM.Temperature, _ = strconv.ParseFloat(v, 64) },
		"LLM__API_URL":     func(v string) { cfg.LLM.APIURL = v },
		"LLM__API_TOKEN":   func(v string) { cfg.LLM.APIToken = v },
		"ANTHROPIC_API_KEY": func(v string) {
			if cfg.LLM.APIToken == "" {
				cfg.LLM.APIToken = v
			}
		},
		"VCS__PROVIDER":                   func(v string) { cfg.VCS.Provider = v },
		"VCS__PIPELINE__PROJECT_ID":       func(v string) { cfg.VCS.Pipeline.ProjectID = v },
		"VCS__PIPELINE__MERGE_REQUEST_ID": func(v string) { cfg.VCS.Pipeline.MergeRequestID = v },
		"VCS__PIPELINE__OWNER":            func(v string) { cfg.VCS.Pipeline.Owner = v },
		"VCS__PIPELINE__REPO":             func(v string) { cfg.VCS.Pipeline.Repo = v },
		"VCS__PIPELINE__PULL_NUMBER":      func(v string) { cfg.VCS.Pipeline.PullNumber, _ = strconv.Atoi(v) },
		"VCS__HTTP_CLIENT__API_URL":       func(v string) { cfg.VCS.HTTP.APIURL = v },
		"VCS__HTTP_CLIENT__API_TOKEN":     func(v string) { cfg.VCS.HTTP.APIToken = v },
		"AGENT__MAX_ITERATIONS":           func(v string) { cfg.Agent.MaxIterations, _ = strconv.Atoi(v) },
		"REVIEW__DRY_RUN":                 func(v string) { cfg.Review.DryRun = v == "true" || v == "1" },
		"CODEX__BINARY":                   func(v string) { cfg.Codex.Binary = v },
		"CODEX__WORK_DIR":                 func(v string) { cfg.Codex.WorkDir = v },
		"CODEX__SANDBOX":                  func(v string) { cfg.Codex.Sandbox = v },
		"CODEX__TIMEOUT_SECONDS":          func(v string) { cfg.Codex.TimeoutSeconds, _ = strconv.Atoi(v) },
		"CODEX__SKIP_GIT_REPO_CHECK": func(v string) {
			cfg.Codex.SkipGitRepoCheck = v == "true" || v == "1"
		},
		"CODEX__NETWORK_ACCESS": func(v string) {
			cfg.Codex.NetworkAccess = v == "true" || v == "1"
		},
		"HTTP__LISTEN_ADDR":    func(v string) { cfg.HTTP.ListenAddr = v },
		"HTTP__MAX_CONCURRENT": func(v string) { cfg.HTTP.MaxConcurrent, _ = strconv.Atoi(v) },
		"HTTP__MAX_REQUEST_BYTES": func(v string) {
			cfg.HTTP.MaxRequestBytes, _ = strconv.ParseInt(v, 10, 64)
		},
		"LARK__APP_ID":        func(v string) { cfg.Lark.AppID = v },
		"LARK__APP_SECRET":    func(v string) { cfg.Lark.AppSecret = v },
		"LARK__BASE_URL":      func(v string) { cfg.Lark.BaseURL = v },
		"LARK__STATE_PATH":    func(v string) { cfg.Lark.StatePath = v },
		"LARK__QUEUE_SIZE":    func(v string) { cfg.Lark.QueueSize, _ = strconv.Atoi(v) },
		"LARK__REQUIRE_REPLY": func(v string) { cfg.Lark.RequireReply = parseBool(v) },
		"LARK__CODEX_URL":     func(v string) { cfg.Lark.CodexURL = v },
		"LARK__CODEX_TIMEOUT_SECONDS": func(v string) {
			cfg.Lark.CodexTimeoutSeconds, _ = strconv.Atoi(v)
		},
		"LARK__BUSY_RETRY_SECONDS": func(v string) {
			cfg.Lark.BusyRetrySeconds, _ = strconv.Atoi(v)
		},
		"LARK__MAX_PROMPT_BYTES": func(v string) {
			cfg.Lark.MaxPromptBytes, _ = strconv.Atoi(v)
		},
	}

	for key, val := range envMap {
		if setter, ok := setters[key]; ok && val != "" {
			setter(val)
		}
	}

	// Auth tokens intentionally support an explicitly empty environment value.
	if value, exists := envMap["HTTP__AUTH_TOKEN"]; exists {
		cfg.HTTP.AuthToken = value
	} else if value := envMap["AI_REVIEW_HTTP_TOKEN"]; value != "" && cfg.HTTP.AuthToken == "" {
		cfg.HTTP.AuthToken = value
	}
	if value, exists := envMap["LARK__CODEX_AUTH_TOKEN"]; exists {
		cfg.Lark.CodexAuthToken = value
	}
	if value, exists := envMap["LARK__ALLOWED_CHAT_IDS"]; exists {
		cfg.Lark.AllowedChatIDs = splitCommaSeparated(value)
	}
}

func parseBool(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func splitCommaSeparated(value string) []string {
	var result []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			result = append(result, entry)
		}
	}
	return result
}

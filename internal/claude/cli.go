package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Env struct {
	GitLabURL       string
	GitLabToken     string
	GitLabProjectID string
	GitLabMRIID     string
	GitLabBaseSHA   string
	GitLabHeadSHA   string
	GitLabStartSHA  string
	// GitHub
	GitHubOwner      string
	GitHubRepo       string
	GitHubPullNumber int
	GitHubHeadSHA    string
	GitHubAPIURL     string
	// LLM
	AnthropicAPIKey string
	AnthropicURL    string
	ClaudeModel     string
	MaxTurns        int
	// VCS provider
	VCSProvider string
}

type ReviewResult struct {
	ExitCode int
	CostUSD  float64
	Duration int64
}

func RunReview(ctx context.Context, workDir, prompt string, skillDir string, cleanupDir string, env Env) (*ReviewResult, error) {
	cmd := BuildCmd(ctx, prompt, skillDir, env.ClaudeModel, env.MaxTurns)
	cmd.Dir = workDir
	cmd.Stdin = nil
	cmd.Env = buildEnviron(env)

	// Ensure cleanup of temp skill dir
	if cleanupDir != "" {
		defer os.RemoveAll(cleanupDir)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	log.Printf("[claude] Starting review in %s (prompt: %d chars)\n", workDir, len(prompt))

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Stream stderr
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[claude stderr] %s", scanner.Text())
		}
	}()

	// Stream stdout (JSON lines)
	result := &ReviewResult{}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("[claude stdout] %s", line)
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		case "assistant":
			msg, _ := event["message"].(map[string]interface{})
			content, _ := msg["content"].([]interface{})
			for _, block := range content {
				b, ok := block.(map[string]interface{})
				if !ok {
					continue
				}
				blockType, _ := b["type"].(string)
				switch blockType {
				case "text":
					text, _ := b["text"].(string)
					log.Printf("[claude] %s", text)
				case "tool_use":
					name, _ := b["name"].(string)
					input, _ := json.Marshal(b["input"])
					log.Printf("[claude tool] %s: %s", name, string(input))
				case "tool_result":
					content, _ := json.Marshal(b["content"])
					log.Printf("[claude tool_result] %s", string(content))
				default:
					data, _ := json.Marshal(b)
					log.Printf("[claude %s] %s", blockType, string(data))
				}
			}
		case "result":
			if duration, ok := event["duration_ms"].(float64); ok {
				result.Duration = int64(duration)
			}
			if cost, ok := event["total_cost_usd"].(float64); ok {
				result.CostUSD = cost
			}
			log.Printf("[claude] Completed in %dms, cost $%.4f", result.Duration, result.CostUSD)
		}
	}

	if err := cmd.Wait(); err != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
		<-stderrDone
		log.Printf("[claude] Exited with code %d", result.ExitCode)
		return result, fmt.Errorf("claude exit code %d", result.ExitCode)
	}

	<-stderrDone
	result.ExitCode = 0
	log.Printf("[claude] Review completed successfully")
	return result, nil
}

func BuildCmd(ctx context.Context, prompt, skillDir, model string, maxTurns int) *exec.Cmd {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--allowedTools", "Bash",
		"--allowedTools", "Bash(curl *)",
		"--allowedTools", "Bash(bash *)",
	}
	if skillDir != "" {
		skillFile := filepath.Join(skillDir, "SKILL.md")
		args = append(args, "--append-system-prompt-file", skillFile)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	return exec.CommandContext(ctx, "claude", args...)
}

func buildEnviron(env Env) []string {
	e := os.Environ()

	cleaned := make([]string, 0, len(e))
	for _, v := range e {
		if strings.HasPrefix(v, "GIT_DIR=") || strings.HasPrefix(v, "GIT_WORK_TREE=") {
			continue
		}
		cleaned = append(cleaned, v)
	}

	if env.AnthropicAPIKey != "" {
		cleaned = append(cleaned, "ANTHROPIC_API_KEY="+env.AnthropicAPIKey)
	}
	if env.AnthropicURL != "" {
		cleaned = append(cleaned, "ANTHROPIC_BASE_URL="+env.AnthropicURL)
	}

	if env.VCSProvider == "GITHUB" {
		apiURL := env.GitHubAPIURL
		if apiURL == "" {
			apiURL = "https://api.github.com"
		}
		cleaned = append(cleaned,
			"GITHUB_OWNER="+env.GitHubOwner,
			"GITHUB_REPO="+env.GitHubRepo,
			fmt.Sprintf("GITHUB_PULL_NUMBER=%d", env.GitHubPullNumber),
			"GITHUB_SHA="+env.GitHubHeadSHA,
			"GITHUB_TOKEN="+env.GitLabToken,
			"GITHUB_API_URL="+apiURL,
		)
	} else {
		cleaned = append(cleaned,
			"GITLAB_URL="+env.GitLabURL,
			"GITLAB_TOKEN="+env.GitLabToken,
			"GITLAB_PROJECT_ID="+env.GitLabProjectID,
			"GITLAB_MR_IID="+env.GitLabMRIID,
			"GITLAB_BASE_SHA="+env.GitLabBaseSHA,
			"GITLAB_HEAD_SHA="+env.GitLabHeadSHA,
			"GITLAB_START_SHA="+env.GitLabStartSHA,
		)
	}

	return cleaned
}

func WriteSkillFiles(skillFS fs.FS, destDir string) error {
	return fs.WalkDir(skillFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		targetPath := filepath.Join(destDir, path)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}
		data, err := fs.ReadFile(skillFS, path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(targetPath, data, 0644); err != nil {
			return err
		}
		if strings.HasSuffix(path, ".sh") {
			os.Chmod(targetPath, 0755)
		}
		return nil
	})
}

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wangle201210/ai-review/internal/claude"
	"github.com/wangle201210/ai-review/internal/config"
	llmPkg "github.com/wangle201210/ai-review/internal/llm"
	"github.com/wangle201210/ai-review/internal/prompt"
	"github.com/wangle201210/ai-review/internal/review"
	"github.com/wangle201210/ai-review/internal/vcs"
	gitlabVCS "github.com/wangle201210/ai-review/internal/vcs/gitlab"
	githubVCS "github.com/wangle201210/ai-review/internal/vcs/github"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "ai-review",
		Short: "AI-powered code review tool using Claude Code CLI",
	}

	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(runInlineCmd())
	rootCmd.AddCommand(runSummaryCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run full review (Claude CLI deep review + summary)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(true, true)
		},
	}
}

func runInlineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-inline",
		Short: "Run Claude CLI deep review only",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(true, false)
		},
	}
}

func runSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-summary",
		Short: "Run summary review only (via Anthropic API)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(false, true)
		},
	}
}

func runReview(doInline, doSummary bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()
	vcsClient, err := createVCSClient(cfg)
	if err != nil {
		return err
	}

	info, err := vcsClient.GetReviewInfo(ctx)
	if err != nil {
		return fmt.Errorf("get review info: %w", err)
	}
	log.Printf("[main] Review: %s (%s → %s)\n", info.Title, info.SourceBranch, info.TargetBranch)

	// Inline review via Claude CLI
	if doInline {
		if err := runClaudeCLIReview(ctx, cfg, vcsClient, info); err != nil {
			log.Printf("[main] Claude CLI review error: %v\n", err)
			if !doSummary {
				return err
			}
		}
	}

	// Summary review via Anthropic API
	if doSummary {
		if err := runSummaryReview(ctx, cfg, vcsClient); err != nil {
			return fmt.Errorf("summary review: %w", err)
		}
	}

	return nil
}

func runClaudeCLIReview(ctx context.Context, cfg *config.Config, vcsClient vcs.Client, info *vcs.ReviewInfo) error {
	log.Println("[main] Starting Claude CLI deep review...")

	// Get diff text
	diffText, err := getDiff(ctx, info, vcsClient)
	if err != nil {
		return fmt.Errorf("get diff: %w", err)
	}
	if diffText == "" {
		log.Println("[main] No diff available, skipping review")
		return nil
	}
	log.Printf("[main] Diff: %d chars\n", len(diffText))

	provider := strings.ToUpper(cfg.VCS.Provider)
	skillDir := claude.GetSkillDir(provider)
	log.Printf("[main] Skill dir: %s\n", skillDir)

	// Build prompt with skill instructions embedded
	reviewPrompt := claude.BuildPromptWithSkills(diffText, provider)

	// Determine work directory (current directory, should be repo root in CI)
	workDir, _ := os.Getwd()

	// Build env for Claude CLI
	claudeEnv := claude.Env{
		VCSProvider:      provider,
		AnthropicAPIKey:  cfg.LLM.APIToken,
		AnthropicURL:     cfg.LLM.APIURL,
		ClaudeModel:      cfg.LLM.Model,
		MaxTurns:         cfg.Agent.MaxIterations,
		GitLabURL:        cfg.VCS.HTTP.APIURL,
		GitLabToken:      cfg.VCS.HTTP.APIToken,
		GitLabProjectID:  cfg.VCS.Pipeline.ProjectID,
		GitLabMRIID:      cfg.VCS.Pipeline.MergeRequestID,
		GitLabBaseSHA:    info.BaseSHA,
		GitLabHeadSHA:    info.HeadSHA,
		GitLabStartSHA:   info.StartSHA,
		GitHubOwner:      cfg.VCS.Pipeline.Owner,
		GitHubRepo:       cfg.VCS.Pipeline.Repo,
		GitHubPullNumber: cfg.VCS.Pipeline.PullNumber,
		GitHubHeadSHA:    info.HeadSHA,
		GitHubAPIURL:     cfg.VCS.HTTP.APIURL,
	}
	// For GitHub, reuse the VCS HTTP token
	if provider == "GITHUB" {
		claudeEnv.GitLabToken = cfg.VCS.HTTP.APIToken
	}

	result, err := claude.RunReview(ctx, workDir, reviewPrompt, skillDir, claudeEnv)
	if err != nil {
		return err
	}

	log.Printf("[main] Claude CLI review completed: exit=%d, cost=$%.4f, duration=%dms\n",
		result.ExitCode, result.CostUSD, result.Duration)
	return nil
}

func runSummaryReview(ctx context.Context, cfg *config.Config, vcsClient vcs.Client) error {
	log.Println("[main] Starting summary review (via API)...")

	diffText, err := getDiff(ctx, nil, vcsClient)
	if err != nil {
		return fmt.Errorf("get diff: %w", err)
	}
	if diffText == "" {
		log.Println("[main] No diff available, skipping summary review")
		return nil
	}

	llmClient := llmPkg.NewClaudeClient(
		cfg.LLM.APIToken, cfg.LLM.APIURL, cfg.LLM.Model,
		cfg.LLM.MaxTokens, cfg.LLM.Temperature, cfg.LLM.Timeout,
	)
	promptSvc, err := prompt.NewService()
	if err != nil {
		return fmt.Errorf("load prompts: %w", err)
	}

	return review.RunSummary(ctx, llmClient, vcsClient, diffText, cfg, promptSvc)
}

func getDiff(ctx context.Context, info *vcs.ReviewInfo, vcsClient vcs.Client) (string, error) {
	// Try VCS API (always works, even outside git repo)
	diffText, err := vcsClient.GetDiff(ctx)
	if err == nil && diffText != "" {
		return diffText, nil
	}
	if err != nil {
		log.Printf("[main] VCS API diff failed: %v\n", err)
	}
	return "", fmt.Errorf("no diff available")
}

func createVCSClient(cfg *config.Config) (vcs.Client, error) {
	provider := strings.ToUpper(cfg.VCS.Provider)
	switch provider {
	case "GITLAB":
		projectID := cfg.VCS.Pipeline.ProjectID
		mrIID := cfg.VCS.Pipeline.MergeRequestID
		if projectID == "" || mrIID == "" {
			return nil, fmt.Errorf("GitLab requires VCS__PIPELINE__PROJECT_ID and VCS__PIPELINE__MERGE_REQUEST_ID")
		}
		apiURL := cfg.VCS.HTTP.APIURL
		if apiURL == "" {
			apiURL = "https://gitlab.com"
		}
		return gitlabVCS.New(apiURL, cfg.VCS.HTTP.APIToken, projectID, mrIID), nil
	case "GITHUB":
		owner := cfg.VCS.Pipeline.Owner
		repo := cfg.VCS.Pipeline.Repo
		pullNumber := cfg.VCS.Pipeline.PullNumber
		if owner == "" || repo == "" || pullNumber == 0 {
			return nil, fmt.Errorf("GitHub requires VCS__PIPELINE__OWNER, VCS__PIPELINE__REPO, and VCS__PIPELINE__PULL_NUMBER")
		}
		apiURL := cfg.VCS.HTTP.APIURL
		if apiURL == "" {
			apiURL = "https://api.github.com"
		}
		return githubVCS.New(apiURL, cfg.VCS.HTTP.APIToken, owner, repo, pullNumber), nil
	default:
		return nil, fmt.Errorf("unsupported VCS provider: %s (use GITLAB or GITHUB)", cfg.VCS.Provider)
	}
}

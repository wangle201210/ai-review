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
	githubVCS "github.com/wangle201210/ai-review/internal/vcs/github"
	gitlabVCS "github.com/wangle201210/ai-review/internal/vcs/gitlab"
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

	diffText, err := vcsClient.GetDiff(ctx)
	if err != nil {
		return fmt.Errorf("get diff: %w", err)
	}
	if diffText == "" {
		log.Println("[main] No diff available, skipping review")
		return nil
	}
	log.Printf("[main] Diff: %d chars\n", len(diffText))

	if doInline {
		info, err := vcsClient.GetReviewInfo(ctx)
		if err != nil {
			return fmt.Errorf("get review info: %w", err)
		}
		log.Printf("[main] Review: %s (%s → %s)\n", info.Title, info.SourceBranch, info.TargetBranch)

		if err := runClaudeCLIReview(ctx, cfg, diffText, info); err != nil {
			log.Printf("[main] Claude CLI review error: %v\n", err)
			if !doSummary {
				return err
			}
		}
	}

	if doSummary {
		if err := runSummaryReview(ctx, cfg, vcsClient, diffText); err != nil {
			return fmt.Errorf("summary review: %w", err)
		}
	}

	return nil
}

func runClaudeCLIReview(ctx context.Context, cfg *config.Config, diffText string, info *vcs.ReviewInfo) error {
	log.Println("[main] Starting Claude CLI deep review...")

	provider := strings.ToUpper(cfg.VCS.Provider)
	skillDir := claude.GetSkillDir(provider)
	log.Printf("[main] Skill dir: %s\n", skillDir)

	reviewPrompt := claude.BuildPromptWithSkills(diffText, provider)

	workDir, _ := os.Getwd()

	claudeEnv := claude.Env{
		VCSProvider:     provider,
		AnthropicAPIKey: cfg.LLM.APIToken,
		AnthropicURL:    cfg.LLM.APIURL,
		ClaudeModel:     cfg.LLM.Model,
		MaxTurns:        cfg.Agent.MaxIterations,
	}
	switch provider {
	case "GITHUB":
		claudeEnv.GitHubOwner = cfg.VCS.Pipeline.Owner
		claudeEnv.GitHubRepo = cfg.VCS.Pipeline.Repo
		claudeEnv.GitHubPullNumber = cfg.VCS.Pipeline.PullNumber
		claudeEnv.GitHubHeadSHA = info.HeadSHA
		claudeEnv.GitHubAPIURL = cfg.VCS.HTTP.APIURL
		claudeEnv.GitHubToken = cfg.VCS.HTTP.APIToken
	default:
		claudeEnv.GitLabURL = cfg.VCS.HTTP.APIURL
		claudeEnv.GitLabToken = cfg.VCS.HTTP.APIToken
		claudeEnv.GitLabProjectID = cfg.VCS.Pipeline.ProjectID
		claudeEnv.GitLabMRIID = cfg.VCS.Pipeline.MergeRequestID
		claudeEnv.GitLabBaseSHA = info.BaseSHA
		claudeEnv.GitLabHeadSHA = info.HeadSHA
		claudeEnv.GitLabStartSHA = info.StartSHA
	}

	result, err := claude.RunReview(ctx, workDir, reviewPrompt, skillDir, claudeEnv)
	if err != nil {
		return err
	}

	log.Printf("[main] Claude CLI review completed: exit=%d, cost=$%.4f, duration=%dms\n",
		result.ExitCode, result.CostUSD, result.Duration)
	return nil
}

func runSummaryReview(ctx context.Context, cfg *config.Config, vcsClient vcs.Client, diffText string) error {
	log.Println("[main] Starting summary review (via API)...")

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

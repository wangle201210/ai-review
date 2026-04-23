package review

import (
	"context"
	"fmt"
	"log"

	"github.com/wangle201210/ai-review/internal/config"
	"github.com/wangle201210/ai-review/internal/llm"
	"github.com/wangle201210/ai-review/internal/prompt"
	"github.com/wangle201210/ai-review/internal/vcs"
)

func RunSummary(ctx context.Context, llmClient *llm.ClaudeClient, vcsClient vcs.Client, diffText string, cfg *config.Config, promptSvc *prompt.Service) error {
	log.Println("[review] Starting summary review...")

	systemPrompt, userPrompt := promptSvc.BuildSummaryPrompt(diffText)

	result, err := llmClient.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("LLM call: %w", err)
	}

	log.Printf("[review] Summary received (%d tokens)\n", result.TotalTokens)

	if cfg.Review.DryRun {
		log.Printf("[review] [DRY RUN] Summary:\n%s\n", result.Text)
		return nil
	}

	if err := vcsClient.PostGeneralComment(ctx, result.Text); err != nil {
		return fmt.Errorf("post summary: %w", err)
	}

	log.Println("[review] Summary posted successfully.")
	return nil
}

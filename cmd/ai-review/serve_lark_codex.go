package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangle201210/ai-review/internal/config"
	"github.com/wangle201210/ai-review/internal/larkbot"
)

func serveLarkCodexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve-lark-codex",
		Short: "Receive Lark messages and forward incident tasks to the Codex HTTP service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runLarkCodexService(cmd.Context(), cfg)
		},
	}
}

func runLarkCodexService(parent context.Context, cfg *config.Config) error {
	service, err := larkbot.NewService(larkbot.ServiceConfig{
		AppID:          cfg.Lark.AppID,
		AppSecret:      cfg.Lark.AppSecret,
		BaseURL:        cfg.Lark.BaseURL,
		AllowedChatIDs: cfg.Lark.AllowedChatIDs,
		StatePath:      cfg.Lark.StatePath,
		QueueSize:      cfg.Lark.QueueSize,
		RequireReply:   cfg.Lark.RequireReply,
		CodexURL:       cfg.Lark.CodexURL,
		CodexAuthToken: cfg.Lark.CodexAuthToken,
		CodexTimeout:   time.Duration(cfg.Lark.CodexTimeoutSeconds) * time.Second,
		BusyRetry:      time.Duration(cfg.Lark.BusyRetrySeconds) * time.Second,
		MaxPromptBytes: cfg.Lark.MaxPromptBytes,
		Logger:         log.Default(),
	})
	if err != nil {
		return fmt.Errorf("configure Lark Codex service: %w", err)
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf(
		"[lark-codex] starting base_url=%s codex_url=%s queue_size=%d require_reply=%t allowed_chats=%d",
		cfg.Lark.BaseURL,
		cfg.Lark.CodexURL,
		cfg.Lark.QueueSize,
		cfg.Lark.RequireReply,
		len(cfg.Lark.AllowedChatIDs),
	)
	return service.Run(ctx)
}

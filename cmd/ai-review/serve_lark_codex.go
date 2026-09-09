package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangle201210/ai-review/internal/config"
	"github.com/wangle201210/ai-review/internal/larkbot"
	"github.com/wangle201210/ai-review/internal/mrreview"
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
		WorkerCount:    cfg.Lark.WorkerCount,
		RequireReply:   cfg.Lark.RequireReply,
		CodexURL:       cfg.Lark.CodexURL,
		CodexAuthToken: cfg.Lark.CodexAuthToken,
		CodexTimeout:   time.Duration(cfg.Lark.CodexTimeoutSeconds) * time.Second,
		BusyRetry:      time.Duration(cfg.Lark.BusyRetrySeconds) * time.Second,
		MaxPromptBytes: cfg.Lark.MaxPromptBytes,
		ReviewChatID:   cfg.GitLabMRReview.LarkChatID,
		Logger:         log.Default(),
	})
	if err != nil {
		return fmt.Errorf("configure Lark Codex service: %w", err)
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf(
		"[lark-codex] starting base_url=%s codex_url=%s queue_size=%d workers=%d require_reply=%t allowed_chats=%d",
		cfg.Lark.BaseURL,
		cfg.Lark.CodexURL,
		cfg.Lark.QueueSize,
		cfg.Lark.WorkerCount,
		cfg.Lark.RequireReply,
		len(cfg.Lark.AllowedChatIDs),
	)
	if !cfg.GitLabMRReview.Enabled {
		return service.Run(ctx)
	}
	if strings.TrimSpace(cfg.GitLabMRReview.ListenAddr) == "" {
		return errors.New("GITLAB_MR_REVIEW__LISTEN_ADDR is required when MR review is enabled")
	}
	if strings.TrimSpace(cfg.GitLabMRReview.LarkChatID) == "" {
		return errors.New("GITLAB_MR_REVIEW__LARK_CHAT_ID is required when MR review is enabled")
	}

	webhookHandler, err := mrreview.NewHandler(service.EnqueueMRReview, mrreview.Config{
		Secret:           cfg.GitLabMRReview.Secret,
		AllowedHost:      cfg.GitLabMRReview.AllowedHost,
		AllowedNamespace: cfg.GitLabMRReview.AllowedNamespace,
		MaxRequestBytes:  cfg.GitLabMRReview.MaxRequestBytes,
		Logger:           log.Default(),
	})
	if err != nil {
		return fmt.Errorf("configure GitLab MR review webhook: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle(mrreview.WebhookPath, webhookHandler)
	mux.Handle(mrreview.LegacyWebhookPath, webhookHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	webhookServer := &http.Server{
		Addr:              cfg.GitLabMRReview.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serviceErr := make(chan error, 1)
	go func() {
		serviceErr <- service.Run(ctx)
	}()
	webhookErr := make(chan error, 1)
	go func() {
		log.Printf(
			"[gitlab-mr-review] listening on %s path=%s allowed_namespace=%s lark_chat_id=%s",
			cfg.GitLabMRReview.ListenAddr,
			mrreview.WebhookPath,
			cfg.GitLabMRReview.AllowedNamespace,
			cfg.GitLabMRReview.LarkChatID,
		)
		webhookErr <- webhookServer.ListenAndServe()
	}()

	shutdownWebhook := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := webhookServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down GitLab MR review webhook: %w", err)
		}
		return nil
	}

	select {
	case err := <-serviceErr:
		stop()
		_ = shutdownWebhook()
		return err
	case err := <-webhookErr:
		stop()
		<-serviceErr
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve GitLab MR review webhook: %w", err)
	case <-ctx.Done():
		if err := shutdownWebhook(); err != nil {
			return err
		}
		if err := <-serviceErr; err != nil {
			return err
		}
		return nil
	}
}

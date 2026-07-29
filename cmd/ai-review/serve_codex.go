package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangle201210/ai-review/internal/codex"
	"github.com/wangle201210/ai-review/internal/codexhttp"
	"github.com/wangle201210/ai-review/internal/config"
)

func serveCodexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve-codex",
		Short: "Serve a token-protected HTTP API backed by Codex CLI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runCodexServer(cmd.Context(), cfg)
		},
	}
}

func runCodexServer(parent context.Context, cfg *config.Config) error {
	if cfg.HTTP.ListenAddr == "" {
		return errors.New("HTTP listen address is required")
	}

	runner, err := codex.NewRunner(codex.RunnerConfig{
		Binary:           cfg.Codex.Binary,
		WorkDir:          cfg.Codex.WorkDir,
		Sandbox:          cfg.Codex.Sandbox,
		Timeout:          time.Duration(cfg.Codex.TimeoutSeconds) * time.Second,
		SkipGitRepoCheck: cfg.Codex.SkipGitRepoCheck,
		NetworkAccess:    cfg.Codex.NetworkAccess,
		Logger:           log.Default(),
	})
	if err != nil {
		return fmt.Errorf("configure Codex runner: %w", err)
	}

	handler, err := codexhttp.NewHandler(runner, codexhttp.Config{
		AuthToken:       cfg.HTTP.AuthToken,
		MaxConcurrent:   cfg.HTTP.MaxConcurrent,
		MaxRequestBytes: cfg.HTTP.MaxRequestBytes,
		Logger:          log.Default(),
	})
	if err != nil {
		return fmt.Errorf("configure HTTP handler: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.HTTP.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      time.Duration(cfg.Codex.TimeoutSeconds)*time.Second + 30*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf(
			"[codex-http] listening on %s work_dir=%s sandbox=%s network_access=%t max_concurrent=%d",
			cfg.HTTP.ListenAddr,
			cfg.Codex.WorkDir,
			cfg.Codex.Sandbox,
			cfg.Codex.NetworkAccess,
			cfg.HTTP.MaxConcurrent,
		)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Codex HTTP API: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down Codex HTTP API: %w", err)
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Codex HTTP API: %w", err)
		}
		log.Println("[codex-http] stopped")
		return nil
	}
}

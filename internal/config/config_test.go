package config

import (
	"os"
	"reflect"
	"testing"
)

func TestLoadCodexHTTPEnvironment(t *testing.T) {
	t.Setenv("CODEX__BINARY", "/usr/local/bin/codex")
	t.Setenv("CODEX__WORK_DIR", "/srv/project")
	t.Setenv("CODEX__SANDBOX", "read-only")
	t.Setenv("CODEX__TIMEOUT_SECONDS", "60")
	t.Setenv("CODEX__SKIP_GIT_REPO_CHECK", "true")
	t.Setenv("CODEX__NETWORK_ACCESS", "true")
	t.Setenv("HTTP__LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("HTTP__AUTH_TOKEN", "0123456789abcdef0123456789abcdef")
	t.Setenv("HTTP__MAX_CONCURRENT", "2")
	t.Setenv("HTTP__MAX_REQUEST_BYTES", "4096")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Codex.Binary != "/usr/local/bin/codex" ||
		cfg.Codex.WorkDir != "/srv/project" ||
		cfg.Codex.Sandbox != "read-only" ||
		cfg.Codex.TimeoutSeconds != 60 ||
		!cfg.Codex.SkipGitRepoCheck ||
		!cfg.Codex.NetworkAccess {
		t.Fatalf("Codex config = %#v", cfg.Codex)
	}
	if cfg.HTTP.ListenAddr != "127.0.0.1:9000" ||
		cfg.HTTP.AuthToken != "0123456789abcdef0123456789abcdef" ||
		cfg.HTTP.MaxConcurrent != 2 ||
		cfg.HTTP.MaxRequestBytes != 4096 {
		t.Fatalf("HTTP config = %#v", cfg.HTTP)
	}
}

func TestLoadLarkEnvironment(t *testing.T) {
	t.Setenv("LARK__APP_ID", "cli_test")
	t.Setenv("LARK__APP_SECRET", "secret_test")
	t.Setenv("LARK__BASE_URL", "https://open.example.test")
	t.Setenv("LARK__ALLOWED_CHAT_IDS", "oc_one, oc_two")
	t.Setenv("LARK__STATE_PATH", "/tmp/lark-state.json")
	t.Setenv("LARK__QUEUE_SIZE", "8")
	t.Setenv("LARK__REQUIRE_REPLY", "false")
	t.Setenv("LARK__CODEX_URL", "http://127.0.0.1:9999/v1/codex")
	t.Setenv("LARK__CODEX_AUTH_TOKEN", "codex-token")
	t.Setenv("LARK__CODEX_TIMEOUT_SECONDS", "90")
	t.Setenv("LARK__BUSY_RETRY_SECONDS", "2")
	t.Setenv("LARK__MAX_PROMPT_BYTES", "8192")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Lark.AppID != "cli_test" ||
		cfg.Lark.AppSecret != "secret_test" ||
		cfg.Lark.BaseURL != "https://open.example.test" ||
		cfg.Lark.StatePath != "/tmp/lark-state.json" ||
		cfg.Lark.QueueSize != 8 ||
		cfg.Lark.RequireReply ||
		cfg.Lark.CodexURL != "http://127.0.0.1:9999/v1/codex" ||
		cfg.Lark.CodexAuthToken != "codex-token" ||
		cfg.Lark.CodexTimeoutSeconds != 90 ||
		cfg.Lark.BusyRetrySeconds != 2 ||
		cfg.Lark.MaxPromptBytes != 8192 {
		t.Fatalf("Lark config = %#v", cfg.Lark)
	}
	if want := []string{"oc_one", "oc_two"}; !reflect.DeepEqual(cfg.Lark.AllowedChatIDs, want) {
		t.Fatalf("AllowedChatIDs = %#v, want %#v", cfg.Lark.AllowedChatIDs, want)
	}
}

func TestExplicitEmptyHTTPAuthTokenDisablesAuth(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".ai-review.yaml", []byte("http:\n  auth_token: configured-token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("HTTP__AUTH_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTP.AuthToken != "" {
		t.Fatalf("HTTP AuthToken = %q, want empty", cfg.HTTP.AuthToken)
	}
}

func TestExplicitEmptyLarkChatAllowlistClearsConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(
		".ai-review.yaml",
		[]byte("lark:\n  allowed_chat_ids:\n    - oc_configured\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("LARK__ALLOWED_CHAT_IDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Lark.AllowedChatIDs) != 0 {
		t.Fatalf("AllowedChatIDs = %#v, want empty", cfg.Lark.AllowedChatIDs)
	}
}

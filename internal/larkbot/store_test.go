package larkbot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsSessionAndProcessedMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "lark.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if err := store.Complete("om_message", "oc_chat:om_root", "session-1"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state mode = %o, want 600", got)
	}

	reloaded, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(reload) error = %v", err)
	}
	reloaded.now = func() time.Time { return now }
	if got := reloaded.Session("oc_chat:om_root"); got != "session-1" {
		t.Fatalf("Session() = %q, want session-1", got)
	}
	if !reloaded.Processed("om_message") {
		t.Fatal("Processed() = false, want true")
	}
}

func TestStorePrunesOldProcessedMessages(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.state.Processed["old"] = now.Add(-processedRetention - time.Second)
	store.state.Processed["recent"] = now.Add(-time.Hour)

	if err := store.Complete("new", "", ""); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if store.Processed("old") {
		t.Fatal("old processed message was not pruned")
	}
	if !store.Processed("recent") || !store.Processed("new") {
		t.Fatal("recent processed messages were pruned")
	}
}

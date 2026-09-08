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

	now := time.Now().UTC().Truncate(time.Second)
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

func TestStorePersistsTagReviewThreadKindAndLoadsLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lark.json")
	legacy := `{"version":1,"sessions":{"oc_chat:om_old":"session-old"},"processed":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	if sessionID, kind := store.Thread("oc_chat:om_old"); sessionID != "session-old" || kind != "" {
		t.Fatalf("legacy thread = session %q, kind %q", sessionID, kind)
	}
	if err := store.CompleteThread("tag-event", "oc_chat:om_tag", "session-tag", threadKindTagReview); err != nil {
		t.Fatalf("CompleteThread() error = %v", err)
	}

	reloaded, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(reload) error = %v", err)
	}
	if sessionID, kind := reloaded.Thread("oc_chat:om_tag"); sessionID != "session-tag" || kind != threadKindTagReview {
		t.Fatalf("tag thread = session %q, kind %q", sessionID, kind)
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

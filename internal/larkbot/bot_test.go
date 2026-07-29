package larkbot

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGateway struct {
	mu      sync.Mutex
	parents map[string]string
	replies []string
	notify  chan struct{}
}

func (g *fakeGateway) FetchMessage(_ context.Context, messageID string) (string, error) {
	return g.parents[messageID], nil
}

func (g *fakeGateway) Reply(_ context.Context, _, _ string, markdown string) error {
	g.mu.Lock()
	g.replies = append(g.replies, markdown)
	g.mu.Unlock()
	select {
	case g.notify <- struct{}{}:
	default:
	}
	return nil
}

func (g *fakeGateway) replyCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.replies)
}

type fakeTurner struct {
	mu       sync.Mutex
	requests []TurnRequest
	busyOnce bool
}

func (t *fakeTurner) Turn(_ context.Context, request TurnRequest) (*TurnResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests = append(t.requests, request)
	if t.busyOnce {
		t.busyOnce = false
		return nil, &BusyError{StatusCode: 429, Code: "server_busy"}
	}
	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = "session-new"
	}
	return &TurnResponse{SessionID: sessionID, Message: "Codex result"}, nil
}

func (t *fakeTurner) snapshot() []TurnRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TurnRequest(nil), t.requests...)
}

func TestBotProcessesMessageAndResumesThreadSession(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic stack"},
		notify:  make(chan struct{}, 8),
	}
	turner := &fakeTurner{}
	bot, store := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	first := IncomingMessage{
		MessageID: "message-1",
		ChatID:    "chat-1",
		ChatType:  "group",
		ParentID:  "alert-1",
		RootID:    "alert-1",
		Text:      "查明并修复",
	}
	if err := bot.Handle(ctx, first); err != nil {
		t.Fatalf("Handle(first) error = %v", err)
	}
	waitForReplies(t, gateway, 2)

	if got := store.Session("chat-1:alert-1"); got != "session-new" {
		t.Fatalf("stored session = %q", got)
	}

	second := first
	second.MessageID = "message-2"
	second.Text = "继续提交 MR"
	if err := bot.Handle(ctx, second); err != nil {
		t.Fatalf("Handle(second) error = %v", err)
	}
	waitForReplies(t, gateway, 4)

	requests := turner.snapshot()
	if len(requests) != 2 {
		t.Fatalf("Codex request count = %d, want 2", len(requests))
	}
	if requests[0].SessionID != "" || requests[1].SessionID != "session-new" {
		t.Fatalf("Codex sessions = %#v", requests)
	}
	if !strings.Contains(requests[0].Message, "$nova-incident-remediation") ||
		!strings.Contains(requests[0].Message, "panic stack") {
		t.Fatalf("Codex prompt = %q", requests[0].Message)
	}

	if err := bot.Handle(ctx, first); err != nil {
		t.Fatalf("Handle(duplicate) error = %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := len(turner.snapshot()); got != 2 {
		t.Fatalf("duplicate triggered Codex; request count = %d", got)
	}
}

func TestBotRequiresARepliedMessage(t *testing.T) {
	gateway := &fakeGateway{notify: make(chan struct{}, 4)}
	turner := &fakeTurner{}
	bot, _ := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	err := bot.Handle(ctx, IncomingMessage{
		MessageID: "message-1",
		ChatID:    "chat-1",
		ChatType:  "group",
		Text:      "fix",
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	waitForReplies(t, gateway, 1)
	if got := len(turner.snapshot()); got != 0 {
		t.Fatalf("Codex request count = %d, want 0", got)
	}
}

func TestBotRetriesWhenCodexIsBusy(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic"},
		notify:  make(chan struct{}, 4),
	}
	turner := &fakeTurner{busyOnce: true}
	bot, _ := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	err := bot.Handle(ctx, IncomingMessage{
		MessageID: "message-1",
		ChatID:    "chat-1",
		ChatType:  "group",
		ParentID:  "alert-1",
		RootID:    "alert-1",
		Text:      "fix",
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	waitForReplies(t, gateway, 2)
	if got := len(turner.snapshot()); got != 2 {
		t.Fatalf("Codex request count = %d, want 2", got)
	}
}

func TestBuildPromptTruncatesOnUTF8Boundaries(t *testing.T) {
	prompt := buildPrompt("处理异常", strings.Repeat("日志中文\n", 1000), 1024)
	if len(prompt) > 1024 {
		t.Fatalf("prompt bytes = %d, want <= 1024", len(prompt))
	}
	if !strings.Contains(prompt, "中间部分已截断") {
		t.Fatalf("prompt was not truncated: %q", prompt)
	}
	if !strings.Contains(prompt, "$nova-incident-remediation") {
		t.Fatalf("prompt lost skill name: %q", prompt)
	}
}

func newTestBot(t *testing.T, gateway MessageGateway, turner Turner, requireReply bool) (*Bot, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	bot, err := NewBot(gateway, turner, store, BotConfig{
		QueueSize:      4,
		RequireReply:   requireReply,
		BusyRetry:      time.Millisecond,
		MaxPromptBytes: 4096,
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewBot() error = %v", err)
	}
	return bot, store
}

func waitForReplies(t *testing.T, gateway *fakeGateway, count int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	for gateway.replyCount() < count {
		select {
		case <-gateway.notify:
		case <-deadline.C:
			t.Fatalf("reply count = %d, want %d", gateway.replyCount(), count)
		}
	}
}

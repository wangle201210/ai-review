package larkbot

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangle201210/ai-review/internal/tagreview"
)

type sentMessage struct {
	messageID string
	chatID    string
	title     string
	markdown  string
}

type fakeGateway struct {
	mu           sync.Mutex
	parents      map[string]string
	replies      []string
	replyTargets []string
	sends        []sentMessage
	notify       chan struct{}
}

func (g *fakeGateway) Send(_ context.Context, chatID, title, markdown string) (string, error) {
	g.mu.Lock()
	messageID := fmt.Sprintf("sent-%d", len(g.sends)+1)
	g.sends = append(g.sends, sentMessage{messageID: messageID, chatID: chatID, title: title, markdown: markdown})
	g.mu.Unlock()
	select {
	case g.notify <- struct{}{}:
	default:
	}
	return messageID, nil
}

func (g *fakeGateway) FetchMessage(_ context.Context, messageID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.parents[messageID], nil
}

func (g *fakeGateway) Reply(_ context.Context, _, messageID string, markdown string) error {
	g.mu.Lock()
	g.replies = append(g.replies, markdown)
	g.replyTargets = append(g.replyTargets, messageID)
	g.mu.Unlock()
	select {
	case g.notify <- struct{}{}:
	default:
	}
	return nil
}

func (g *fakeGateway) replyTargetSnapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.replyTargets...)
}

func (g *fakeGateway) replyCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.replies)
}

func (g *fakeGateway) replySnapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.replies...)
}

func (g *fakeGateway) setParent(messageID, content string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.parents == nil {
		g.parents = make(map[string]string)
	}
	g.parents[messageID] = content
}

func (g *fakeGateway) sendSnapshot() []sentMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]sentMessage(nil), g.sends...)
}

type fakeTurner struct {
	mu                sync.Mutex
	requests          []TurnRequest
	busyOnce          bool
	timeoutOnce       bool
	policyBlockedOnce bool
	incompleteOnce    bool
}

type concurrentTurner struct {
	mu        sync.Mutex
	requests  []TurnRequest
	active    int
	maxActive int
	started   chan TurnRequest
	release   <-chan struct{}
}

func (t *concurrentTurner) Turn(ctx context.Context, request TurnRequest) (*TurnResponse, error) {
	t.mu.Lock()
	t.requests = append(t.requests, request)
	callNumber := len(t.requests)
	t.active++
	if t.active > t.maxActive {
		t.maxActive = t.active
	}
	t.mu.Unlock()

	t.started <- request
	select {
	case <-ctx.Done():
		t.finish()
		return nil, ctx.Err()
	case <-t.release:
	}
	t.finish()

	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("parallel-session-%d", callNumber)
	}
	return &TurnResponse{SessionID: sessionID, Message: "Codex result"}, nil
}

func (t *concurrentTurner) finish() {
	t.mu.Lock()
	t.active--
	t.mu.Unlock()
}

func (t *concurrentTurner) snapshot() ([]TurnRequest, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TurnRequest(nil), t.requests...), t.maxActive
}

func (t *fakeTurner) Turn(_ context.Context, request TurnRequest) (*TurnResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests = append(t.requests, request)
	if t.busyOnce {
		t.busyOnce = false
		return nil, &BusyError{StatusCode: 429, Code: "server_busy"}
	}
	if t.timeoutOnce {
		t.timeoutOnce = false
		return nil, &TurnError{
			StatusCode: 504,
			Code:       "codex_timeout",
			Message:    "Codex request timed out",
			SessionID:  "session-timeout",
		}
	}
	if t.policyBlockedOnce {
		t.policyBlockedOnce = false
		return nil, &TurnError{
			StatusCode: 502,
			Code:       "codex_policy_blocked",
			Message:    "Codex response remained blocked after automatic recovery",
			SessionID:  "session-policy",
		}
	}
	if t.incompleteOnce {
		t.incompleteOnce = false
		return nil, &TurnError{StatusCode: 502, Code: "codex_incomplete", SessionID: "session-incomplete"}
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

func TestBotRunsIndependentThreadsConcurrently(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic one", "alert-2": "panic two"},
		notify:  make(chan struct{}, 16),
	}
	release := make(chan struct{})
	turner := &concurrentTurner{
		started: make(chan TurnRequest, 2),
		release: release,
	}
	bot, _ := newTestBotWithWorkers(t, gateway, turner, true, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	for index, rootID := range []string{"alert-1", "alert-2"} {
		message := IncomingMessage{
			MessageID: fmt.Sprintf("message-%d", index+1),
			ChatID:    "chat-1",
			ChatType:  "group",
			ParentID:  rootID,
			RootID:    rootID,
			Text:      "分析并修复",
		}
		if err := bot.Handle(ctx, message); err != nil {
			t.Fatalf("Handle(%s) error = %v", rootID, err)
		}
	}
	receiveStartedTurn(t, turner.started)
	receiveStartedTurn(t, turner.started)
	if _, maxActive := turner.snapshot(); maxActive != 2 {
		t.Fatalf("maximum active turns = %d, want 2", maxActive)
	}
	close(release)
	waitForReplies(t, gateway, 4)
}

func TestBotSerializesSameThreadAndResumesSession(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic"},
		notify:  make(chan struct{}, 16),
	}
	release := make(chan struct{})
	turner := &concurrentTurner{
		started: make(chan TurnRequest, 2),
		release: release,
	}
	bot, _ := newTestBotWithWorkers(t, gateway, turner, true, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	first := IncomingMessage{
		MessageID: "message-1",
		ChatID:    "chat-1",
		ChatType:  "group",
		ParentID:  "alert-1",
		RootID:    "alert-1",
		Text:      "首次处理",
	}
	second := first
	second.MessageID = "message-2"
	second.Text = "继续处理"
	if err := bot.Handle(ctx, first); err != nil {
		t.Fatalf("Handle(first) error = %v", err)
	}
	if err := bot.Handle(ctx, second); err != nil {
		t.Fatalf("Handle(second) error = %v", err)
	}
	receiveStartedTurn(t, turner.started)
	select {
	case request := <-turner.started:
		t.Fatalf("same thread started concurrently: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	secondRequest := receiveStartedTurn(t, turner.started)
	if secondRequest.SessionID != "parallel-session-1" {
		t.Fatalf("second session = %q, want parallel-session-1", secondRequest.SessionID)
	}
	waitForReplies(t, gateway, 4)
	if _, maxActive := turner.snapshot(); maxActive != 1 {
		t.Fatalf("maximum active turns for one thread = %d, want 1", maxActive)
	}
}

func TestWaitingSameThreadDoesNotBlockIndependentThread(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic one", "alert-2": "panic two"},
		notify:  make(chan struct{}, 16),
	}
	release := make(chan struct{})
	turner := &concurrentTurner{
		started: make(chan TurnRequest, 3),
		release: release,
	}
	bot, _ := newTestBotWithWorkers(t, gateway, turner, true, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	messages := []IncomingMessage{
		{MessageID: "message-1", ChatID: "chat-1", ChatType: "group", ParentID: "alert-1", RootID: "alert-1", Text: "首次任务"},
		{MessageID: "message-2", ChatID: "chat-1", ChatType: "group", ParentID: "alert-1", RootID: "alert-1", Text: "同线程等待"},
		{MessageID: "message-3", ChatID: "chat-1", ChatType: "group", ParentID: "alert-2", RootID: "alert-2", Text: "独立任务"},
	}
	for _, message := range messages {
		if err := bot.Handle(ctx, message); err != nil {
			t.Fatalf("Handle(%s) error = %v", message.MessageID, err)
		}
	}
	first := receiveStartedTurn(t, turner.started)
	second := receiveStartedTurn(t, turner.started)
	if strings.Contains(first.Message, "同线程等待") || strings.Contains(second.Message, "同线程等待") {
		t.Fatalf("same-thread follow-up started before its predecessor: %#v %#v", first, second)
	}
	if !strings.Contains(first.Message, "独立任务") && !strings.Contains(second.Message, "独立任务") {
		t.Fatalf("independent thread did not start: %#v %#v", first, second)
	}
	if _, maxActive := turner.snapshot(); maxActive != 2 {
		t.Fatalf("maximum active turns = %d, want 2", maxActive)
	}

	close(release)
	third := receiveStartedTurn(t, turner.started)
	if !strings.Contains(third.Message, "同线程等待") || third.SessionID == "" {
		t.Fatalf("same-thread follow-up = %#v", third)
	}
	waitForReplies(t, gateway, 6)
}

func TestBotBoundsSameThreadBacklog(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic"},
		notify:  make(chan struct{}, 16),
	}
	release := make(chan struct{})
	turner := &concurrentTurner{
		started: make(chan TurnRequest, 3),
		release: release,
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	bot, err := NewBot(gateway, turner, store, BotConfig{
		QueueSize:       2,
		WorkerCount:     1,
		RequireReply:    true,
		BusyRetry:       time.Millisecond,
		MaxPromptBytes:  4096,
		TagReviewChatID: "chat-review",
		Logger:          log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewBot() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	message := IncomingMessage{
		MessageID: "message-1",
		ChatID:    "chat-1",
		ChatType:  "group",
		ParentID:  "alert-1",
		RootID:    "alert-1",
		Text:      "首次任务",
	}
	if err := bot.Handle(ctx, message); err != nil {
		t.Fatalf("Handle(first) error = %v", err)
	}
	receiveStartedTurn(t, turner.started)
	waitForQueueDepth(t, bot, 0)

	for index := 2; index <= 3; index++ {
		message.MessageID = fmt.Sprintf("message-%d", index)
		message.Text = "排队任务"
		if err := bot.Handle(ctx, message); err != nil {
			t.Fatalf("Handle(%d) error = %v", index, err)
		}
	}
	message.MessageID = "message-4"
	if err := bot.Handle(ctx, message); err != nil {
		t.Fatalf("Handle(full queue) error = %v", err)
	}

	if queued := len(bot.queued); queued != 2 {
		t.Fatalf("queued tasks = %d, want 2", queued)
	}
	replies := gateway.replySnapshot()
	if replies[len(replies)-1] != queueBusyMessage {
		t.Fatalf("queue-full reply = %q, want %q", replies[len(replies)-1], queueBusyMessage)
	}
	close(release)
	waitForReplies(t, gateway, 7)
}

func TestBotPreservesFirstSessionOnTimeoutAndResumesAfterNewMessage(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic"},
		notify:  make(chan struct{}, 8),
	}
	turner := &fakeTurner{timeoutOnce: true}
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
		Text:      "修复问题",
	}
	if err := bot.Handle(ctx, first); err != nil {
		t.Fatalf("Handle(first) error = %v", err)
	}
	waitForReplies(t, gateway, 2)
	if got := store.Session("chat-1:alert-1"); got != "session-timeout" {
		t.Fatalf("stored session = %q, want session-timeout", got)
	}

	second := first
	second.MessageID = "message-2"
	second.Text = "继续"
	if err := bot.Handle(ctx, second); err != nil {
		t.Fatalf("Handle(second) error = %v", err)
	}
	waitForReplies(t, gateway, 4)

	requests := turner.snapshot()
	if len(requests) != 2 {
		t.Fatalf("Codex request count = %d, want 2", len(requests))
	}
	if requests[0].SessionID != "" || requests[1].SessionID != "session-timeout" {
		t.Fatalf("Codex sessions = %#v", requests)
	}
}

func TestBotPreservesMessageSessionAfterPolicyRecoveryFails(t *testing.T) {
	gateway := &fakeGateway{
		parents: map[string]string{"alert-1": "panic"},
		notify:  make(chan struct{}, 8),
	}
	turner := &fakeTurner{policyBlockedOnce: true}
	bot, store := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	message := IncomingMessage{
		MessageID: "message-1",
		ChatID:    "chat-1",
		ChatType:  "group",
		ParentID:  "alert-1",
		RootID:    "alert-1",
		Text:      "分析问题",
	}
	if err := bot.Handle(ctx, message); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	waitForReplies(t, gateway, 2)

	if got := store.Session("chat-1:alert-1"); got != "session-policy" {
		t.Fatalf("stored session = %q, want session-policy", got)
	}
	replies := gateway.replySnapshot()
	if !strings.Contains(replies[len(replies)-1], "已自动恢复一次") ||
		!strings.Contains(replies[len(replies)-1], "会话已保留") {
		t.Fatalf("policy-blocked reply = %#v", replies)
	}
}

func TestBotResumesIncompleteTurns(t *testing.T) {
	for _, tagTask := range []bool{false, true} {
		t.Run(fmt.Sprint("tag_task=", tagTask), func(t *testing.T) {
			gateway := &fakeGateway{parents: map[string]string{"alert-1": "panic"}}
			turner := &fakeTurner{incompleteOnce: true}
			bot, store := newTestBot(t, gateway, turner, true)
			ctx := context.Background()
			message := IncomingMessage{MessageID: "message-1", ChatID: "chat-review", ChatType: "group", ParentID: "alert-1", RootID: "alert-1", Text: "review"}
			if tagTask {
				bot.processTagReview(ctx, tagreview.Review{ProjectID: 42, ProjectPath: "nova/game-play/kraken", Tag: "v1", CommitSHA: "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7"})
				message.RootID = "sent-1"
				message.ParentID = "sent-1"
				gateway.setParent("sent-1", "Tag review")
			} else {
				bot.process(ctx, message)
			}
			key := message.ChatID + ":" + message.RootID
			if got := store.Session(key); got != "session-incomplete" {
				t.Fatalf("session = %q", got)
			}
			replies := gateway.replySnapshot()
			if !strings.Contains(replies[len(replies)-1], "未产生完整结果") {
				t.Fatalf("reply = %q", replies[len(replies)-1])
			}
			message.MessageID = "follow-up"
			message.Text = "继续"
			bot.process(ctx, message)
			requests := turner.snapshot()
			if len(requests) != 2 || requests[1].SessionID != "session-incomplete" {
				t.Fatalf("requests = %#v", requests)
			}
			if tagTask && !strings.Contains(requests[1].Message, "$nova-tag-fund-risk-review") {
				t.Fatal("Tag follow-up lost its review context")
			}
		})
	}
}

func TestBotProcessesTagReviewAndDeduplicatesDelivery(t *testing.T) {
	gateway := &fakeGateway{notify: make(chan struct{}, 8)}
	turner := &fakeTurner{}
	bot, store := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	review := tagreview.Review{
		ProjectID:   42,
		ProjectName: "kraken",
		ProjectPath: "nova/game-play/kraken",
		ProjectURL:  "https://git.easycodesource.com/nova/game-play/kraken",
		Tag:         "version/v2.65.5",
		CommitSHA:   "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7",
	}
	duplicate, err := bot.EnqueueTagReview(ctx, review)
	if err != nil || duplicate {
		t.Fatalf("EnqueueTagReview() = duplicate %t, error %v", duplicate, err)
	}
	waitForSends(t, gateway, 1)
	waitForReplies(t, gateway, 1)
	waitForProcessed(t, store, review.DedupKey())

	requests := turner.snapshot()
	if len(requests) != 1 || requests[0].SessionID != "" {
		t.Fatalf("Codex requests = %#v", requests)
	}
	for _, expected := range []string{
		"$nova-tag-fund-risk-review",
		"公司自有且已授权仓库开展的防御性业务逻辑审计",
		"version/v2.65.5",
		"检查下注/撤销整个流程是否正常，是否没有过滤掉非法下注，比如金额为负等情况",
		"检查策略的执行是否可能产生异常的结果，是否会出现让玩家可利用从而反复套现的问题",
		"检查用户断线重连的相关逻辑，是否会导致用户的结算异常",
		"可能存在的规则漏洞会导致资金损失的",
		"规则漏洞可能是本身游戏玩法设计不合理，或者游戏的调控策略不合理导致的",
		"检查是否存在可能触发空指针的逻辑",
		"具体、可验证的复现步骤",
		"隔离测试环境",
	} {
		if !strings.Contains(requests[0].Message, expected) {
			t.Fatalf("Codex prompt does not contain %q:\n%s", expected, requests[0].Message)
		}
	}
	sends := gateway.sendSnapshot()
	if len(sends) != 1 || sends[0].messageID != "sent-1" || sends[0].chatID != "chat-review" ||
		sends[0].title != "Tag 资金风险审查已开始" {
		t.Fatalf("Lark sends = %#v", sends)
	}
	replies := gateway.replySnapshot()
	replyTargets := gateway.replyTargetSnapshot()
	if len(replies) != 1 || !strings.Contains(replies[0], "Codex result") ||
		len(replyTargets) != 1 || replyTargets[0] != "sent-1" {
		t.Fatalf("Lark replies = %#v, targets = %#v", replies, replyTargets)
	}
	threadKey := "chat-review:sent-1"
	if sessionID, kind := store.Thread(threadKey); sessionID != "session-new" || kind != threadKindTagReview {
		t.Fatalf("stored tag thread = session %q, kind %q", sessionID, kind)
	}

	duplicate, err = bot.EnqueueTagReview(ctx, review)
	if err != nil || !duplicate {
		t.Fatalf("duplicate EnqueueTagReview() = duplicate %t, error %v", duplicate, err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := len(turner.snapshot()); got != 1 {
		t.Fatalf("duplicate triggered Codex; request count = %d", got)
	}

	gateway.setParent("tag-result-1", "Tag 审查结果：Codex result")
	followUp := IncomingMessage{
		MessageID: "follow-up-1",
		ChatID:    "chat-review",
		ChatType:  "group",
		ParentID:  "tag-result-1",
		RootID:    "sent-1",
		Text:      "继续解释这个风险",
	}
	if err := bot.Handle(ctx, followUp); err != nil {
		t.Fatalf("Handle(follow-up) error = %v", err)
	}
	waitForReplies(t, gateway, 3)
	requests = turner.snapshot()
	if len(requests) != 2 || requests[1].SessionID != "session-new" {
		t.Fatalf("follow-up Codex requests = %#v", requests)
	}
	if !strings.Contains(requests[1].Message, "$nova-tag-fund-risk-review") ||
		strings.Contains(requests[1].Message, "$nova-incident-remediation") ||
		!strings.Contains(requests[1].Message, "公司自有且已授权仓库开展的防御性业务逻辑审计") ||
		!strings.Contains(requests[1].Message, "继续解释这个风险") {
		t.Fatalf("follow-up prompt = %q", requests[1].Message)
	}
	if sessionID, kind := store.Thread(threadKey); sessionID != "session-new" || kind != threadKindTagReview {
		t.Fatalf("tag thread after follow-up = session %q, kind %q", sessionID, kind)
	}
}

func TestBotPreservesTagReviewSessionAfterPolicyRecoveryFails(t *testing.T) {
	gateway := &fakeGateway{notify: make(chan struct{}, 8)}
	turner := &fakeTurner{policyBlockedOnce: true}
	bot, store := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	review := tagreview.Review{
		ProjectID:   42,
		ProjectName: "kraken",
		ProjectPath: "nova/game-play/kraken",
		ProjectURL:  "https://git.easycodesource.com/nova/game-play/kraken",
		Tag:         "version/v2.65.5",
		CommitSHA:   "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7",
	}
	if duplicate, err := bot.EnqueueTagReview(ctx, review); err != nil || duplicate {
		t.Fatalf("EnqueueTagReview() = duplicate %t, error %v", duplicate, err)
	}
	waitForSends(t, gateway, 1)
	waitForReplies(t, gateway, 1)
	waitForProcessed(t, store, review.DedupKey())

	threadKey := "chat-review:sent-1"
	if sessionID, kind := store.Thread(threadKey); sessionID != "session-policy" || kind != threadKindTagReview {
		t.Fatalf("policy-blocked tag thread = session %q, kind %q", sessionID, kind)
	}
	replies := gateway.replySnapshot()
	if len(replies) != 1 || !strings.Contains(replies[0], "已自动恢复一次") ||
		!strings.Contains(replies[0], "会话已保留") || !strings.Contains(replies[0], "@ 机器人发送“继续”") {
		t.Fatalf("policy-blocked reply = %#v", replies)
	}
}

func TestBotPreservesTagReviewSessionOnTimeout(t *testing.T) {
	gateway := &fakeGateway{notify: make(chan struct{}, 8)}
	turner := &fakeTurner{timeoutOnce: true}
	bot, store := newTestBot(t, gateway, turner, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)

	review := tagreview.Review{
		ProjectID:   42,
		ProjectName: "kraken",
		ProjectPath: "nova/game-play/kraken",
		ProjectURL:  "https://git.easycodesource.com/nova/game-play/kraken",
		Tag:         "version/v2.65.5",
		CommitSHA:   "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7",
	}
	if duplicate, err := bot.EnqueueTagReview(ctx, review); err != nil || duplicate {
		t.Fatalf("EnqueueTagReview() = duplicate %t, error %v", duplicate, err)
	}
	waitForSends(t, gateway, 1)
	waitForReplies(t, gateway, 1)
	waitForProcessed(t, store, review.DedupKey())

	if sessionID, kind := store.Thread("chat-review:sent-1"); sessionID != "session-timeout" || kind != threadKindTagReview {
		t.Fatalf("timed-out tag thread = session %q, kind %q", sessionID, kind)
	}
	replies := gateway.replySnapshot()
	if len(replies) != 1 || !strings.Contains(replies[0], "会话已保留") ||
		!strings.Contains(replies[0], "@ 机器人发送“继续”") {
		t.Fatalf("timeout reply = %#v", replies)
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
	return newTestBotWithWorkers(t, gateway, turner, requireReply, 1)
}

func newTestBotWithWorkers(
	t *testing.T,
	gateway MessageGateway,
	turner Turner,
	requireReply bool,
	workerCount int,
) (*Bot, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	bot, err := NewBot(gateway, turner, store, BotConfig{
		QueueSize:       4,
		WorkerCount:     workerCount,
		RequireReply:    requireReply,
		BusyRetry:       time.Millisecond,
		MaxPromptBytes:  4096,
		TagReviewChatID: "chat-review",
		Logger:          log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewBot() error = %v", err)
	}
	return bot, store
}

func receiveStartedTurn(t *testing.T, started <-chan TurnRequest) TurnRequest {
	t.Helper()
	select {
	case request := <-started:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("Codex turn did not start")
		return TurnRequest{}
	}
}

func waitForQueueDepth(t *testing.T, bot *Bot, want int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for len(bot.queued) != want {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("queued tasks = %d, want %d", len(bot.queued), want)
		}
	}
}

func waitForSends(t *testing.T, gateway *fakeGateway, count int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(gateway.sendSnapshot()) < count {
		select {
		case <-gateway.notify:
		case <-deadline.C:
			t.Fatalf("send count = %d, want %d", len(gateway.sendSnapshot()), count)
		}
	}
}

func waitForProcessed(t *testing.T, store *Store, key string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !store.Processed(key) {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("processed key %q was not persisted", key)
		}
	}
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

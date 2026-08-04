package larkbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultInstruction   = "请分析并处理被回复的异常。"
	queueBusyMessage     = "当前任务队列已满，请稍后重新 @ 机器人。"
	requireReplyMessage  = "请先回复需要分析的告警或日志消息，再 @ 机器人发送处理要求。"
	acceptedMessage      = "已收到，开始分析。完成后会在此回复。"
	timeoutResumeMessage = "Codex 本次执行已超时，但会话已保留。请继续回复同一条告警所在的线程，重新 @ 机器人并发送一条新消息，例如“继续”，系统将从原会话继续处理。"
)

type IncomingMessage struct {
	MessageID string
	ChatID    string
	ChatType  string
	ParentID  string
	RootID    string
	Text      string
}

type MessageGateway interface {
	FetchMessage(ctx context.Context, messageID string) (string, error)
	Reply(ctx context.Context, chatID, messageID, markdown string) error
}

type BotConfig struct {
	QueueSize      int
	RequireReply   bool
	BusyRetry      time.Duration
	MaxPromptBytes int
	Logger         *log.Logger
}

type Bot struct {
	gateway MessageGateway
	codex   Turner
	store   *Store
	config  BotConfig
	queue   chan IncomingMessage

	activeMu sync.Mutex
	active   map[string]struct{}
}

func NewBot(gateway MessageGateway, codex Turner, store *Store, cfg BotConfig) (*Bot, error) {
	if gateway == nil {
		return nil, errors.New("Lark message gateway is required")
	}
	if codex == nil {
		return nil, errors.New("Codex client is required")
	}
	if store == nil {
		return nil, errors.New("Lark state store is required")
	}
	if cfg.QueueSize < 1 {
		return nil, errors.New("Lark queue size must be at least 1")
	}
	if cfg.BusyRetry <= 0 {
		return nil, errors.New("Lark busy retry interval must be greater than zero")
	}
	if cfg.MaxPromptBytes < 1024 {
		return nil, errors.New("Lark max prompt bytes must be at least 1024")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	return &Bot{
		gateway: gateway,
		codex:   codex,
		store:   store,
		config:  cfg,
		queue:   make(chan IncomingMessage, cfg.QueueSize),
		active:  make(map[string]struct{}),
	}, nil
}

func (b *Bot) Handle(ctx context.Context, message IncomingMessage) error {
	if message.MessageID == "" || message.ChatID == "" {
		return errors.New("Lark message_id and chat_id are required")
	}
	if message.ChatType != "group" && message.ChatType != "topic_group" {
		return nil
	}
	if b.store.Processed(message.MessageID) || !b.begin(message.MessageID) {
		return nil
	}

	select {
	case b.queue <- message:
		b.config.Logger.Printf(
			"[lark-codex] queued message_id=%q chat_id=%q queue_depth=%d",
			message.MessageID,
			message.ChatID,
			len(b.queue),
		)
		return nil
	default:
		b.end(message.MessageID)
		return b.gateway.Reply(ctx, message.ChatID, message.MessageID, queueBusyMessage)
	}
}

func (b *Bot) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-b.queue:
			b.process(ctx, message)
		}
	}
}

func (b *Bot) process(ctx context.Context, message IncomingMessage) {
	defer b.end(message.MessageID)

	if b.config.RequireReply && message.ParentID == "" {
		b.finishWithReply(ctx, message, requireReplyMessage, "")
		return
	}

	parentContent := ""
	if message.ParentID != "" {
		var err error
		parentContent, err = b.gateway.FetchMessage(ctx, message.ParentID)
		if err != nil {
			b.config.Logger.Printf(
				"[lark-codex] fetch parent failed message_id=%q parent_id=%q: %v",
				message.MessageID,
				message.ParentID,
				err,
			)
			b.finishWithReply(ctx, message, "读取被回复的消息失败，请确认机器人拥有读取消息权限后重试。", "")
			return
		}
	}

	if err := b.gateway.Reply(ctx, message.ChatID, message.MessageID, acceptedMessage); err != nil {
		b.config.Logger.Printf("[lark-codex] send acknowledgement failed message_id=%q: %v", message.MessageID, err)
	}

	threadKey := message.ChatID + ":" + threadRoot(message)
	sessionID := b.store.Session(threadKey)
	prompt := buildPrompt(message.Text, parentContent, b.config.MaxPromptBytes)

	startedAt := time.Now()
	result, err := b.turnWithBusyRetry(ctx, TurnRequest{
		Message:   prompt,
		SessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		var turnErr *TurnError
		if errors.As(err, &turnErr) && turnErr.Code == "codex_timeout" && turnErr.SessionID != "" {
			b.config.Logger.Printf(
				"[lark-codex] Codex request timed out; session preserved message_id=%q session_id=%q",
				message.MessageID,
				turnErr.SessionID,
			)
			b.finishWithReply(ctx, message, timeoutResumeMessage, turnErr.SessionID)
			return
		}
		b.config.Logger.Printf(
			"[lark-codex] Codex request failed message_id=%q session_id=%q: %v",
			message.MessageID,
			sessionID,
			err,
		)
		b.finishWithReply(ctx, message, "Codex 执行失败，请查看服务器日志后重新发送任务。", "")
		return
	}

	if err := b.store.Complete(message.MessageID, threadKey, result.SessionID); err != nil {
		b.config.Logger.Printf("[lark-codex] persist completed task failed message_id=%q: %v", message.MessageID, err)
	}
	if err := b.gateway.Reply(ctx, message.ChatID, message.MessageID, result.Message); err != nil {
		b.config.Logger.Printf("[lark-codex] send result failed message_id=%q: %v", message.MessageID, err)
		return
	}
	b.config.Logger.Printf(
		"[lark-codex] completed message_id=%q session_id=%q duration=%s",
		message.MessageID,
		result.SessionID,
		time.Since(startedAt).Round(time.Millisecond),
	)
}

func (b *Bot) turnWithBusyRetry(ctx context.Context, request TurnRequest) (*TurnResponse, error) {
	for {
		result, err := b.codex.Turn(ctx, request)
		var busy *BusyError
		if !errors.As(err, &busy) {
			return result, err
		}

		delay := b.config.BusyRetry
		if busy.RetryAfter > delay {
			delay = busy.RetryAfter
		}
		b.config.Logger.Printf(
			"[lark-codex] Codex service busy session_id=%q retry_after=%s",
			request.SessionID,
			delay,
		)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (b *Bot) finishWithReply(ctx context.Context, message IncomingMessage, reply, sessionID string) {
	threadKey := ""
	if sessionID != "" {
		threadKey = message.ChatID + ":" + threadRoot(message)
	}
	if err := b.store.Complete(message.MessageID, threadKey, sessionID); err != nil {
		b.config.Logger.Printf("[lark-codex] persist terminal task failed message_id=%q: %v", message.MessageID, err)
	}
	if err := b.gateway.Reply(ctx, message.ChatID, message.MessageID, reply); err != nil {
		b.config.Logger.Printf("[lark-codex] send terminal reply failed message_id=%q: %v", message.MessageID, err)
	}
}

func (b *Bot) begin(messageID string) bool {
	b.activeMu.Lock()
	defer b.activeMu.Unlock()
	if _, exists := b.active[messageID]; exists {
		return false
	}
	b.active[messageID] = struct{}{}
	return true
}

func (b *Bot) end(messageID string) {
	b.activeMu.Lock()
	delete(b.active, messageID)
	b.activeMu.Unlock()
}

func threadRoot(message IncomingMessage) string {
	if message.RootID != "" {
		return message.RootID
	}
	if message.ParentID != "" {
		return message.ParentID
	}
	return message.MessageID
}

func buildPrompt(userMessage, parentMessage string, maxBytes int) string {
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" {
		userMessage = defaultInstruction
	}
	parentMessage = strings.TrimSpace(parentMessage)

	prompt := fmt.Sprintf(
		"$nova-incident-remediation\n\n用户本次发送的消息：\n%s\n\n用户回复/选中的消息：\n%s",
		userMessage,
		parentMessage,
	)
	return truncateMiddleUTF8(prompt, maxBytes)
}

func truncateMiddleUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}

	const marker = "\n\n[内容过长，中间部分已截断]\n\n"
	available := maxBytes - len(marker)
	if available <= 0 {
		return truncateUTF8(value, maxBytes)
	}

	headBytes := available * 2 / 3
	tailBytes := available - headBytes
	head := truncateUTF8(value, headBytes)
	tail := truncateUTF8FromEnd(value, tailBytes)
	return head + marker + tail
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func truncateUTF8FromEnd(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

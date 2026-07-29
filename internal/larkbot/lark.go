package larkbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel"
	channeltypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type ServiceConfig struct {
	AppID          string
	AppSecret      string
	BaseURL        string
	AllowedChatIDs []string
	StatePath      string
	QueueSize      int
	RequireReply   bool
	CodexURL       string
	CodexAuthToken string
	CodexTimeout   time.Duration
	BusyRetry      time.Duration
	MaxPromptBytes int
	Logger         *log.Logger
}

type Service struct {
	channel channeltypes.Channel
	bot     *Bot
	logger  *log.Logger
}

type larkGateway struct {
	client  *lark.Client
	channel channeltypes.Channel
}

func NewService(cfg ServiceConfig) (*Service, error) {
	if strings.TrimSpace(cfg.AppID) == "" {
		return nil, errors.New("LARK__APP_ID is required")
	}
	if strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, errors.New("LARK__APP_SECRET is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("LARK__BASE_URL is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	store, err := OpenStore(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	codexClient, err := NewHTTPClient(cfg.CodexURL, cfg.CodexAuthToken, cfg.CodexTimeout)
	if err != nil {
		return nil, err
	}

	apiClient := lark.NewClient(
		cfg.AppID,
		cfg.AppSecret,
		lark.WithOpenBaseUrl(cfg.BaseURL),
		lark.WithLogLevel(larkcore.LogLevelWarn),
	)
	eventDispatcher := dispatcher.NewEventDispatcher("", "")
	wsClient := larkws.NewClient(
		cfg.AppID,
		cfg.AppSecret,
		larkws.WithDomain(cfg.BaseURL),
		larkws.WithEventHandler(eventDispatcher),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
	)

	requireMention := true
	respondToMentionAll := false
	channelConfig := channeltypes.DefaultChannelConfig()
	channelConfig.Safety.Batch.DelayMs = 0
	larkChannel := channel.NewChannel(
		apiClient,
		wsClient,
		channeltypes.WithSafetyConfig(channelConfig.Safety),
		channeltypes.WithPolicyConfig(channeltypes.PolicyConfig{
			GroupAllowlist:      cfg.AllowedChatIDs,
			RequireMention:      &requireMention,
			RespondToMentionAll: &respondToMentionAll,
			DMMode:              "disabled",
		}),
	)

	gateway := &larkGateway{client: apiClient, channel: larkChannel}
	bot, err := NewBot(gateway, codexClient, store, BotConfig{
		QueueSize:      cfg.QueueSize,
		RequireReply:   cfg.RequireReply,
		BusyRetry:      cfg.BusyRetry,
		MaxPromptBytes: cfg.MaxPromptBytes,
		Logger:         cfg.Logger,
	})
	if err != nil {
		return nil, err
	}

	service := &Service{
		channel: larkChannel,
		bot:     bot,
		logger:  cfg.Logger,
	}
	service.registerHandlers()
	return service, nil
}

func (s *Service) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		s.bot.Run(runCtx)
	}()

	startResult := make(chan error, 1)
	go func() {
		startResult <- s.channel.Start(runCtx)
	}()

	select {
	case err := <-startResult:
		cancel()
		<-workerDone
		if err != nil {
			return fmt.Errorf("start Lark WebSocket channel: %w", err)
		}
		return errors.New("Lark WebSocket channel stopped unexpectedly")
	case <-ctx.Done():
		if err := s.channel.Stop(context.Background()); err != nil {
			s.logger.Printf("[lark-codex] stop channel failed: %v", err)
		}
		<-workerDone
		return nil
	}
}

func (s *Service) registerHandlers() {
	s.channel.OnReady(func() {
		s.logger.Println("[lark-codex] WebSocket connected and bot is ready")
	})
	s.channel.OnError(func(err error) {
		s.logger.Printf("[lark-codex] WebSocket error: %v", err)
	})
	s.channel.OnReconnecting(func() {
		s.logger.Println("[lark-codex] WebSocket reconnecting")
	})
	s.channel.OnReconnected(func() {
		s.logger.Println("[lark-codex] WebSocket reconnected")
	})
	s.channel.OnDisconnected(func() {
		s.logger.Println("[lark-codex] WebSocket disconnected")
	})
	s.channel.OnMessage(func(ctx context.Context, message *channeltypes.NormalizedMessage) error {
		incoming, err := incomingFromLark(message)
		if err != nil {
			s.logger.Printf("[lark-codex] ignore invalid message: %v", err)
			return nil
		}
		if err := s.bot.Handle(ctx, incoming); err != nil {
			s.logger.Printf("[lark-codex] handle message_id=%q failed: %v", message.MessageID, err)
		}
		return nil
	})
}

func (g *larkGateway) FetchMessage(ctx context.Context, messageID string) (string, error) {
	request := larkim.NewGetMessageReqBuilder().
		MessageId(messageID).
		CardMsgContentType("raw_card_content").
		Build()
	response, err := g.client.Im.V1.Message.Get(ctx, request)
	if err != nil {
		return "", fmt.Errorf("get Lark message: %w", err)
	}
	if response == nil {
		return "", errors.New("get Lark message returned an empty response")
	}
	if !response.Success() {
		return "", fmt.Errorf("get Lark message failed: code=%d message=%s", response.Code, response.Msg)
	}
	if response.Data == nil || len(response.Data.Items) == 0 {
		return "", errors.New("get Lark message returned no items")
	}

	message := response.Data.Items[0]
	if message == nil || message.MsgType == nil || message.Body == nil || message.Body.Content == nil {
		return "", errors.New("Lark message has no readable content")
	}
	if message.Deleted != nil && *message.Deleted {
		return "", errors.New("Lark message was deleted")
	}

	content, err := parseLarkMessageContent(*message.MsgType, *message.Body.Content)
	if err != nil {
		return "", fmt.Errorf("parse Lark message content: %w", err)
	}
	return content, nil
}

func (g *larkGateway) Reply(ctx context.Context, chatID, messageID, markdown string) error {
	_, err := g.channel.Send(ctx, &channeltypes.SendInput{
		ChatID:         chatID,
		ReplyMessageID: messageID,
		Markdown:       markdown,
	})
	return err
}

func incomingFromLark(message *channeltypes.NormalizedMessage) (IncomingMessage, error) {
	if message == nil {
		return IncomingMessage{}, errors.New("message is nil")
	}
	raw, ok := message.RawEvent.(*larkim.P2MessageReceiveV1)
	if !ok || raw == nil || raw.Event == nil || raw.Event.Message == nil {
		return IncomingMessage{}, errors.New("raw message event is unavailable")
	}

	eventMessage := raw.Event.Message
	return IncomingMessage{
		MessageID: message.MessageID,
		ChatID:    message.ChatID,
		ChatType:  message.ChatType,
		ParentID:  stringValue(eventMessage.ParentId),
		RootID:    stringValue(eventMessage.RootId),
		Text:      stripBotMentions(message.Content, message.Mentions),
	}, nil
}

func stripBotMentions(content string, mentions []channeltypes.Mention) string {
	for _, mention := range mentions {
		if mention.IsBot && mention.Key != "" {
			content = strings.ReplaceAll(content, mention.Key, "")
		}
	}
	return strings.TrimSpace(content)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

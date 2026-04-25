package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type ChatResult struct {
	Text             string
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int
}

type ClaudeClient struct {
	client      *anthropic.Client
	model       string
	maxTokens   int64
	temperature float64
	timeout     time.Duration
}

func NewClaudeClient(apiKey, apiURL, model string, maxTokens int, temperature float64, timeout int) *ClaudeClient {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if apiURL != "" {
		opts = append(opts, option.WithBaseURL(apiURL))
	}

	client := anthropic.NewClient(opts...)

	return &ClaudeClient{
		client:      &client,
		model:       model,
		maxTokens:   int64(maxTokens),
		temperature: temperature,
		timeout:     time.Duration(timeout) * time.Second,
	}
}

func (c *ClaudeClient) Chat(ctx context.Context, systemPrompt, userPrompt string) (*ChatResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: c.maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	if c.temperature > 0 {
		params.Temperature = anthropic.Float(c.temperature)
	}

	msg, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("claude API call: %w", err)
	}

	result := &ChatResult{
		TotalTokens:      int(msg.Usage.InputTokens + msg.Usage.OutputTokens),
		PromptTokens:     int(msg.Usage.InputTokens),
		CompletionTokens: int(msg.Usage.OutputTokens),
	}

	for _, block := range msg.Content {
		if block.Type == "text" {
			result.Text += block.Text
		}
	}

	return result, nil
}

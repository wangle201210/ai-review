package larkbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxCodexRequestBytes  = 63 * 1024
	maxCodexResponseBytes = 4 * 1024 * 1024
)

type TurnRequest struct {
	Message   string
	SessionID string
}

type TurnResponse struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Status    string `json:"status"`
}

type turnPayload struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

type Turner interface {
	Turn(ctx context.Context, request TurnRequest) (*TurnResponse, error)
}

type HTTPClient struct {
	endpoint  string
	authToken string
	client    *http.Client
}

type BusyError struct {
	StatusCode int
	Code       string
	RetryAfter time.Duration
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("Codex HTTP service is busy: status=%d code=%s", e.StatusCode, e.Code)
}

type TurnError struct {
	StatusCode int
	Code       string
	Message    string
	SessionID  string
}

func (e *TurnError) Error() string {
	return fmt.Sprintf("Codex HTTP service returned %d (%s): %s", e.StatusCode, e.Code, e.Message)
}

func NewHTTPClient(endpoint, authToken string, timeout time.Duration) (*HTTPClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Codex URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("Codex URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("Codex URL must include a host")
	}
	if timeout <= 0 {
		return nil, errors.New("Codex HTTP timeout must be greater than zero")
	}

	return &HTTPClient{
		endpoint:  parsed.String(),
		authToken: authToken,
		client:    &http.Client{Timeout: timeout},
	}, nil
}

func (c *HTTPClient) Turn(ctx context.Context, request TurnRequest) (*TurnResponse, error) {
	payload, err := marshalTurnRequest(request)
	if err != nil {
		return nil, fmt.Errorf("encode Codex request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create Codex request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	response, err := c.client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("call Codex HTTP service: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxCodexResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex response: %w", err)
	}
	if len(body) > maxCodexResponseBytes {
		return nil, errors.New("Codex response is too large")
	}

	if response.StatusCode != http.StatusOK {
		codexError := decodeCodexError(body)
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusConflict {
			return nil, &BusyError{
				StatusCode: response.StatusCode,
				Code:       codexError.Code,
				RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
			}
		}
		if codexError.Message == "" {
			codexError.Message = http.StatusText(response.StatusCode)
		}
		return nil, &TurnError{
			StatusCode: response.StatusCode,
			Code:       codexError.Code,
			Message:    codexError.Message,
			SessionID:  codexError.SessionID,
		}
	}

	var result TurnResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode Codex response: %w", err)
	}
	if result.SessionID == "" {
		return nil, errors.New("Codex response did not include session_id")
	}
	if strings.TrimSpace(result.Message) == "" {
		return nil, errors.New("Codex response did not include a message")
	}
	return &result, nil
}

func marshalTurnRequest(request TurnRequest) ([]byte, error) {
	payload, err := json.Marshal(turnPayload{
		Message:   request.Message,
		SessionID: request.SessionID,
	})
	if err != nil || len(payload) <= maxCodexRequestBytes {
		return payload, err
	}

	low, high := 0, len(request.Message)
	var best []byte
	for low <= high {
		middle := low + (high-low)/2
		candidate, marshalErr := json.Marshal(turnPayload{
			Message:   truncateMiddleUTF8(request.Message, middle),
			SessionID: request.SessionID,
		})
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(candidate) <= maxCodexRequestBytes {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best == nil {
		return nil, errors.New("Codex session_id leaves no room for a message")
	}
	return best, nil
}

func decodeCodexError(body []byte) TurnError {
	var envelope struct {
		SessionID string `json:"session_id"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return TurnError{}
	}
	return TurnError{
		Code:      envelope.Error.Code,
		Message:   envelope.Error.Message,
		SessionID: envelope.SessionID,
	}
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

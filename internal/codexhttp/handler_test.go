package codexhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wangle201210/ai-review/internal/codex"
)

const testToken = "0123456789abcdef0123456789abcdef"

type executorFunc func(context.Context, codex.Request) (*codex.Result, error)

func (f executorFunc) Execute(ctx context.Context, request codex.Request) (*codex.Result, error) {
	return f(ctx, request)
}

func TestHandlerTurn(t *testing.T) {
	var received codex.Request
	handler := newTestHandler(t, executorFunc(func(_ context.Context, request codex.Request) (*codex.Result, error) {
		received = request
		return &codex.Result{
			SessionID: "019abcde-1234-7000-8000-0123456789ab",
			Message:   "answer",
			Usage:     codex.Usage{InputTokens: 12, OutputTokens: 4},
		}, nil
	}), 1)

	unauthorized := performRequest(handler, `{"message":"hello"}`, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	response := performRequest(handler, `{"message":"continue","session_id":"thr_123"}`, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if received.Message != "continue" || received.SessionID != "thr_123" {
		t.Fatalf("received request = %#v", received)
	}

	var body turnResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.SessionID != "019abcde-1234-7000-8000-0123456789ab" || body.Message != "answer" {
		t.Fatalf("response = %#v", body)
	}
	if body.Usage.InputTokens != 12 || body.Status != "completed" {
		t.Fatalf("response = %#v", body)
	}
}

func TestHandlerAllowsRequestsWithoutConfiguredToken(t *testing.T) {
	handler, err := NewHandler(executorFunc(func(_ context.Context, request codex.Request) (*codex.Result, error) {
		return &codex.Result{
			SessionID: "thread-1",
			Message:   request.Message,
		}, nil
	}), Config{
		MaxConcurrent:   1,
		MaxRequestBytes: 64 * 1024,
		Logger:          log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	response := performRequest(handler, `{"message":"hello"}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsShortConfiguredToken(t *testing.T) {
	_, err := NewHandler(executorFunc(func(_ context.Context, _ codex.Request) (*codex.Result, error) {
		return nil, nil
	}), Config{
		AuthToken:       "short",
		MaxConcurrent:   1,
		MaxRequestBytes: 64 * 1024,
	})
	if err == nil {
		t.Fatal("NewHandler() accepted a short configured token")
	}
}

func TestHandlerRejectsInvalidRequest(t *testing.T) {
	called := false
	handler := newTestHandler(t, executorFunc(func(_ context.Context, _ codex.Request) (*codex.Result, error) {
		called = true
		return nil, nil
	}), 1)

	response := performRequest(handler, `{"message":"hello","session_id":"--last"}`, testToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("executor was called for an invalid request")
	}

	response = performRequest(handler, `{"message":"hello","unknown":true}`, testToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHandlerLimitsGlobalConcurrency(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	handler := newTestHandler(t, executorFunc(func(_ context.Context, _ codex.Request) (*codex.Result, error) {
		once.Do(func() { close(started) })
		<-release
		return &codex.Result{SessionID: "thread-1", Message: "done"}, nil
	}), 1)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- performRequest(handler, `{"message":"first"}`, testToken)
	}()
	<-started

	second := performRequest(handler, `{"message":"second"}`, testToken)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}

	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
}

func TestHandlerLocksResumedSession(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	handler := newTestHandler(t, executorFunc(func(_ context.Context, request codex.Request) (*codex.Result, error) {
		once.Do(func() { close(started) })
		<-release
		return &codex.Result{SessionID: request.SessionID, Message: "done"}, nil
	}), 2)

	requestBody := `{"message":"first","session_id":"thread-1"}`
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- performRequest(handler, requestBody, testToken)
	}()
	<-started

	second := performRequest(handler, requestBody, testToken)
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}

	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
}

func TestHandlerHealth(t *testing.T) {
	handler := newTestHandler(t, executorFunc(func(_ context.Context, _ codex.Request) (*codex.Result, error) {
		return nil, nil
	}), 1)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}

func newTestHandler(t *testing.T, executor codex.Executor, maxConcurrent int) *Handler {
	t.Helper()
	handler, err := NewHandler(executor, Config{
		AuthToken:       testToken,
		MaxConcurrent:   maxConcurrent,
		MaxRequestBytes: 64 * 1024,
		Logger:          log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func performRequest(handler http.Handler, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/codex", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

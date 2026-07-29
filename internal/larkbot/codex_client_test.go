package larkbot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var request struct {
			Message   string `json:"message"`
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Message != "fix it" || request.SessionID != "session-1" {
			t.Fatalf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"session-1","message":"done","status":"completed"}`))
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "test-token", time.Second)
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	result, err := client.Turn(context.Background(), TurnRequest{
		Message:   "fix it",
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	if result.Message != "done" || result.SessionID != "session-1" {
		t.Fatalf("Turn() = %#v", result)
	}
}

func TestHTTPClientReturnsBusyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"server_busy","message":"busy"}}`))
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	_, err = client.Turn(context.Background(), TurnRequest{Message: "fix it"})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Turn() error = %v, want BusyError", err)
	}
	if busy.Code != "server_busy" || busy.RetryAfter != 3*time.Second {
		t.Fatalf("BusyError = %#v", busy)
	}
}

func TestHTTPClientLimitsJSONEncodedRequestSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if len(body) > maxCodexRequestBytes {
			t.Fatalf("request bytes = %d, want <= %d", len(body), maxCodexRequestBytes)
		}
		var request turnPayload
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if !strings.Contains(request.Message, "中间部分已截断") {
			t.Fatalf("request message was not truncated")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"session-1","message":"done","status":"completed"}`))
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	_, err = client.Turn(context.Background(), TurnRequest{
		Message: strings.Repeat("line\n", 20000),
	})
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
}

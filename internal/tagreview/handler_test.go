package tagreview

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestHandlerAcceptsTagCreation(t *testing.T) {
	var received Review
	handler := newTestHandler(t, func(_ context.Context, review Review) (bool, error) {
		received = review
		return false, nil
	})

	recorder := performWebhook(handler, validTagPayload(), testSecret, "Tag Push Hook")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if received.ProjectID != 42 || received.ProjectPath != "nova/game-play/kraken" ||
		received.Tag != "version/v2.65.5" ||
		received.CommitSHA != "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7" {
		t.Fatalf("review = %#v", received)
	}
	var body response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "accepted" || body.Project != "nova/game-play/kraken" {
		t.Fatalf("response = %#v", body)
	}
}

func TestHandlerAllowsRequestsWithoutConfiguredSecret(t *testing.T) {
	called := false
	handler, err := NewHandler(func(_ context.Context, _ Review) (bool, error) {
		called = true
		return false, nil
	}, Config{
		AllowedHost:      "git.easycodesource.com",
		AllowedNamespace: "nova/game-play",
		MaxRequestBytes:  64 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	response := performWebhook(handler, validTagPayload(), "", "Tag Push Hook")
	if response.Code != http.StatusAccepted || !called {
		t.Fatalf("status = %d, called = %t, body = %s", response.Code, called, response.Body.String())
	}
}

func TestHandlerRejectsShortConfiguredSecret(t *testing.T) {
	_, err := NewHandler(func(_ context.Context, _ Review) (bool, error) {
		return false, nil
	}, Config{
		Secret:           "short",
		AllowedHost:      "git.easycodesource.com",
		AllowedNamespace: "nova/game-play",
		MaxRequestBytes:  64 * 1024,
	})
	if err == nil {
		t.Fatal("NewHandler() accepted a short configured secret")
	}
}

func TestHandlerIgnoresTagDeletion(t *testing.T) {
	called := false
	handler := newTestHandler(t, func(_ context.Context, _ Review) (bool, error) {
		called = true
		return false, nil
	})
	payload := `{
  "object_kind":"tag_push",
  "event_name":"tag_push",
  "after":"0000000000000000000000000000000000000000",
  "ref":"refs/tags/version/v2.65.5",
  "checkout_sha":null,
  "project":{"id":42,"name":"kraken","path_with_namespace":"nova/game-play/kraken","web_url":"https://git.easycodesource.com/nova/game-play/kraken"}
}`
	response := performWebhook(handler, payload, testSecret, "Tag Push Hook")
	if response.Code != http.StatusOK || called {
		t.Fatalf("status = %d, called = %t, body = %s", response.Code, called, response.Body.String())
	}
}

func TestHandlerAcceptsAnnotatedTagWithDifferentTagObjectSHA(t *testing.T) {
	var received Review
	handler := newTestHandler(t, func(_ context.Context, review Review) (bool, error) {
		received = review
		return false, nil
	})
	payload := strings.Replace(
		validTagPayload(),
		`"after":"82b3d5ae55f7080f1e6022629cdb57bfae7cccc7"`,
		`"after":"cf5bbc2e6a82ae844063eaabcde3fe040ac42c2a"`,
		1,
	)
	response := performWebhook(handler, payload, testSecret, "Tag Push Hook")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if received.CommitSHA != "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7" {
		t.Fatalf("commit SHA = %q", received.CommitSHA)
	}
}

func TestHandlerAcceptsOlderPayloadWithoutEventName(t *testing.T) {
	handler := newTestHandler(t, func(_ context.Context, _ Review) (bool, error) {
		return false, nil
	})
	payload := strings.Replace(validTagPayload(), `"event_name":"tag_push",`, "", 1)
	response := performWebhook(handler, payload, testSecret, "Tag Push Hook")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsUnauthenticatedOrOutOfScopeEvents(t *testing.T) {
	handler := newTestHandler(t, func(_ context.Context, _ Review) (bool, error) {
		t.Fatal("enqueue called for rejected event")
		return false, nil
	})
	tests := []struct {
		name       string
		payload    string
		secret     string
		event      string
		wantStatus int
	}{
		{name: "wrong secret", payload: validTagPayload(), secret: "wrong", event: "Tag Push Hook", wantStatus: http.StatusUnauthorized},
		{name: "wrong event", payload: validTagPayload(), secret: testSecret, event: "Push Hook", wantStatus: http.StatusBadRequest},
		{name: "outside namespace", payload: strings.ReplaceAll(validTagPayload(), "nova/game-play/kraken", "other/kraken"), secret: testSecret, event: "Tag Push Hook", wantStatus: http.StatusBadRequest},
		{name: "mismatched project URL", payload: strings.Replace(validTagPayload(), `"web_url":"https://git.easycodesource.com/nova/game-play/kraken"`, `"web_url":"https://git.easycodesource.com/nova/game-play/other"`, 1), secret: testSecret, event: "Tag Push Hook", wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performWebhook(handler, test.payload, test.secret, test.event)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestHandlerReportsDuplicateAndQueueFull(t *testing.T) {
	duplicateHandler := newTestHandler(t, func(_ context.Context, _ Review) (bool, error) {
		return true, nil
	})
	response := performWebhook(duplicateHandler, validTagPayload(), testSecret, "Tag Push Hook")
	if response.Code != http.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"duplicate"`)) {
		t.Fatalf("duplicate response = %d %s", response.Code, response.Body.String())
	}

	fullHandler := newTestHandler(t, func(_ context.Context, _ Review) (bool, error) {
		return false, ErrQueueFull
	})
	response = performWebhook(fullHandler, validTagPayload(), testSecret, "Tag Push Hook")
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "30" {
		t.Fatalf("queue response = %d %s", response.Code, response.Body.String())
	}
}

func TestReviewDedupKeyIsStableAndSpecific(t *testing.T) {
	review := Review{ProjectID: 42, Tag: "v1.0.0", CommitSHA: "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7"}
	if review.DedupKey() != review.DedupKey() {
		t.Fatal("dedup key is not stable")
	}
	other := review
	other.Tag = "v1.0.1"
	if review.DedupKey() == other.DedupKey() {
		t.Fatal("different tags have the same dedup key")
	}
}

func newTestHandler(t *testing.T, enqueue EnqueueFunc) *Handler {
	t.Helper()
	handler, err := NewHandler(enqueue, Config{
		Secret:           testSecret,
		AllowedHost:      "git.easycodesource.com",
		AllowedNamespace: "nova/game-play",
		MaxRequestBytes:  64 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func performWebhook(handler http.Handler, payload, secret, event string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewBufferString(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gitlab-Token", secret)
	request.Header.Set("X-Gitlab-Event", event)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func validTagPayload() string {
	return `{
  "object_kind":"tag_push",
  "event_name":"tag_push",
  "after":"82b3d5ae55f7080f1e6022629cdb57bfae7cccc7",
  "ref":"refs/tags/version/v2.65.5",
  "checkout_sha":"82b3d5ae55f7080f1e6022629cdb57bfae7cccc7",
  "project":{"id":42,"name":"kraken","path_with_namespace":"nova/game-play/kraken","web_url":"https://git.easycodesource.com/nova/game-play/kraken"}
}`
}

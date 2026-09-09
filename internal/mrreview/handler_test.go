package mrreview

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
const testHead = "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7"
const testMerge = "cf5bbc2e6a82ae844063eaabcde3fe040ac42c2a"

func validPayload() string {
	return `{"object_kind":"merge_request","event_type":"merge_request",
 "project":{"id":42,"name":"kraken","path_with_namespace":"nova/game-play/kraken","web_url":"https://git.easycodesource.com/nova/game-play/kraken"},
 "object_attributes":{"action":"merge","state":"merged","iid":17,"target_project_id":42,"source_branch":"fix/bet","target_branch":"main","last_commit":{"id":"` + testHead + `"},"merge_commit_sha":"` + testMerge + `","squash_commit_sha":null}}`
}

func newTestHandler(t *testing.T, enqueue EnqueueFunc) *Handler {
	t.Helper()
	h, err := NewHandler(enqueue, Config{Secret: testSecret, AllowedHost: "git.easycodesource.com", AllowedNamespace: "nova/game-play", MaxRequestBytes: 64 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func performWebhook(h http.Handler, payload, secret, event string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Gitlab-Token", secret)
	r.Header.Set("X-Gitlab-Event", event)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandlerAcceptsMergedMRVariants(t *testing.T) {
	for _, variant := range []string{"merge_commit", "fast_forward", "squash", "older_payload", "fork"} {
		t.Run(variant, func(t *testing.T) {
			payload := validPayload()
			switch variant {
			case "fast_forward":
				payload = strings.Replace(payload, `"merge_commit_sha":"`+testMerge+`"`, `"merge_commit_sha":null`, 1)
			case "squash":
				payload = strings.Replace(payload, `"squash_commit_sha":null`, `"squash_commit_sha":"`+testMerge+`"`, 1)
			case "older_payload":
				payload = strings.Replace(payload, `"event_type":"merge_request",`, "", 1)
			case "fork":
				payload = strings.Replace(payload, `"target_project_id":42`, `"source_project_id":99,"target_project_id":42`, 1)
			}
			var received Review
			h := newTestHandler(t, func(_ context.Context, r Review) (bool, error) { received = r; return false, nil })
			w := performWebhook(h, payload, testSecret, "Merge Request Hook")
			if w.Code != 202 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if received.ProjectID != 42 || received.MRIID != 17 || received.HeadSHA != testHead || received.SourceBranch != "fix/bet" || received.TargetBranch != "main" || received.MRURL != "https://git.easycodesource.com/nova/game-play/kraken/-/merge_requests/17" {
				t.Fatalf("review=%#v", received)
			}
			if variant == "fast_forward" && received.MergeCommitSHA != "" {
				t.Fatal("invented merge SHA")
			}
			if variant == "squash" && received.SquashCommitSHA != testMerge {
				t.Fatal("lost squash SHA")
			}
		})
	}
}

func TestHandlerIgnoresEventsOtherThanMRMerge(t *testing.T) {
	h := newTestHandler(t, func(context.Context, Review) (bool, error) { t.Fatal("ignored event enqueued"); return false, nil })
	for _, action := range []string{"open", "reopen", "update", "close", "approval", "approved", "unapproved", ""} {
		// Even an update to an already merged MR must not trigger a new review.
		payload := strings.Replace(validPayload(), `"action":"merge"`, `"action":"`+action+`"`, 1)
		w := performWebhook(h, payload, testSecret, "Merge Request Hook")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "mr_not_merged") {
			t.Fatalf("action=%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	w := performWebhook(h, strings.Replace(validPayload(), `"state":"merged"`, `"state":"opened"`, 1), testSecret, "Merge Request Hook")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, event := range []string{"Tag Push Hook", "Push Hook"} {
		w := performWebhook(h, `{"object_kind":"tag_push"}`, testSecret, event)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "ignored") {
			t.Fatalf("%s: %s", event, w.Body.String())
		}
	}
}

func TestHandlerOnlyQueuesMergesIntoMain(t *testing.T) {
	for _, target := range []string{"main", "develop", "release/v1", "master", "Main", "main-feature", ""} {
		t.Run("target="+target, func(t *testing.T) {
			var queued []Review
			h := newTestHandler(t, func(_ context.Context, review Review) (bool, error) {
				queued = append(queued, review)
				return false, nil
			})
			payload := strings.Replace(validPayload(), `"target_branch":"main"`, `"target_branch":"`+target+`"`, 1)
			if target != "main" {
				// A merge from main into another branch must not trigger a review.
				payload = strings.Replace(payload, `"source_branch":"fix/bet"`, `"source_branch":"main"`, 1)
			}
			w := performWebhook(h, payload, testSecret, "Merge Request Hook")
			var body response
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if target == "main" {
				if w.Code != http.StatusAccepted || len(queued) != 1 || queued[0].TargetBranch != "main" {
					t.Fatalf("main merge: status=%d queued=%#v", w.Code, queued)
				}
			} else if w.Code != http.StatusOK || body.Status != "ignored" || body.Reason != "target_branch_not_main" || len(queued) != 0 {
				t.Fatalf("other target: status=%d body=%#v queued=%#v", w.Code, body, queued)
			}
		})
	}
}

func TestHandlerRejectsMalformedAndOutOfScopeMerge(t *testing.T) {
	h := newTestHandler(t, func(context.Context, Review) (bool, error) { t.Fatal("invalid event enqueued"); return false, nil })
	tests := []struct{ name, from, to string }{
		{"kind", `"object_kind":"merge_request"`, `"object_kind":"tag_push"`},
		{"event type", `"event_type":"merge_request"`, `"event_type":"push"`},
		{"namespace", "nova/game-play/kraken", "other/kraken"},
		{"host", "https://git.easycodesource.com/", "https://example.com/"},
		{"target mismatch", `"target_project_id":42`, `"target_project_id":99`},
		{"iid", `"iid":17`, `"iid":0`},
		{"head", testHead, "bad-sha"},
		{"zero head", testHead, strings.Repeat("0", 40)},
		{"merge", testMerge, "bad-sha"},
		{"branch", `"source_branch":"fix/bet"`, `"source_branch":"-bad"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := performWebhook(h, strings.Replace(validPayload(), tt.from, tt.to, 1), testSecret, "Merge Request Hook")
			if w.Code != 400 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, payload := range []string{`{`, validPayload() + `{}`} {
		if w := performWebhook(h, payload, testSecret, "Merge Request Hook"); w.Code != 400 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if w := performWebhook(h, validPayload(), "wrong", "Merge Request Hook"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	h.maxRequestBytes = 32
	if w := performWebhook(h, validPayload(), testSecret, "Merge Request Hook"); w.Code != 413 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestHandlerOptionalSecretAndHTTPContract(t *testing.T) {
	h := newTestHandler(t, func(context.Context, Review) (bool, error) { return false, nil })
	h.secret = ""
	if w := performWebhook(h, validPayload(), "", "Merge Request Hook"); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, WebhookPath, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(validPayload()))
	r.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
	_, err := NewHandler(func(context.Context, Review) (bool, error) { return false, nil }, Config{Secret: "short"})
	if err == nil {
		t.Fatal("accepted short secret")
	}
}

func TestHandlerReportsDuplicateAndQueueFull(t *testing.T) {
	for _, full := range []bool{false, true} {
		h := newTestHandler(t, func(context.Context, Review) (bool, error) {
			if full {
				return false, ErrQueueFull
			}
			return true, nil
		})
		w := performWebhook(h, validPayload(), testSecret, "Merge Request Hook")
		if full {
			if w.Code != 429 || w.Header().Get("Retry-After") != "30" {
				t.Fatal(w.Body.String())
			}
		} else {
			var body response
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 202 || body.Status != "duplicate" || body.MRIID != 17 || !bytes.Contains(w.Body.Bytes(), []byte("kraken")) {
				t.Fatal(w.Body.String())
			}
		}
	}
}

func TestReviewDedupKeyUsesProjectAndMR(t *testing.T) {
	r := Review{ProjectID: 42, MRIID: 17, HeadSHA: testHead}
	other := r
	other.MergeCommitSHA = testMerge
	if r.DedupKey() != other.DedupKey() {
		t.Fatal("optional metadata causes duplicate review")
	}
	other.MRIID++
	if r.DedupKey() == other.DedupKey() {
		t.Fatal("different MRs collide")
	}
	other = r
	other.ProjectID++
	if r.DedupKey() == other.DedupKey() {
		t.Fatal("different projects collide")
	}
}

package mrreview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	WebhookPath = "/webhooks/gitlab/mr"
	// LegacyWebhookPath accepts MR events at the old URL during migration.
	LegacyWebhookPath = "/webhooks/gitlab/tag"
)

var (
	ErrQueueFull = errors.New("MR review queue is full")
	pathPartRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	shaRE        = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
)

type Review struct {
	ProjectID       int
	ProjectName     string
	ProjectPath     string
	ProjectURL      string
	MRIID           int
	MRURL           string
	SourceBranch    string
	TargetBranch    string
	HeadSHA         string
	MergeCommitSHA  string
	SquashCommitSHA string
}

func (r Review) DedupKey() string {
	// An MR can be merged only once; optional SHA fields vary across deliveries.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d", r.ProjectID, r.MRIID)))
	return "gitlab-mr:" + hex.EncodeToString(sum[:])
}

type EnqueueFunc func(context.Context, Review) (duplicate bool, err error)

type Config struct {
	Secret           string
	AllowedHost      string
	AllowedNamespace string
	MaxRequestBytes  int64
	Logger           *log.Logger
}

type Handler struct {
	enqueue          EnqueueFunc
	secret           string
	allowedHost      string
	allowedNamespace string
	maxRequestBytes  int64
	logger           *log.Logger
}

type mergeRequestPayload struct {
	ObjectKind string `json:"object_kind"`
	EventType  string `json:"event_type"`
	Project    struct {
		ID                int    `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
	} `json:"project"`
	Attributes struct {
		Action          string `json:"action"`
		State           string `json:"state"`
		IID             int    `json:"iid"`
		TargetProjectID int    `json:"target_project_id"`
		SourceBranch    string `json:"source_branch"`
		TargetBranch    string `json:"target_branch"`
		MergeCommitSHA  string `json:"merge_commit_sha"`
		SquashCommitSHA string `json:"squash_commit_sha"`
		LastCommit      struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

type response struct {
	Status  string `json:"status"`
	Project string `json:"project,omitempty"`
	MRIID   int    `json:"mr_iid,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func NewHandler(enqueue EnqueueFunc, cfg Config) (*Handler, error) {
	if enqueue == nil {
		return nil, errors.New("MR review enqueuer is required")
	}
	if cfg.Secret != "" && len(cfg.Secret) < 32 {
		return nil, errors.New("GitLab MR review webhook secret must be at least 32 bytes")
	}
	allowedHost := strings.ToLower(strings.TrimSpace(cfg.AllowedHost))
	if allowedHost == "" || strings.ContainsAny(allowedHost, "/:@") {
		return nil, errors.New("GitLab MR review allowed host is invalid")
	}
	allowedNamespace := strings.Trim(strings.TrimSpace(cfg.AllowedNamespace), "/")
	if !validProjectPath(allowedNamespace) {
		return nil, errors.New("GitLab MR review allowed namespace is invalid")
	}
	if cfg.MaxRequestBytes < 1 {
		return nil, errors.New("GitLab MR review max request bytes must be greater than zero")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Handler{
		enqueue:          enqueue,
		secret:           cfg.Secret,
		allowedHost:      allowedHost,
		allowedNamespace: allowedNamespace,
		maxRequestBytes:  cfg.MaxRequestBytes,
		logger:           cfg.Logger,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, response{Status: "rejected", Reason: "method_not_allowed"})
		return
	}
	if h.secret != "" && !h.authorized(r.Header.Get("X-Gitlab-Token")) {
		writeJSON(w, http.StatusUnauthorized, response{Status: "rejected", Reason: "unauthorized"})
		return
	}
	if r.Header.Get("X-Gitlab-Event") != "Merge Request Hook" {
		writeJSON(w, http.StatusOK, response{Status: "ignored", Reason: "not_a_merge_request_hook"})
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeJSON(w, http.StatusUnsupportedMediaType, response{Status: "rejected", Reason: "content_type_must_be_json"})
		return
	}

	payload, err := h.decode(w, r)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeJSON(w, http.StatusRequestEntityTooLarge, response{Status: "rejected", Reason: "request_too_large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, response{Status: "rejected", Reason: err.Error()})
		return
	}
	if payload.Attributes.Action != "merge" || payload.Attributes.State != "merged" {
		writeJSON(w, http.StatusOK, response{Status: "ignored", Reason: "mr_not_merged"})
		return
	}

	review, err := h.reviewFromPayload(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, response{Status: "rejected", Reason: err.Error()})
		return
	}
	duplicate, err := h.enqueue(r.Context(), review)
	if errors.Is(err, ErrQueueFull) {
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusTooManyRequests, response{Status: "rejected", Reason: "queue_full"})
		return
	}
	if err != nil {
		h.logger.Printf("[gitlab-mr-review] enqueue failed project=%q mr=%d: %v", review.ProjectPath, review.MRIID, err)
		writeJSON(w, http.StatusInternalServerError, response{Status: "rejected", Reason: "enqueue_failed"})
		return
	}

	status := "accepted"
	if duplicate {
		status = "duplicate"
	}
	h.logger.Printf("[gitlab-mr-review] webhook %s project=%q mr=%d", status, review.ProjectPath, review.MRIID)
	writeJSON(w, http.StatusAccepted, response{
		Status:  status,
		Project: review.ProjectPath,
		MRIID:   review.MRIID,
	})
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request) (mergeRequestPayload, error) {
	body := http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	var payload mergeRequestPayload
	if err := decoder.Decode(&payload); err != nil {
		return mergeRequestPayload{}, fmt.Errorf("invalid_json: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return mergeRequestPayload{}, errors.New("request_must_contain_one_json_object")
	}
	if payload.ObjectKind != "merge_request" || payload.EventType != "" && payload.EventType != "merge_request" {
		return mergeRequestPayload{}, errors.New("invalid_merge_request_payload")
	}
	return payload, nil
}

func (h *Handler) reviewFromPayload(payload mergeRequestPayload) (Review, error) {
	a := payload.Attributes
	if a.IID < 1 {
		return Review{}, errors.New("invalid_mr_iid")
	}
	if a.TargetProjectID != payload.Project.ID {
		return Review{}, errors.New("mismatched_target_project")
	}
	if !validBranch(a.SourceBranch) || !validBranch(a.TargetBranch) {
		return Review{}, errors.New("invalid_mr_branch")
	}
	if payload.Project.ID < 1 {
		return Review{}, errors.New("invalid_project_id")
	}
	projectPath := strings.Trim(payload.Project.PathWithNamespace, "/")
	if len(projectPath) > 512 || !validProjectPath(projectPath) || !strings.HasPrefix(projectPath, h.allowedNamespace+"/") {
		return Review{}, errors.New("project_outside_allowed_namespace")
	}
	projectURL, err := url.Parse(payload.Project.WebURL)
	if err != nil || projectURL.Scheme != "https" || strings.ToLower(projectURL.Hostname()) != h.allowedHost ||
		projectURL.User != nil || projectURL.Port() != "" || strings.Trim(projectURL.Path, "/") != projectPath ||
		projectURL.RawQuery != "" || projectURL.Fragment != "" {
		return Review{}, errors.New("project_outside_allowed_host")
	}
	if !shaRE.MatchString(a.LastCommit.ID) {
		return Review{}, errors.New("invalid_head_sha")
	}
	for _, sha := range []string{a.MergeCommitSHA, a.SquashCommitSHA} {
		if sha != "" && (!shaRE.MatchString(sha) || isZeroSHA(sha)) {
			return Review{}, errors.New("invalid_merge_sha")
		}
	}
	if isZeroSHA(a.LastCommit.ID) {
		return Review{}, errors.New("invalid_head_sha")
	}
	projectName := strings.TrimSpace(payload.Project.Name)
	if projectName == "" || len(projectName) > 255 || strings.ContainsAny(projectName, "\r\n") {
		return Review{}, errors.New("invalid_project_name")
	}
	return Review{
		ProjectID:       payload.Project.ID,
		ProjectName:     projectName,
		ProjectPath:     projectPath,
		ProjectURL:      strings.TrimRight(payload.Project.WebURL, "/"),
		MRIID:           a.IID,
		MRURL:           fmt.Sprintf("%s/-/merge_requests/%d", strings.TrimRight(payload.Project.WebURL, "/"), a.IID),
		SourceBranch:    a.SourceBranch,
		TargetBranch:    a.TargetBranch,
		HeadSHA:         strings.ToLower(a.LastCommit.ID),
		MergeCommitSHA:  strings.ToLower(a.MergeCommitSHA),
		SquashCommitSHA: strings.ToLower(a.SquashCommitSHA),
	}, nil
}

func (h *Handler) authorized(provided string) bool {
	if len(provided) != len(h.secret) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) == 1
}

func validProjectPath(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !pathPartRE.MatchString(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validBranch(tag string) bool {
	if tag == "" || len(tag) > 255 || strings.HasPrefix(tag, "-") || strings.HasSuffix(tag, ".") ||
		strings.Contains(tag, "..") || strings.Contains(tag, "@{") || strings.Contains(tag, "//") ||
		strings.HasPrefix(tag, "/") || strings.HasSuffix(tag, "/") {
		return false
	}
	for _, char := range tag {
		if char < 0x20 || char == 0x7f || strings.ContainsRune(" `~^:?*[\\", char) {
			return false
		}
	}
	for _, part := range strings.Split(tag, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func isZeroSHA(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, char := range value {
		if char != '0' {
			return false
		}
	}
	return true
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

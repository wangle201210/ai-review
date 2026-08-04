package codexhttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangle201210/ai-review/internal/codex"
)

const minimumTokenBytes = 32

type Config struct {
	AuthToken       string
	MaxConcurrent   int
	MaxRequestBytes int64
	Logger          *log.Logger
}

type Handler struct {
	executor        codex.Executor
	authToken       string
	maxRequestBytes int64
	logger          *log.Logger
	slots           chan struct{}

	activeMu       sync.Mutex
	activeSessions map[string]struct{}
}

type turnRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

type turnResponse struct {
	SessionID string      `json:"session_id"`
	Message   string      `json:"message"`
	Status    string      `json:"status"`
	Usage     codex.Usage `json:"usage"`
}

type errorEnvelope struct {
	Error     errorResponse `json:"error"`
	SessionID string        `json:"session_id,omitempty"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewHandler(executor codex.Executor, cfg Config) (*Handler, error) {
	if executor == nil {
		return nil, errors.New("codex executor is required")
	}
	if cfg.AuthToken != "" && len(cfg.AuthToken) < minimumTokenBytes {
		return nil, fmt.Errorf("HTTP auth token must be at least %d bytes", minimumTokenBytes)
	}
	if cfg.MaxConcurrent < 1 {
		return nil, errors.New("HTTP max concurrent requests must be at least 1")
	}
	if cfg.MaxRequestBytes < 1 {
		return nil, errors.New("HTTP max request bytes must be at least 1")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	return &Handler{
		executor:        executor,
		authToken:       cfg.AuthToken,
		maxRequestBytes: cfg.MaxRequestBytes,
		logger:          cfg.Logger,
		slots:           make(chan struct{}, cfg.MaxConcurrent),
		activeSessions:  make(map[string]struct{}),
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		h.handleHealth(w, r)
	case r.URL.Path == "/v1/codex":
		h.handleTurn(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleTurn(w http.ResponseWriter, r *http.Request) {
	if !h.validatePostRequest(w, r) {
		return
	}

	request, err := h.decodeRequest(w, r)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if request.SessionID != "" && !codex.ValidSessionID(request.SessionID) {
		writeError(w, http.StatusBadRequest, "invalid_session_id", "session_id contains unsupported characters")
		return
	}
	release, ok := h.acquireExecution(w, request.SessionID)
	if !ok {
		return
	}
	defer release()

	startedAt := time.Now()
	result, err := h.executor.Execute(r.Context(), codex.Request{
		Message:   request.Message,
		SessionID: request.SessionID,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			sessionID := request.SessionID
			if result != nil && codex.ValidSessionID(result.SessionID) {
				sessionID = result.SessionID
			}
			h.logger.Printf(
				"[codex-http] request timed out session_id=%q duration=%s session_preserved=%t",
				sessionID,
				time.Since(startedAt).Round(time.Millisecond),
				sessionID != "",
			)
			writeErrorWithSession(
				w,
				http.StatusGatewayTimeout,
				"codex_timeout",
				"Codex request timed out",
				sessionID,
			)
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		h.logger.Printf("[codex-http] request failed session_id=%q: %v", request.SessionID, err)
		writeError(w, http.StatusBadGateway, "codex_failed", "Codex execution failed")
		return
	}

	h.logger.Printf("[codex-http] request completed session_id=%q duration=%s",
		result.SessionID, time.Since(startedAt).Round(time.Millisecond))
	writeJSON(w, http.StatusOK, turnResponse{
		SessionID: result.SessionID,
		Message:   result.Message,
		Status:    "completed",
		Usage:     result.Usage,
	})
}

func (h *Handler) decodeRequest(w http.ResponseWriter, r *http.Request) (turnRequest, error) {
	body := http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	defer body.Close()

	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()

	var request turnRequest
	if err := decoder.Decode(&request); err != nil {
		return turnRequest{}, fmt.Errorf("decode JSON request: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return turnRequest{}, err
	}
	if strings.TrimSpace(request.Message) == "" {
		return turnRequest{}, errors.New("message is required")
	}
	return request, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON object")
		}
		return fmt.Errorf("decode trailing request data: %w", err)
	}
	return nil
}

func (h *Handler) validatePostRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return false
	}
	if h.authToken != "" && !h.authorized(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ai-review"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return false
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	return true
}

func (h *Handler) acquireExecution(w http.ResponseWriter, sessionID string) (func(), bool) {
	if sessionID != "" && !h.beginSession(sessionID) {
		writeError(w, http.StatusConflict, "session_busy", "another request is already running for this session_id")
		return nil, false
	}

	select {
	case h.slots <- struct{}{}:
		return func() {
			<-h.slots
			if sessionID != "" {
				h.endSession(sessionID)
			}
		}, true
	default:
		if sessionID != "" {
			h.endSession(sessionID)
		}
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "server_busy", "maximum concurrent Codex requests reached")
		return nil, false
	}
}

func (h *Handler) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	if len(provided) != len(h.authToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.authToken)) == 1
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func (h *Handler) beginSession(sessionID string) bool {
	h.activeMu.Lock()
	defer h.activeMu.Unlock()

	if _, exists := h.activeSessions[sessionID]; exists {
		return false
	}
	h.activeSessions[sessionID] = struct{}{}
	return true
}

func (h *Handler) endSession(sessionID string) {
	h.activeMu.Lock()
	delete(h.activeSessions, sessionID)
	h.activeMu.Unlock()
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeErrorWithSession(w, status, code, message, "")
}

func writeErrorWithSession(w http.ResponseWriter, status int, code, message, sessionID string) {
	writeJSON(w, status, errorEnvelope{
		Error: errorResponse{
			Code:    code,
			Message: message,
		},
		SessionID: sessionID,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

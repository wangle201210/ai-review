package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxEventLineBytes = 10 * 1024 * 1024
	maxStderrBytes    = 64 * 1024
)

type Request struct {
	Message   string
	SessionID string
}

type Usage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

type Result struct {
	SessionID string
	Message   string
	Usage     Usage
}

type Executor interface {
	Execute(ctx context.Context, request Request) (*Result, error)
}

type RunnerConfig struct {
	Binary           string
	WorkDir          string
	Sandbox          string
	Timeout          time.Duration
	SkipGitRepoCheck bool
	NetworkAccess    bool
	Logger           *log.Logger
}

type Runner struct {
	binary           string
	workDir          string
	sandbox          string
	timeout          time.Duration
	skipGitRepoCheck bool
	networkAccess    bool
	logger           *log.Logger
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Binary == "" {
		cfg.Binary = "codex"
	}
	binary, err := exec.LookPath(cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("find codex binary %q: %w", cfg.Binary, err)
	}

	if cfg.WorkDir == "" {
		return nil, errors.New("codex work directory is required")
	}
	workDir, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("resolve codex work directory: %w", err)
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return nil, fmt.Errorf("stat codex work directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("codex work directory is not a directory: %s", workDir)
	}

	switch cfg.Sandbox {
	case "read-only", "workspace-write":
	default:
		return nil, fmt.Errorf("unsupported codex sandbox %q: use read-only or workspace-write", cfg.Sandbox)
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("codex timeout must be greater than zero")
	}
	if cfg.NetworkAccess && cfg.Sandbox != "workspace-write" {
		return nil, errors.New("codex network access requires the workspace-write sandbox")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	return &Runner{
		binary:           binary,
		workDir:          workDir,
		sandbox:          cfg.Sandbox,
		timeout:          cfg.Timeout,
		skipGitRepoCheck: cfg.SkipGitRepoCheck,
		networkAccess:    cfg.NetworkAccess,
		logger:           cfg.Logger,
	}, nil
}

func (r *Runner) Execute(ctx context.Context, request Request) (*Result, error) {
	if strings.TrimSpace(request.Message) == "" {
		return nil, errors.New("message is required")
	}
	if request.SessionID != "" && !ValidSessionID(request.SessionID) {
		return nil, errors.New("invalid session id")
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.binary, r.args(request.SessionID)...)
	prepareCommand(cmd)
	cmd.Dir = r.workDir
	cmd.Stdin = strings.NewReader(request.Message)
	cmd.Env = codexEnvironment(os.Environ())

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}
	pid := cmd.Process.Pid
	r.logger.Printf(
		"[codex-cli] started pid=%d resume_session_id=%q network_access=%t",
		pid,
		request.SessionID,
		r.networkAccess,
	)
	r.logInput(pid, request.SessionID, request.Message)

	stderrDone := make(chan stderrCapture, 1)
	go func() {
		stderrDone <- captureStderr(stderr, r.logger, pid)
	}()

	state, scanErr := scanEventsWithObservers(
		stdout,
		func(line []byte) {
			r.logger.Printf("[codex-cli stdout pid=%d] %s", pid, line)
		},
		func(event streamEvent) {
			r.logProgressEvent(pid, event)
		},
	)
	if scanErr != nil {
		_ = cmd.Cancel()
		stderrState := <-stderrDone
		_ = cmd.Wait()
		if stderrState.err != nil {
			r.logger.Printf("[codex-cli stderr pid=%d] read failed: %v", pid, stderrState.err)
		}
		return nil, fmt.Errorf("read codex event stream: %w", scanErr)
	}

	stderrState := <-stderrDone
	waitErr := cmd.Wait()
	if stderrState.err != nil {
		return nil, fmt.Errorf("read codex stderr: %w", stderrState.err)
	}
	if runCtx.Err() != nil {
		return nil, runCtx.Err()
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderrState.output.String())
		if detail == "" {
			return nil, fmt.Errorf("codex process failed: %w", waitErr)
		}
		return nil, fmt.Errorf("codex process failed: %w: %s", waitErr, detail)
	}
	r.logger.Printf("[codex-cli] exited pid=%d status=0", pid)
	if state.failure != "" {
		return nil, fmt.Errorf("codex turn failed: %s", state.failure)
	}

	sessionID := state.sessionID
	if sessionID == "" {
		sessionID = request.SessionID
	}
	if sessionID == "" {
		return nil, errors.New("codex response did not include a session id")
	}
	if state.message == "" {
		return nil, errors.New("codex response did not include a final agent message")
	}

	return &Result{
		SessionID: sessionID,
		Message:   state.message,
		Usage:     state.usage,
	}, nil
}

func (r *Runner) args(sessionID string) []string {
	args := []string{
		"--cd", r.workDir,
		"--sandbox", r.sandbox,
		"--ask-for-approval", "never",
	}
	if r.networkAccess {
		args = append(args,
			"--config", "sandbox_workspace_write.network_access=true",
		)
	}
	args = append(args, "exec")

	if sessionID != "" {
		args = append(args, "resume", "--json")
		if r.skipGitRepoCheck {
			args = append(args, "--skip-git-repo-check")
		}
		return append(args, sessionID, "-")
	}

	args = append(args,
		"--json",
		"--color", "never",
	)
	if r.skipGitRepoCheck {
		args = append(args, "--skip-git-repo-check")
	}
	return append(args, "-")
}

func ValidSessionID(sessionID string) bool {
	if len(sessionID) == 0 || len(sessionID) > 128 {
		return false
	}
	for i, char := range sessionID {
		if isASCIIAlphaNumeric(char) {
			continue
		}
		if i > 0 && (char == '-' || char == '_' || char == '.') {
			continue
		}
		return false
	}
	return true
}

func isASCIIAlphaNumeric(char rune) bool {
	return char >= 'a' && char <= 'z' ||
		char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9'
}

func codexEnvironment(environ []string) []string {
	blocked := map[string]struct{}{
		"AI_REVIEW_HTTP_TOKEN":        {},
		"ANTHROPIC_API_KEY":           {},
		"GITHUB_TOKEN":                {},
		"GITLAB_TOKEN":                {},
		"HTTP__AUTH_TOKEN":            {},
		"LLM__API_TOKEN":              {},
		"VCS__HTTP_CLIENT__API_TOKEN": {},
	}

	cleaned := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if _, shouldBlock := blocked[name]; found && shouldBlock {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

type eventState struct {
	sessionID string
	message   string
	failure   string
	completed bool
	usage     Usage
}

type streamEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Message  string          `json:"message"`
	Error    json.RawMessage `json:"error"`
	Item     streamItem      `json:"item"`
	Turn     struct {
		Error json.RawMessage `json:"error"`
	} `json:"turn"`
	Usage *Usage `json:"usage"`
}

type streamItem struct {
	ID               string             `json:"id"`
	Type             string             `json:"type"`
	Text             string             `json:"text"`
	Command          string             `json:"command"`
	AggregatedOutput string             `json:"aggregated_output"`
	Status           string             `json:"status"`
	ExitCode         *int               `json:"exit_code"`
	Server           string             `json:"server"`
	Tool             string             `json:"tool"`
	Name             string             `json:"name"`
	Query            string             `json:"query"`
	Changes          []streamFileChange `json:"changes"`
	Items            []streamPlanItem   `json:"items"`
	Plan             []streamPlanItem   `json:"plan"`
}

type streamFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type streamPlanItem struct {
	Text      string `json:"text"`
	Step      string `json:"step"`
	Status    string `json:"status"`
	Completed bool   `json:"completed"`
}

func scanEvents(reader io.Reader) (eventState, error) {
	return scanEventsWithObservers(reader, nil, nil)
}

func scanEventsWithObservers(
	reader io.Reader,
	logLine func([]byte),
	logEvent func(streamEvent),
) (eventState, error) {
	var state eventState
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxEventLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if logLine != nil {
			logLine(line)
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return eventState{}, fmt.Errorf("decode JSONL event: %w", err)
		}
		if logEvent != nil {
			logEvent(event)
		}

		switch event.Type {
		case "thread.started":
			if event.ThreadID != "" {
				state.sessionID = event.ThreadID
			}
		case "item.completed":
			if event.Item.Type == "agent_message" && event.Item.Text != "" {
				state.message = event.Item.Text
			}
		case "turn.completed":
			state.completed = true
			state.failure = ""
			if event.Usage != nil {
				state.usage = *event.Usage
			}
		case "turn.failed":
			state.completed = false
			state.failure = eventFailure(event)
		case "error":
			if !state.completed {
				state.failure = eventFailure(event)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return eventState{}, err
	}
	return state, nil
}

func eventFailure(event streamEvent) string {
	detail := decodeEventError(event.Error)
	if detail == "" {
		detail = decodeEventError(event.Turn.Error)
	}
	if detail == "" {
		detail = strings.TrimSpace(event.Message)
	}
	if detail == "" {
		detail = event.Type
	}
	return detail
}

type stderrCapture struct {
	output cappedBuffer
	err    error
}

func captureStderr(reader io.Reader, logger *log.Logger, pid int) stderrCapture {
	var result stderrCapture
	result.output.limit = maxStderrBytes

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxEventLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if logger != nil {
			logger.Printf("[codex-cli stderr pid=%d] %s", pid, line)
		}
		_, _ = result.output.Write(line)
		_, _ = result.output.Write([]byte("\n"))
	}
	result.err = scanner.Err()
	return result
}

func decodeEventError(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var value struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &value); err == nil && value.Message != "" {
		return value.Message
	}

	return string(raw)
}

type cappedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		b.data = append(b.data, data...)
	}
	if written > remaining {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedBuffer) String() string {
	value := string(b.data)
	if b.truncated {
		value += "\n[stderr truncated]"
	}
	return value
}

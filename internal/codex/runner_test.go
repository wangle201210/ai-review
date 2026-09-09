package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunnerArgs(t *testing.T) {
	runner := &Runner{
		workDir:          "/srv/project",
		sandbox:          "workspace-write",
		skipGitRepoCheck: true,
		networkAccess:    true,
	}

	newSession := runner.args("")
	expectedNew := []string{
		"--cd", "/srv/project",
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
		"--config", "sandbox_workspace_write.network_access=true",
		"exec",
		"--json",
		"--color", "never",
		"--skip-git-repo-check",
		"-",
	}
	if !reflect.DeepEqual(newSession, expectedNew) {
		t.Fatalf("new session args = %#v, want %#v", newSession, expectedNew)
	}

	resumed := runner.args("019abcde-1234-7000-8000-0123456789ab")
	expectedResumed := []string{
		"--cd", "/srv/project",
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
		"--config", "sandbox_workspace_write.network_access=true",
		"exec",
		"resume",
		"--json",
		"--skip-git-repo-check",
		"019abcde-1234-7000-8000-0123456789ab",
		"-",
	}
	if !reflect.DeepEqual(resumed, expectedResumed) {
		t.Fatalf("resume args = %#v, want %#v", resumed, expectedResumed)
	}
}

func TestValidSessionID(t *testing.T) {
	tests := map[string]bool{
		"019abcde-1234-7000-8000-0123456789ab": true,
		"thr_123":                              true,
		"thread.name-1":                        true,
		"":                                     false,
		"--last":                               false,
		"contains space":                       false,
		"../../etc/passwd":                     false,
		"会话":                                   false,
	}

	for sessionID, expected := range tests {
		if actual := ValidSessionID(sessionID); actual != expected {
			t.Errorf("ValidSessionID(%q) = %v, want %v", sessionID, actual, expected)
		}
	}
}

func TestCodexEnvironmentRemovesServiceSecrets(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"HTTP__AUTH_TOKEN=secret",
		"AI_REVIEW_HTTP_TOKEN=secret",
		"GITHUB_TOKEN=secret",
		"GITLAB_TAG_REVIEW__SECRET=secret",
		"LARK__APP_SECRET=secret",
		"CODEX_HOME=/var/lib/codex",
	}

	cleaned := codexEnvironment(environ)
	expected := []string{
		"PATH=/usr/bin",
		"CODEX_HOME=/var/lib/codex",
	}
	if !reflect.DeepEqual(cleaned, expected) {
		t.Fatalf("codexEnvironment() = %#v, want %#v", cleaned, expected)
	}
}

func TestScanEvents(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"final answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":7,"reasoning_output_tokens":2}}`,
	}, "\n")

	state, err := scanEvents(strings.NewReader(input))
	if err != nil {
		t.Fatalf("scanEvents() error = %v", err)
	}
	if state.sessionID != "019abcde-1234-7000-8000-0123456789ab" {
		t.Fatalf("session id = %q", state.sessionID)
	}
	if state.message != "final answer" {
		t.Fatalf("message = %q", state.message)
	}
	if state.usage.InputTokens != 10 || state.usage.OutputTokens != 7 {
		t.Fatalf("usage = %#v", state.usage)
	}
}

func TestScanEventsFailure(t *testing.T) {
	input := `{"type":"turn.failed","error":{"message":"model unavailable"}}`
	state, err := scanEvents(strings.NewReader(input))
	if err != nil {
		t.Fatalf("scanEvents() error = %v", err)
	}
	if state.failure != "model unavailable" {
		t.Fatalf("failure = %q", state.failure)
	}
}

func TestScanEventsCompletesAfterRecoverableError(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}`,
		`{"type":"error","message":"Reconnecting... 1/5 (stream disconnected before completion)"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"final answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":7,"reasoning_output_tokens":2}}`,
	}, "\n")

	state, err := scanEvents(strings.NewReader(input))
	if err != nil {
		t.Fatalf("scanEvents() error = %v", err)
	}
	if state.failure != "" {
		t.Fatalf("failure = %q, want no failure after turn.completed", state.failure)
	}
	if !state.completed {
		t.Fatal("completed = false, want true")
	}
	if state.message != "final answer" {
		t.Fatalf("message = %q", state.message)
	}
}

func TestScanEventsErrorWithoutCompletionFails(t *testing.T) {
	input := `{"type":"error","message":"stream disconnected before completion"}`
	state, err := scanEvents(strings.NewReader(input))
	if err != nil {
		t.Fatalf("scanEvents() error = %v", err)
	}
	if state.failure != "stream disconnected before completion" {
		t.Fatalf("failure = %q", state.failure)
	}
	if state.completed {
		t.Fatal("completed = true, want false")
	}
}

func TestRunnerExecute(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := filepath.Join(tempDir, "codex")
	script := `#!/bin/sh
cat >received-message
printf '%s\n' 'codex diagnostic' >&2
printf '%s\n' \
  '{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}' \
  '{"type":"error","message":"Reconnecting... 1/5 (stream disconnected before completion)"}' \
  '{"type":"item.completed","item":{"type":"agent_message","text":"done"}}' \
  '{"type":"turn.completed","usage":{"input_tokens":3,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}'
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	var logs bytes.Buffer
	runner, err := NewRunner(RunnerConfig{
		Binary:  fakeCodex,
		WorkDir: tempDir,
		Sandbox: "read-only",
		Timeout: time.Second,
		Logger:  log.New(&logs, "", 0),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	message := "应用：kraken\n时间：2026-07-28 17:29:10 CST\n内容：panic stack"
	result, err := runner.Execute(context.Background(), Request{Message: message})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.SessionID != "019abcde-1234-7000-8000-0123456789ab" {
		t.Fatalf("session id = %q", result.SessionID)
	}
	if result.Message != "done" {
		t.Fatalf("message = %q", result.Message)
	}
	if result.Usage.InputTokens != 3 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	received, err := os.ReadFile(filepath.Join(tempDir, "received-message"))
	if err != nil {
		t.Fatalf("read received message: %v", err)
	}
	if string(received) != message {
		t.Fatalf("Codex stdin = %q, want %q", received, message)
	}
	if !strings.Contains(logs.String(), `[codex-cli stdout pid=`) ||
		!strings.Contains(logs.String(), `"type":"thread.started"`) {
		t.Fatalf("logs do not contain Codex stdout events:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `[codex-cli stderr pid=`) ||
		!strings.Contains(logs.String(), "codex diagnostic") {
		t.Fatalf("logs do not contain Codex stderr:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `[codex-input pid=`) {
		t.Fatalf("logs do not contain the Codex input:\n%s", logs.String())
	}
	for _, line := range strings.Split(message, "\n") {
		if !strings.Contains(logs.String(), line) {
			t.Fatalf("logs do not contain input line %q:\n%s", line, logs.String())
		}
	}
}

func TestRunnerAutomaticallyResumesCyberPolicyFailure(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := filepath.Join(tempDir, "codex")
	script := `#!/bin/sh
case " $* " in
  *" exec resume "*)
    cat >received-recovery-message
    printf '%s\n' \
      '{"type":"item.completed","item":{"type":"agent_message","text":"recovered report"}}' \
      '{"type":"turn.completed","usage":{"input_tokens":8,"cached_input_tokens":6,"output_tokens":2,"reasoning_output_tokens":0}}'
    ;;
  *)
    cat >received-initial-message
    printf '%s\n' \
      '{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}' \
      '{"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request."}}'
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	var logs bytes.Buffer
	runner, err := NewRunner(RunnerConfig{
		Binary:  fakeCodex,
		WorkDir: tempDir,
		Sandbox: "read-only",
		Timeout: time.Second,
		Logger:  log.New(&logs, "", 0),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	result, err := runner.Execute(context.Background(), Request{Message: "review company project"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.SessionID != "019abcde-1234-7000-8000-0123456789ab" || result.Message != "recovered report" {
		t.Fatalf("Execute() result = %#v", result)
	}
	if result.Usage.InputTokens != 8 || result.Usage.OutputTokens != 2 {
		t.Fatalf("Execute() usage = %#v", result.Usage)
	}
	recoveryMessage, err := os.ReadFile(filepath.Join(tempDir, "received-recovery-message"))
	if err != nil {
		t.Fatalf("read recovery message: %v", err)
	}
	for _, expected := range []string{
		"公司自有且已明确授权的项目",
		"具体、可验证的复现步骤",
		"本地单元测试、集成测试或隔离测试环境",
	} {
		if !strings.Contains(string(recoveryMessage), expected) {
			t.Fatalf("recovery message does not contain %q:\n%s", expected, recoveryMessage)
		}
	}
	if !strings.Contains(logs.String(), "auto-resuming attempt=1/1") ||
		!strings.Contains(logs.String(), `resume_session_id="019abcde-1234-7000-8000-0123456789ab"`) {
		t.Fatalf("logs do not describe policy recovery:\n%s", logs.String())
	}
}

func TestRunnerStopsAfterOneCyberPolicyRecovery(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := filepath.Join(tempDir, "codex")
	script := `#!/bin/sh
count=0
if [ -f invocation-count ]; then
  count=$(cat invocation-count)
fi
count=$((count + 1))
printf '%s' "$count" >invocation-count
cat >/dev/null
printf '%s\n' \
  '{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}' \
  '{"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}'
exit 1
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	runner, err := NewRunner(RunnerConfig{
		Binary:  fakeCodex,
		WorkDir: tempDir,
		Sandbox: "read-only",
		Timeout: time.Second,
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	result, err := runner.Execute(context.Background(), Request{Message: "review"})
	if !errors.Is(err, ErrCyberPolicyBlocked) {
		t.Fatalf("Execute() error = %v, want ErrCyberPolicyBlocked", err)
	}
	if result == nil || result.SessionID != "019abcde-1234-7000-8000-0123456789ab" {
		t.Fatalf("Execute() result = %#v, want preserved session", result)
	}
	count, err := os.ReadFile(filepath.Join(tempDir, "invocation-count"))
	if err != nil {
		t.Fatalf("read invocation count: %v", err)
	}
	if string(count) != "2" {
		t.Fatalf("Codex invocation count = %s, want 2", count)
	}
}

func TestRunnerDoesNotRecoverUnrelatedTurnFailure(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := filepath.Join(tempDir, "codex")
	script := `#!/bin/sh
printf x >>invocations
cat >/dev/null
printf '%s\n' \
  '{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}' \
  '{"type":"turn.failed","error":{"message":"model unavailable"}}'
exit 1
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	runner, err := NewRunner(RunnerConfig{
		Binary:  fakeCodex,
		WorkDir: tempDir,
		Sandbox: "read-only",
		Timeout: time.Second,
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	result, err := runner.Execute(context.Background(), Request{Message: "review"})
	if err == nil || errors.Is(err, ErrCyberPolicyBlocked) || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == nil || result.SessionID == "" {
		t.Fatalf("Execute() result = %#v, want preserved session", result)
	}
	invocations, err := os.ReadFile(filepath.Join(tempDir, "invocations"))
	if err != nil {
		t.Fatalf("read invocations: %v", err)
	}
	if string(invocations) != "x" {
		t.Fatalf("Codex invocations = %q, want one", invocations)
	}
}

func TestRunnerTimeoutPreservesStartedSession(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := filepath.Join(tempDir, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"019abcde-1234-7000-8000-0123456789ab"}'
sleep 5
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	runner, err := NewRunner(RunnerConfig{
		Binary:  fakeCodex,
		WorkDir: tempDir,
		Sandbox: "read-only",
		Timeout: 100 * time.Millisecond,
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	result, err := runner.Execute(context.Background(), Request{Message: "continue"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want deadline exceeded", err)
	}
	if result == nil || result.SessionID != "019abcde-1234-7000-8000-0123456789ab" {
		t.Fatalf("Execute() result = %#v, want preserved session", result)
	}
}

func TestRunnerRejectsProgressWithoutCompletedTurn(t *testing.T) {
	for _, priorTurn := range []bool{false, true} {
		t.Run(fmt.Sprint("prior_completed_turn=", priorTurn), func(t *testing.T) {
			dir := t.TempDir()
			fakeCodex := filepath.Join(dir, "codex")
			events := []string{`{"type":"thread.started","thread_id":"thread-incomplete"}`}
			if priorTurn {
				events = append(events,
					`{"type":"item.completed","item":{"type":"agent_message","text":"earlier answer"}}`,
					`{"type":"turn.completed"}`,
				)
			}
			events = append(events,
				`{"type":"turn.started"}`,
				`{"type":"item.completed","item":{"type":"agent_message","text":"still reviewing the code"}}`,
			)
			script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" + strings.Join(events, "' '") + "'\nexit 0\n"
			if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			runner, err := NewRunner(RunnerConfig{
				Binary: fakeCodex, WorkDir: dir, Sandbox: "read-only",
				Timeout: time.Second, Logger: log.New(io.Discard, "", 0),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Execute(context.Background(), Request{Message: "review"})
			if !errors.Is(err, ErrIncompleteTurn) {
				t.Fatalf("error = %v, want incomplete turn", err)
			}
			if result == nil || result.SessionID != "thread-incomplete" || result.Message != "" {
				t.Fatalf("result = %#v; preserve only the session, not a progress message", result)
			}
		})
	}
}

func TestNewRunnerRejectsNetworkAccessOutsideWorkspaceWrite(t *testing.T) {
	_, err := NewRunner(RunnerConfig{
		Binary:        os.Args[0],
		WorkDir:       t.TempDir(),
		Sandbox:       "read-only",
		Timeout:       time.Second,
		NetworkAccess: true,
	})
	if err == nil || !strings.Contains(err.Error(), "network access requires") {
		t.Fatalf("NewRunner() error = %v, want network access validation error", err)
	}
}

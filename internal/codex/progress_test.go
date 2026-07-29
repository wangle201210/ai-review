package codex

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestProgressLoggerDescribesCodexEvents(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 1/5"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"先检查目录\n再定位问题"}}`,
		`{"type":"item.started","item":{"id":"item-2","type":"command_execution","command":"pwd","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"command_execution","command":"pwd","aggregated_output":"/srv/project\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item-3","type":"file_change","changes":[{"path":"main.go","kind":"update"}],"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"item-4","type":"mcp_tool_call","server":"gitlab","tool":"create_merge_request","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item-5","type":"web_search","query":"Codex JSONL events"}}`,
		`{"type":"item.completed","item":{"id":"item-6","type":"plan_update","items":[{"text":"定位问题","status":"completed"},{"text":"验证修复","status":"in_progress"}]}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":7,"reasoning_output_tokens":2}}`,
	}, "\n")

	var output bytes.Buffer
	runner := &Runner{logger: log.New(&output, "", 0)}
	_, err := scanEventsWithObservers(
		strings.NewReader(input),
		nil,
		func(event streamEvent) {
			runner.logProgressEvent(123, event)
		},
	)
	if err != nil {
		t.Fatalf("scanEventsWithObservers() error = %v", err)
	}

	logs := output.String()
	expected := []string{
		`[codex-progress pid=123] session started: thread-1`,
		`[codex-progress pid=123] turn started`,
		`[codex-progress pid=123] event error: Reconnecting... 1/5`,
		`[codex-progress pid=123] assistant: 先检查目录`,
		`[codex-progress pid=123] assistant: 再定位问题`,
		`[codex-progress pid=123] command started: pwd`,
		`[codex-progress pid=123] command completed: status=completed exit_code=0: pwd`,
		`[codex-progress pid=123] command output: /srv/project`,
		`[codex-progress pid=123] file change completed: main.go (update)`,
		`[codex-progress pid=123] MCP tool started: gitlab/create_merge_request`,
		`[codex-progress pid=123] web search completed: Codex JSONL events`,
		`[codex-progress pid=123] plan completed: 定位问题 (completed) | 验证修复 (in_progress)`,
		`[codex-progress pid=123] turn completed: input_tokens=10 cached_input_tokens=4 output_tokens=7 reasoning_output_tokens=2`,
	}
	for _, value := range expected {
		if !strings.Contains(logs, value) {
			t.Errorf("progress logs missing %q:\n%s", value, logs)
		}
	}
}

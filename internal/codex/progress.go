package codex

import (
	"fmt"
	"strings"
)

func (r *Runner) logInput(pid int, sessionID, message string) {
	prefix := fmt.Sprintf(
		"[codex-input pid=%d resume_session_id=%q] ",
		pid,
		sessionID,
	)
	r.logMultiline(prefix, message)
}

func (r *Runner) logProgressEvent(pid int, event streamEvent) {
	switch event.Type {
	case "thread.started":
		r.logProgress(pid, "session started: ", event.ThreadID)
	case "turn.started":
		r.logProgress(pid, "", "turn started")
	case "turn.completed":
		if event.Usage == nil {
			r.logProgress(pid, "", "turn completed")
			return
		}
		r.logProgress(pid, "", fmt.Sprintf(
			"turn completed: input_tokens=%d cached_input_tokens=%d output_tokens=%d reasoning_output_tokens=%d",
			event.Usage.InputTokens,
			event.Usage.CachedInputTokens,
			event.Usage.OutputTokens,
			event.Usage.ReasoningOutputTokens,
		))
	case "turn.failed":
		detail := eventFailure(event)
		r.logProgress(pid, "turn failed: ", detail)
	case "error":
		r.logProgress(pid, "event error: ", eventFailure(event))
	case "item.started", "item.updated", "item.completed":
		r.logProgressItem(pid, strings.TrimPrefix(event.Type, "item."), event.Item)
	default:
		r.logProgress(pid, "event: ", event.Type)
	}
}

func (r *Runner) logProgressItem(pid int, phase string, item streamItem) {
	switch item.Type {
	case "agent_message":
		if phase == "completed" && item.Text != "" {
			r.logProgress(pid, "assistant: ", item.Text)
		}
	case "reasoning":
		if item.Text != "" {
			r.logProgress(pid, "reasoning: ", item.Text)
			return
		}
		r.logProgress(pid, "", "reasoning "+phase)
	case "command_execution":
		r.logCommandProgress(pid, phase, item)
	case "file_change":
		r.logFileChangeProgress(pid, phase, item)
	case "mcp_tool_call":
		tool := firstNonEmpty(item.Tool, item.Name, "unknown")
		if item.Server != "" {
			tool = item.Server + "/" + tool
		}
		r.logProgress(pid, "", fmt.Sprintf("MCP tool %s: %s", phase, tool))
	case "web_search":
		detail := item.Query
		if detail == "" {
			detail = "query unavailable"
		}
		r.logProgress(pid, "web search "+phase+": ", detail)
	case "plan_update", "todo_list":
		r.logPlanProgress(pid, phase, item)
	default:
		itemType := item.Type
		if itemType == "" {
			itemType = "unknown"
		}
		r.logProgress(pid, "", fmt.Sprintf("item %s: type=%s", phase, itemType))
	}
}

func (r *Runner) logCommandProgress(pid int, phase string, item streamItem) {
	command := item.Command
	if command == "" {
		command = "command unavailable"
	}

	if phase != "completed" {
		r.logProgress(pid, "command "+phase+": ", command)
		return
	}

	detail := "command completed"
	if item.Status != "" {
		detail += ": status=" + item.Status
	}
	if item.ExitCode != nil {
		detail += fmt.Sprintf(" exit_code=%d", *item.ExitCode)
	}
	r.logProgress(pid, detail+": ", command)

	if item.AggregatedOutput != "" {
		r.logProgress(pid, "command output: ", item.AggregatedOutput)
	}
}

func (r *Runner) logFileChangeProgress(pid int, phase string, item streamItem) {
	if len(item.Changes) == 0 {
		r.logProgress(pid, "", "file change "+phase)
		return
	}

	changes := make([]string, 0, len(item.Changes))
	for _, change := range item.Changes {
		description := change.Path
		if change.Kind != "" {
			description += " (" + change.Kind + ")"
		}
		changes = append(changes, description)
	}
	r.logProgress(pid, "file change "+phase+": ", strings.Join(changes, ", "))
}

func (r *Runner) logPlanProgress(pid int, phase string, item streamItem) {
	plan := item.Items
	if len(plan) == 0 {
		plan = item.Plan
	}
	if len(plan) == 0 {
		r.logProgress(pid, "", "plan "+phase)
		return
	}

	steps := make([]string, 0, len(plan))
	for _, entry := range plan {
		text := firstNonEmpty(entry.Text, entry.Step, "unnamed step")
		status := entry.Status
		if status == "" && entry.Completed {
			status = "completed"
		}
		if status != "" {
			text += " (" + status + ")"
		}
		steps = append(steps, text)
	}
	r.logProgress(pid, "plan "+phase+": ", strings.Join(steps, " | "))
}

func (r *Runner) logProgress(pid int, label, value string) {
	prefix := fmt.Sprintf("[codex-progress pid=%d] %s", pid, label)
	r.logMultiline(prefix, value)
}

func (r *Runner) logMultiline(prefix, value string) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		if index == len(lines)-1 && line == "" {
			continue
		}
		r.logger.Print(prefix + line)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

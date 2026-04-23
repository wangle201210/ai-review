package prompt

import (
	"embed"
	"strings"
)

//go:embed prompts/*
var promptFS embed.FS

type Service struct {
	systemSummary string
}

func NewService() (*Service, error) {
	s := &Service{}
	if data, err := promptFS.ReadFile("prompts/system_summary.md"); err == nil {
		s.systemSummary = string(data)
	}
	return s, nil
}

func (s *Service) BuildSummaryPrompt(diffText string) (system, user string) {
	system = s.systemSummary
	user = "Summarize the following code changes:\n\n" + diffText
	return
}

func StripMarkdownCodeBlock(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + 7
		if end := strings.Index(text[start:], "```"); end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + 3
		for start < len(text) && text[start] == '\n' {
			start++
		}
		if end := strings.Index(text[start:], "```"); end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	return text
}

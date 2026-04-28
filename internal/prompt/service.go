package prompt

import (
	"embed"
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

package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func confDir() string {
	if d := os.Getenv("AI_REVIEW_CONF_DIR"); d != "" {
		return d
	}
	return "./conf"
}

func BuildPrompt(diffText string) string {
	data, err := os.ReadFile(filepath.Join(confDir(), "deep_review_prompt.md"))
	if err != nil {
		return "Review the following code changes and post inline comments:\n\n" + diffText
	}
	return strings.ReplaceAll(string(data), "{{DIFF_TEXT}}", diffText)
}

// BuildPromptWithSkills builds the review prompt with skill instructions embedded directly.
func BuildPromptWithSkills(diffText, provider string) string {
	prompt := BuildPrompt(diffText)

	skillContent := loadSkillInstructions(provider)
	prompt = strings.ReplaceAll(prompt, "{{SKILL_INSTRUCTIONS}}", skillContent)

	return prompt
}

func loadSkillInstructions(provider string) string {
	var skillPath string
	switch strings.ToUpper(provider) {
	case "GITHUB":
		skillPath = filepath.Join(confDir(), "skills", "github-inline-review", "SKILL.md")
	default:
		skillPath = filepath.Join(confDir(), "skills", "gitlab-inline-review", "SKILL.md")
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		return fmt.Sprintf("Error loading skill: %v", err)
	}

	content := string(data)
	// Strip YAML frontmatter
	if idx := strings.Index(content, "---"); idx == 0 {
		if end := strings.Index(content[3:], "---"); end != -1 {
			content = strings.TrimSpace(content[3+end+3:])
		}
	}

	return content
}

// GetSkillDir returns the skill directory path for the given provider.
func GetSkillDir(provider string) string {
	switch strings.ToUpper(provider) {
	case "GITHUB":
		return filepath.Join(confDir(), "skills", "github-inline-review")
	default:
		return filepath.Join(confDir(), "skills", "gitlab-inline-review")
	}
}

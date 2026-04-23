package claude

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

//go:embed conf/deep_review_prompt.md
var promptFS embed.FS

//go:embed conf/skills
var skillsFS embed.FS

func SkillsFS() fs.FS {
	sub, _ := fs.Sub(skillsFS, "conf/skills")
	return sub
}

func BuildPrompt(diffText string) string {
	data, err := promptFS.ReadFile("conf/deep_review_prompt.md")
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
		skillPath = "conf/skills/github-inline-review/SKILL.md"
	default:
		skillPath = "conf/skills/gitlab-inline-review/SKILL.md"
	}

	data, err := skillsFS.ReadFile(skillPath)
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

// WriteSkillFilesToDir writes embedded skill files to a temp directory.
// Returns (skillDir, cleanupDir, error). cleanupDir is the parent temp dir to remove when done.
func WriteSkillFilesToDir(provider string) (skillDir string, cleanupDir string, err error) {
	tmpDir, err := os.MkdirTemp("", "ai-review-skills-")
	if err != nil {
		return "", "", err
	}

	if err := WriteSkillFiles(SkillsFS(), tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", err
	}

	switch strings.ToUpper(provider) {
	case "GITHUB":
		return tmpDir + "/github-inline-review", tmpDir, nil
	default:
		return tmpDir + "/gitlab-inline-review", tmpDir, nil
	}
}

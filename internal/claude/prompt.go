package claude

import (
	"embed"
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

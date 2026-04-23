package diff

import (
	"fmt"
	"regexp"
	"strings"
)

type LineType int

const (
	LineAdded LineType = iota
	LineRemoved
	LineContext
)

type DiffLine struct {
	Type    LineType
	NewLine int    // line number in the new file (0 for removed lines)
	OldLine int    // line number in the old file (0 for added lines)
	Content string
}

type DiffHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []DiffLine
}

type DiffFile struct {
	NewName string
	OldName string
	Hunks   []DiffHunk
}

type Diff struct {
	Files []DiffFile
}

var (
	hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	fileHeaderRe = regexp.MustCompile(`^diff --git a/(.*?) b/(.*?)$`)
	oldFileRe    = regexp.MustCompile(`^--- (?:a/)?(.*)$`)
	newFileRe    = regexp.MustCompile(`^\+\+\+ (?:b/)?(.*)$`)
)

func Parse(text string) (*Diff, error) {
	result := &Diff{}
	lines := strings.Split(text, "\n")

	var currentFile *DiffFile
	var currentHunk *DiffHunk
	var oldLine, newLine int

	for _, line := range lines {
		// New file
		if matches := fileHeaderRe.FindStringSubmatch(line); matches != nil {
			if currentFile != nil && currentHunk != nil {
				currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
				currentHunk = nil
			}
			if currentFile != nil {
				result.Files = append(result.Files, *currentFile)
			}
			currentFile = &DiffFile{
				OldName: matches[1],
				NewName: matches[2],
			}
			continue
		}

		// Old/new file names (more reliable than diff --git for renames)
		if currentFile != nil {
			if matches := oldFileRe.FindStringSubmatch(line); matches != nil {
				currentFile.OldName = matches[1]
				continue
			}
			if matches := newFileRe.FindStringSubmatch(line); matches != nil {
				currentFile.NewName = matches[1]
				continue
			}
		}

		// Hunk header
		if matches := hunkHeaderRe.FindStringSubmatch(line); matches != nil {
			if currentFile == nil {
				continue
			}
			if currentHunk != nil {
				currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
			}
			os := atoi(matches[1])
			oc := atoi(matches[2])
			if oc == 0 {
				oc = 1
			}
			ns := atoi(matches[3])
			nc := atoi(matches[4])
			if nc == 0 {
				nc = 1
			}
			currentHunk = &DiffHunk{
				OldStart: os,
				OldCount: oc,
				NewStart: ns,
				NewCount: nc,
			}
			oldLine = os
			newLine = ns
			continue
		}

		// Diff content
		if currentHunk == nil {
			continue
		}

		if strings.HasPrefix(line, "+") {
			currentHunk.Lines = append(currentHunk.Lines, DiffLine{
				Type:    LineAdded,
				NewLine: newLine,
				Content: line[1:],
			})
			newLine++
		} else if strings.HasPrefix(line, "-") {
			currentHunk.Lines = append(currentHunk.Lines, DiffLine{
				Type:    LineRemoved,
				OldLine: oldLine,
				Content: line[1:],
			})
			oldLine++
		} else if strings.HasPrefix(line, " ") {
			currentHunk.Lines = append(currentHunk.Lines, DiffLine{
				Type:    LineContext,
				OldLine: oldLine,
				NewLine: newLine,
				Content: line[1:],
			})
			oldLine++
			newLine++
		}
		// Skip \ No newline at end of file
	}

	if currentHunk != nil && currentFile != nil {
		currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
	}
	if currentFile != nil {
		result.Files = append(result.Files, *currentFile)
	}

	return result, nil
}

// AddedLines returns all added lines with their new file line numbers for a given file.
func (d *Diff) AddedLines(filePath string) []DiffLine {
	var result []DiffLine
	for _, f := range d.Files {
		if f.NewName != filePath {
			continue
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.Type == LineAdded {
					result = append(result, l)
				}
			}
		}
	}
	return result
}

// RenderForPrompt renders the diff in a format suitable for LLM prompts.
func (d *Diff) RenderForPrompt() string {
	var sb strings.Builder
	for _, f := range d.Files {
		sb.WriteString("File: " + f.NewName + "\n")
		for _, h := range f.Hunks {
			sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldCount, h.NewStart, h.NewCount))
			for _, l := range h.Lines {
				switch l.Type {
				case LineAdded:
					sb.WriteString(fmt.Sprintf("+%s (line %d)\n", l.Content, l.NewLine))
				case LineRemoved:
					sb.WriteString(fmt.Sprintf("-%s\n", l.Content))
				case LineContext:
					sb.WriteString(fmt.Sprintf(" %s\n", l.Content))
				}
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

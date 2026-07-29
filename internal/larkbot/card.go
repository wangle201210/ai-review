package larkbot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
)

func parseLarkMessageContent(messageType, content string) (string, error) {
	if messageType == "interactive" {
		return parseInteractiveCard(content)
	}

	parsed, _ := normalize.ParseContent(messageType, content)
	if strings.TrimSpace(parsed) == "" {
		return "", errors.New("Lark message content is empty")
	}
	return parsed, nil
}

func parseInteractiveCard(content string) (string, error) {
	var card any
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		return "", fmt.Errorf("parse interactive card wrapper: %w", err)
	}

	unwrapped, err := unwrapInteractiveCard(card)
	if err != nil {
		return "", err
	}

	var output cardTextBuffer
	writeCardRoot(&output, unwrapped)
	if text := normalizeCardText(output.String()); text != "" {
		return text, nil
	}

	raw, err := json.Marshal(unwrapped)
	if err != nil {
		return "", fmt.Errorf("encode unrecognized interactive card: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("interactive card has no readable content")
	}
	return "[interactive card JSON]\n" + string(raw), nil
}

func unwrapInteractiveCard(card any) (any, error) {
	for depth := 0; depth < 4; depth++ {
		object, ok := card.(map[string]any)
		if !ok {
			return card, nil
		}

		rawCard, exists := object["json_card"]
		if !exists {
			return card, nil
		}
		switch value := rawCard.(type) {
		case string:
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err != nil {
				return nil, fmt.Errorf("parse interactive json_card: %w", err)
			}
			card = decoded
		default:
			card = value
		}
	}
	return nil, errors.New("interactive card wrapper is nested too deeply")
}

func writeCardRoot(output *cardTextBuffer, card any) {
	object, ok := card.(map[string]any)
	if !ok {
		writeCardNode(output, card)
		return
	}

	wroteSection := false
	if header, exists := object["header"]; exists {
		writeCardNode(output, header)
		output.LineBreak()
		wroteSection = true
	}
	if body, exists := object["body"]; exists {
		writeCardNode(output, body)
		wroteSection = true
	}
	if elements, exists := object["elements"]; exists {
		writeCardNode(output, elements)
		wroteSection = true
	}
	if !wroteSection {
		writeCardNode(output, object)
	}
}

func writeCardNode(output *cardTextBuffer, node any) {
	switch value := node.(type) {
	case nil:
		return
	case string:
		output.WriteText(value)
	case []any:
		for _, child := range value {
			writeCardNode(output, child)
		}
	case map[string]any:
		tag, _ := value["tag"].(string)
		switch tag {
		case "br":
			output.LineBreak()
			return
		case "hr":
			output.ParagraphBreak()
			return
		case "img", "image":
			return
		case "a":
			writeCardLink(output, value)
			return
		}

		for _, key := range []string{
			"header",
			"title",
			"subtitle",
			"body",
			"text",
			"content",
			"elements",
			"fields",
			"columns",
			"column_set",
			"property",
		} {
			if child, exists := value[key]; exists {
				writeCardNode(output, child)
			}
		}

		switch tag {
		case "card_header", "div", "note", "button", "column":
			output.LineBreak()
		}
	}
}

func writeCardLink(output *cardTextBuffer, link map[string]any) {
	label := firstCardString(link, "text", "content")
	href := firstCardString(link, "href", "url")
	if property, ok := link["property"].(map[string]any); ok {
		if label == "" {
			label = firstCardString(property, "text", "content")
		}
		if href == "" {
			href = firstCardString(property, "href", "url")
		}
	}

	switch {
	case label != "" && href != "" && label != href:
		output.WriteText(label + " (" + href + ")")
	case label != "":
		output.WriteText(label)
	case href != "":
		output.WriteText(href)
	}
}

func firstCardString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok {
			return value
		}
	}
	return ""
}

func normalizeCardText(value string) string {
	value = strings.ReplaceAll(value, `\t`, "\t")
	lines := strings.Split(value, "\n")

	result := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			if len(result) > 0 && !blank {
				result = append(result, "")
			}
			blank = true
			continue
		}
		result = append(result, line)
		blank = false
	}
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

type cardTextBuffer struct {
	bytes.Buffer
}

func (b *cardTextBuffer) WriteText(value string) {
	if value == "" {
		return
	}
	_, _ = b.WriteString(value)
}

func (b *cardTextBuffer) LineBreak() {
	if b.Len() == 0 {
		return
	}
	data := b.Bytes()
	if data[len(data)-1] != '\n' {
		_ = b.WriteByte('\n')
	}
}

func (b *cardTextBuffer) ParagraphBreak() {
	if b.Len() == 0 {
		return
	}
	b.LineBreak()
	data := b.Bytes()
	if len(data) < 2 || data[len(data)-2] != '\n' {
		_ = b.WriteByte('\n')
	}
}

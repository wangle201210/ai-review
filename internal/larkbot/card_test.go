package larkbot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseInteractiveCardRawContent(t *testing.T) {
	rawCard := `{
		"body": {
			"elements": [
				{
					"tag": "div",
					"property": {
						"text": {
							"tag": "markdown",
							"property": {
								"elements": [
									{"tag": "plain_text", "property": {"content": "应用：gems"}},
									{"tag": "br", "property": {}},
									{"tag": "plain_text", "property": {"content": "时间：2026-07-29 11:46:02 CST"}},
									{"tag": "br", "property": {}},
									{"tag": "plain_text", "property": {"content": "集群：local-SG-eks"}},
									{"tag": "br", "property": {}},
									{"tag": "plain_text", "property": {"content": "内容"}},
									{"tag": "plain_text", "property": {"content": "：context deadline exceeded"}},
									{"tag": "br", "property": {}},
									{"tag": "plain_text", "property": {"content": "\\tstack line"}}
								]
							}
						}
					}
				},
				{"tag": "hr", "property": {}},
				{
					"tag": "note",
					"property": {
						"elements": [
							{"tag": "plain_text", "property": {"content": "INT2-Game"}}
						]
					}
				}
			]
		},
		"header": {
			"tag": "card_header",
			"property": {
				"title": {"tag": "plain_text", "property": {"content": "INT2-Game"}}
			}
		}
	}`
	wrapper, err := json.Marshal(map[string]any{
		"json_card":   rawCard,
		"card_schema": 1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got, err := parseLarkMessageContent("interactive", string(wrapper))
	if err != nil {
		t.Fatalf("parseLarkMessageContent() error = %v", err)
	}
	wantParts := []string{
		"INT2-Game\n",
		"应用：gems\n",
		"时间：2026-07-29 11:46:02 CST\n",
		"集群：local-SG-eks\n",
		"内容：context deadline exceeded\n",
		"\tstack line",
	}
	for _, want := range wantParts {
		if !strings.Contains(got, want) {
			t.Fatalf("parsed card does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[interactive card]") || strings.Contains(got, "card_schema") {
		t.Fatalf("parsed card contains transport placeholder or metadata:\n%s", got)
	}
}

func TestParseInteractiveCardClassicContent(t *testing.T) {
	content := `{
		"header": {
			"title": {"tag": "plain_text", "content": "Alert"}
		},
		"elements": [
			{
				"tag": "div",
				"text": {
					"tag": "lark_md",
					"content": "**应用：** kraken\n**内容：** panic"
				}
			}
		]
	}`

	got, err := parseLarkMessageContent("interactive", content)
	if err != nil {
		t.Fatalf("parseLarkMessageContent() error = %v", err)
	}
	if want := "Alert\n**应用：** kraken\n**内容：** panic"; got != want {
		t.Fatalf("parsed card = %q, want %q", got, want)
	}
}

func TestParseTextMessageStillUsesSDKNormalizer(t *testing.T) {
	got, err := parseLarkMessageContent("text", `{"text":"plain alert"}`)
	if err != nil {
		t.Fatalf("parseLarkMessageContent() error = %v", err)
	}
	if got != "plain alert" {
		t.Fatalf("parsed text = %q", got)
	}
}

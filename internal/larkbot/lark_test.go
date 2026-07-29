package larkbot

import (
	"testing"

	channeltypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestIncomingFromLarkExtractsThreadAndRemovesBotMention(t *testing.T) {
	parentID := "om_parent"
	rootID := "om_root"
	raw := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				ParentId: &parentID,
				RootId:   &rootID,
			},
		},
	}
	message := &channeltypes.NormalizedMessage{
		MessageID: "om_message",
		ChatID:    "oc_chat",
		ChatType:  "group",
		Content:   "@_user_1 请查明原因",
		Mentions: []channeltypes.Mention{
			{Key: "@_user_1", IsBot: true},
		},
		RawEvent: raw,
	}

	got, err := incomingFromLark(message)
	if err != nil {
		t.Fatalf("incomingFromLark() error = %v", err)
	}
	if got.ParentID != parentID || got.RootID != rootID || got.Text != "请查明原因" {
		t.Fatalf("incomingFromLark() = %#v", got)
	}
}

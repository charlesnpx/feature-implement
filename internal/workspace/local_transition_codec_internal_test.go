package workspace

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTransitionCodecsRejectReceiptFields(t *testing.T) {
	payload := json.RawMessage(`{"receipt_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	for _, test := range []struct {
		name   string
		decode func() error
	}{
		{
			name: "acknowledgement",
			decode: func() error {
				_, _, err := decodeAttemptJournalEvent(
					JournalEventOrchestrationAck,
					payload,
				)
				return err
			},
		},
		{
			name: "owner response",
			decode: func() error {
				_, _, err := decodeAttemptJournalEvent(
					JournalEventOwnerResponse,
					payload,
				)
				return err
			},
		},
		{
			name: "review result",
			decode: func() error {
				_, _, err := decodeReviewJournalEvent(
					JournalEventReviewResultRecorded,
					payload,
				)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.decode()
			if err == nil ||
				!strings.Contains(err.Error(), `unknown field "receipt_digest"`) {
				t.Fatalf("receipt-bearing payload error = %v", err)
			}
		})
	}
}

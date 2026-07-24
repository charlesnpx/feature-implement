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

func TestReviewIsolationCodecContainsNoProviderBrokerField(t *testing.T) {
	proof := StrictReviewIsolationProof()
	result, err := NewReviewResultSubmission(ReviewResultSubmissionOptions{
		RequestDigest:    DigestBytes([]byte("local-review-request")),
		ReviewerInstance: MustID("local-reviewer"),
		Status:           ReviewResultCompleted,
		Isolation:        proof,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(reviewResultToWire(result))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "provider_broker") {
		t.Fatalf("local review wire retains provider field: %s", encoded)
	}
	var isolation reviewIsolationPayloadWire
	err = decodeStrictJSON([]byte(`{
		"repository_read_only": true,
		"scratch_ephemeral": true,
		"credentials_available": false,
		"repository_hooks": false,
		"write_network": false,
		"provider_broker": false,
		"external_write": false,
		"digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}`), &isolation)
	if err == nil || !strings.Contains(err.Error(), `unknown field "provider_broker"`) {
		t.Fatalf("provider-bearing review isolation error = %v", err)
	}
}

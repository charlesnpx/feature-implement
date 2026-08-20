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
			name: "boundary",
			decode: func() error {
				_, _, err := decodeAttemptJournalEvent(
					JournalEventAttemptBoundary,
					payload,
				)
				return err
			},
		},
		{
			name: "resume",
			decode: func() error {
				_, _, err := decodeAttemptJournalEvent(
					JournalEventAttemptResumed,
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

func TestAttemptCodecsRejectRemovedRemoteBindings(t *testing.T) {
	for _, test := range []struct {
		name      string
		eventType JournalEventType
		payload   json.RawMessage
		field     string
	}{
		{
			name:      "boundary authorization identity",
			eventType: JournalEventAttemptBoundary,
			payload:   json.RawMessage(`{"authorization_id":"removed"}`),
			field:     "authorization_id",
		},
		{
			name:      "resume authorization identity",
			eventType: JournalEventAttemptResumed,
			payload:   json.RawMessage(`{"authorization_id":"removed"}`),
			field:     "authorization_id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, supported, err := decodeAttemptJournalEvent(
				test.eventType,
				test.payload,
			)
			if !supported || event != nil || err == nil ||
				!strings.Contains(
					err.Error(),
					`unknown field "`+test.field+`"`,
				) {
				t.Fatalf(
					"removed binding %q decoded as %#v, supported=%t, err=%v",
					test.field, event, supported, err,
				)
			}
		})
	}
}

func TestRemovedProviderJournalEventsAreUnsupported(t *testing.T) {
	for _, eventType := range []JournalEventType{
		"attempt.reserved.v2",
		"attempt.materialization_intended.v2",
		"attempt.started.v2",
		"authorization.grant_recorded.v2",
		"provider.intent_reserved.v2",
		"provider.completion_verified.v2",
	} {
		event, err := decodeWorkspaceJournalEvent(eventType, json.RawMessage(`{}`))
		if err == nil || event != nil ||
			!strings.Contains(err.Error(), "unsupported journal event type") {
			t.Fatalf("removed event %q decoded as %#v with %v", eventType, event, err)
		}
	}
}

func TestReviewIsolationCodecRejectsRemovedFields(t *testing.T) {
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
	for _, field := range []string{"credentials_available", "provider_broker"} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("local review wire retains %q: %s", field, encoded)
		}
	}
	for _, field := range []string{"credentials_available", "provider_broker"} {
		var isolation reviewIsolationPayloadWire
		payload := `{
			"repository_read_only": true,
			"scratch_ephemeral": true,
			"repository_hooks": false,
			"write_network": false,
			"` + field + `": false,
			"external_write": false,
			"digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}`
		err = decodeStrictJSON([]byte(payload), &isolation)
		if err == nil ||
			!strings.Contains(err.Error(), `unknown field "`+field+`"`) {
			t.Fatalf("review isolation accepted removed field %q: %v", field, err)
		}
	}
}

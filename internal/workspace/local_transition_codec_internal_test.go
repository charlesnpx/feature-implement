package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReviewGateCodecsRejectUnknownFields(t *testing.T) {
	payload := json.RawMessage(`{"unexpected":"value"}`)
	for _, eventType := range []JournalEventType{
		JournalEventReviewGateDispatched,
		JournalEventReviewGateRecorded,
	} {
		event, supported, err := decodeReviewJournalEvent(eventType, payload)
		if !supported || event != nil || err == nil || !strings.Contains(err.Error(), `unknown field "unexpected"`) {
			t.Fatalf("%s unknown-field result = event=%#v supported=%t err=%v", eventType, event, supported, err)
		}
	}
}

func TestReviewGateRecordedCodecRetainsExactTerminalBindings(t *testing.T) {
	head, _ := ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	tree, _ := ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	dispatch, err := NewReviewGateDispatch(ReviewGateDispatchOptions{
		WorkspaceID: MustID("codec-workspace"), Generation: DigestBytes([]byte("codec-generation")),
		AttemptID: MustID("codec-attempt"), MergeUnit: MergeUnitReference{planID: MustID("codec-plan"), mergeUnitID: MustID("codec-unit")},
		Adapter: MustID("natural-language"), Recipe: MustID("default"), PolicyDigest: DigestBytes([]byte("codec-policy")),
		Head: head, Tree: tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: ReviewGateNotSatisfied, EvidenceDigest: DigestBytes([]byte("codec-evidence")),
		OccurredAt: time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewReviewGateRecordedJournalEvent(dispatch, record)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalReviewJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf("marshal gate record = %s supported=%t error=%v", payload, supported, err)
	}
	decoded, supported, err := decodeReviewJournalEvent(JournalEventReviewGateRecorded, payload)
	if err != nil || !supported {
		t.Fatalf("decode gate record = %#v supported=%t error=%v", decoded, supported, err)
	}
	decodedEvent, ok := decoded.(ReviewGateRecordedJournalEvent)
	if !ok || decodedEvent.Dispatch() != dispatch || decodedEvent.Record() != record {
		t.Fatalf("gate codec lost bindings: %#v", decoded)
	}
}

func TestReviewGateRecordedDocumentCodecKeepsOnlyEvidenceLocator(t *testing.T) {
	head, _ := ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	tree, _ := ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	dispatch, err := NewReviewGateDispatch(ReviewGateDispatchOptions{
		WorkspaceID: MustID("codec-workspace"), Generation: DigestBytes([]byte("codec-generation")),
		AttemptID: MustID("codec-attempt"), MergeUnit: MergeUnitReference{planID: MustID("codec-plan"), mergeUnitID: MustID("codec-unit")},
		Adapter: MustID("witness"), Recipe: MustID("default"), PolicyDigest: DigestBytes([]byte("codec-policy")),
		Head: head, Tree: tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := NewReviewDocumentArtifact([]byte(`{"schema_version":"review-report-v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: ReviewGateSatisfied, EvidenceDigest: artifact.RawDocumentDigest(),
		OccurredAt: time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewReviewGateRecordedDocumentJournalEvent(dispatch, record, artifact)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalReviewJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf("marshal document gate record = %s supported=%t error=%v", payload, supported, err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(wire["document"], &document); err != nil {
		t.Fatal(err)
	}
	if len(document) != 1 || document["path"] == nil {
		t.Fatalf("document payload = %s, want only its evidence path", wire["document"])
	}
	decoded, supported, err := decodeReviewJournalEvent(JournalEventReviewGateRecorded, payload)
	if err != nil || !supported {
		t.Fatalf("decode document gate record = %#v supported=%t error=%v", decoded, supported, err)
	}
	decodedEvent, ok := decoded.(ReviewGateRecordedJournalEvent)
	if !ok {
		t.Fatalf("decoded document gate record = %#v", decoded)
	}
	decodedArtifact, exists := decodedEvent.DocumentArtifact()
	if !exists || decodedArtifact.RawDocumentDigest() != record.EvidenceDigest() || decodedArtifact.Path() != artifact.Path() {
		t.Fatalf("decoded document evidence locator = %#v exists=%t", decodedArtifact, exists)
	}

	var legacy map[string]any
	if err := json.Unmarshal(payload, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["document"].(map[string]any)["review_input_digest"] = DigestBytes([]byte("legacy-binding")).String()
	legacyPayload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, supported, err := decodeReviewJournalEvent(JournalEventReviewGateRecorded, legacyPayload); !supported || err == nil ||
		!strings.Contains(err.Error(), `unknown field "review_input_digest"`) {
		t.Fatalf("legacy document metadata decode = supported=%t error=%v", supported, err)
	}
}

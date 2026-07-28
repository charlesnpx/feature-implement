package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceCompletionJournalCodecResourcesAndProjection(t *testing.T) {
	workspaceID := MustID("completion-workspace")
	generation := DigestBytes([]byte("completion-generation"))
	featureHead, err := ParseGitObjectID(
		"sha1:" + strings.Repeat("a", 40),
	)
	if err != nil {
		t.Fatal(err)
	}
	reportDigest := DigestBytes([]byte("canonical-local-report"))
	event, err := NewWorkspaceCompletedJournalEvent(
		workspaceID,
		generation,
		"refs/heads/feature/completion-workspace",
		featureHead,
		reportDigest,
	)
	if err != nil {
		t.Fatal(err)
	}

	payload, supported, err := marshalCompletionJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf(
			"marshal completion supported=%t error=%v",
			supported, err,
		)
	}
	decoded, supported, err := decodeCompletionJournalEvent(
		JournalEventWorkspaceCompleted, payload,
	)
	if err != nil || !supported {
		t.Fatalf(
			"decode completion supported=%t error=%v",
			supported, err,
		)
	}
	replayed, ok := decoded.(WorkspaceCompletedJournalEvent)
	if !ok || replayed.workspaceID != workspaceID ||
		replayed.generation != generation ||
		replayed.featureRef != event.featureRef ||
		replayed.featureHead != featureHead ||
		replayed.reportDigest != reportDigest {
		t.Fatalf("decoded completion = %#v", decoded)
	}

	reads, writes, supported :=
		completionJournalEventResources(event)
	if !supported || len(reads) != 4 || len(writes) != 1 ||
		writes[0] != CompletionJournalResource(workspaceID) {
		t.Fatalf(
			"completion resources reads=%#v writes=%#v supported=%t",
			reads, writes, supported,
		)
	}
	if _, err := NewJournalAppend(
		event,
		time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC),
		nil,
		nil,
	); err == nil ||
		!strings.Contains(
			err.Error(), "complete local verification workflow",
		) {
		t.Fatalf("nonprivileged completion append error = %v", err)
	}

	record := JournalRecord{
		sequence:   9,
		generation: generation,
		eventHash:  DigestBytes([]byte("completion-event")),
		event:      event,
	}
	current := WorkspaceRuntimeProjection{
		workspaceID:      workspaceID,
		activeGeneration: generation,
		localTarget: RuntimeLocalTargetProjection{
			binding: LocalTargetBinding{
				featureRef: event.featureRef,
				digest:     DigestBytes([]byte("target-binding")),
			},
			createdHead:   featureHead,
			createdRecord: 3,
			headRecord:    8,
		},
	}
	next := current
	if err := reduceCompletionRuntime(
		current, &next, record, event,
	); err != nil {
		t.Fatal(err)
	}
	completion, exists := next.Completion()
	if !exists || completion.FeatureRef() != event.featureRef ||
		completion.FeatureHead() != featureHead ||
		completion.ReportDigest() != reportDigest ||
		completion.Record() != record.sequence ||
		completion.EventDigest() != record.eventHash {
		t.Fatalf(
			"completion projection = %#v exists=%t",
			completion, exists,
		)
	}
	if _, err := reduceWorkspaceRuntime(
		next,
		JournalRecord{
			sequence:   record.sequence + 1,
			generation: generation,
			event:      event,
		},
	); err == nil ||
		!strings.Contains(
			err.Error(), "completion is final",
		) {
		t.Fatalf(
			"post-completion mutation error = %v", err,
		)
	}
}

func TestWorkspaceCompletionCodecRejectsNoncanonicalPayloads(t *testing.T) {
	valid := workspaceCompletedPayloadWire{
		SchemaVersion: JournalSchemaVersion,
		WorkspaceID:   "completion-workspace",
		Generation: DigestBytes(
			[]byte("completion-generation"),
		).String(),
		FeatureRef: "refs/heads/feature/completion-workspace",
		FeatureHead: "sha1:" +
			strings.Repeat("a", 40),
		ReportDigest: DigestBytes(
			[]byte("completion-report"),
		).String(),
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "unknown field",
			mutate: func(value map[string]any) {
				value["receipt"] = "not-local-state"
			},
		},
		{
			name: "unowned ref",
			mutate: func(value map[string]any) {
				value["feature_ref"] = "refs/heads/main"
			},
		},
		{
			name: "wrong schema",
			mutate: func(value map[string]any) {
				value["schema_version"] = float64(1)
			},
		},
		{
			name: "missing report digest",
			mutate: func(value map[string]any) {
				delete(value, "report_digest")
			},
		},
	}
	source, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(source, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			payload, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, supported, err := decodeCompletionJournalEvent(
				JournalEventWorkspaceCompleted, payload,
			); !supported || err == nil {
				t.Fatalf(
					"decode supported=%t error=%v payload=%s",
					supported, err, payload,
				)
			}
		})
	}
}

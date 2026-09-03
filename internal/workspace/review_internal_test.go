package workspace

import (
	"strings"
	"testing"
	"time"
)

func TestReviewGateTerminalRecordsBindAllVerdictsToOneExactArtifact(t *testing.T) {
	workspaceID := MustID("gate-workspace")
	generation := DigestBytes([]byte("gate-generation"))
	reference, err := NewMergeUnitReference(MustID("gate-plan"), MustID("gate-unit"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := NewReviewGateDispatch(ReviewGateDispatchOptions{
		WorkspaceID: workspaceID, Generation: generation, AttemptID: MustID("gate-attempt"),
		MergeUnit: reference, Adapter: MustID("natural-language"), Recipe: MustID("default"),
		PolicyDigest: DigestBytes([]byte("policy")), Head: head, Tree: tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, verdict := range []ReviewGateVerdict{
		ReviewGateSatisfied, ReviewGateNotSatisfied, ReviewGateFailedToRun,
	} {
		record, recordErr := NewReviewGateRecord(ReviewGateRecordOptions{
			Dispatch: dispatch, Verdict: verdict, EvidenceDigest: DigestBytes([]byte(string(verdict))),
			OccurredAt: time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC),
		})
		if recordErr != nil {
			t.Fatalf("record %s: %v", verdict, recordErr)
		}
		if record.Head() != head || record.Tree() != tree || record.Verdict() != verdict || record.EvidenceDigest().IsZero() {
			t.Fatalf("record %s lost an exact binding: %#v", verdict, record)
		}
	}
	if _, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: ReviewGateSatisfied,
		OccurredAt: time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC),
	}); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("missing evidence record error = %v", err)
	}
}

func TestReviewGateStateRequiresExactHeadAndTreeForSatisfaction(t *testing.T) {
	head, _ := ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	tree, _ := ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	otherHead, _ := ParseGitObjectID("sha1:" + strings.Repeat("c", 40))
	otherTree, _ := ParseGitObjectID("sha1:" + strings.Repeat("d", 40))
	policy := DigestBytes([]byte("policy"))
	dispatch, err := NewReviewGateDispatch(ReviewGateDispatchOptions{
		WorkspaceID: MustID("state-workspace"), Generation: DigestBytes([]byte("state-generation")),
		AttemptID: MustID("state-attempt"), MergeUnit: MergeUnitReference{planID: MustID("state-plan"), mergeUnitID: MustID("state-unit")},
		Adapter: MustID("natural-language"), Recipe: MustID("default"), PolicyDigest: policy, Head: head, Tree: tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: ReviewGateSatisfied, EvidenceDigest: DigestBytes([]byte("gate evidence")),
		OccurredAt: time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ReviewGateState{dispatches: []ReviewGateDispatch{dispatch}, records: []ReviewGateRecord{record}}
	config := ReviewGateConfig{adapter: dispatch.adapter, recipe: dispatch.recipe, policyDigest: policy, policy: []byte("policy")}
	if _, ok := state.Satisfied(config, head, tree); !ok {
		t.Fatal("exact gate record did not satisfy the state")
	}
	for _, artifact := range [][2]GitObjectID{{otherHead, tree}, {head, otherTree}} {
		if _, ok := state.Satisfied(config, artifact[0], artifact[1]); ok {
			t.Fatalf("mismatched artifact %s/%s satisfied the gate", artifact[0], artifact[1])
		}
	}
}

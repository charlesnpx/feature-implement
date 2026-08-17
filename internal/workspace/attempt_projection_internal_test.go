package workspace

import (
	"strings"
	"testing"
)

func TestAttemptProjectionRecomputesOrchestrationAcknowledgementRequestDigest(t *testing.T) {
	workspaceID := MustID("projection-workspace")
	generation := DigestBytes([]byte("projection-generation"))
	attemptID := MustID("projection-attempt")
	boundaryID := MustID("projection-boundary")
	goal, err := NewGoalBinding(MustID("projection-goal"), GoalScopeMergeUnit)
	if err != nil {
		t.Fatal(err)
	}
	head, err := ParseGitObjectID("sha1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	boundary := RuntimeBoundaryProjection{
		boundaryID: boundaryID, ordinal: 1, record: 2,
		checkpoint: AttemptCheckpointCompleteGoalAndWait, goal: goal, head: head,
		directiveDigest: DigestBytes([]byte("projection-directive")),
		idempotencyKey:  DigestBytes([]byte("projection-idempotency")),
	}
	current := WorkspaceRuntimeProjection{
		workspaceID: workspaceID, activeGeneration: generation,
		attempts: []RuntimeAttemptProjection{{
			attemptID: attemptID, generation: generation, phase: AttemptPaused,
			goal: goal, boundaries: []RuntimeBoundaryProjection{boundary},
		}},
	}
	validRequest, err := deriveOrchestrationAcknowledgementRequestDigest(
		workspaceID, generation, attemptID, boundary,
		AcknowledgementGoalCompleted, goal, boundary.idempotencyKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := NewAttemptOrchestrationAcknowledgedJournalEvent(
		workspaceID, attemptID, boundaryID, generation, AcknowledgementGoalCompleted,
		boundary.directiveDigest, goal, boundary.idempotencyKey, validRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.attempts = cloneRuntimeAttempts(current.attempts)
	if err := reduceAttemptRuntime(current, &next, JournalRecord{
		sequence: 3, generation: generation, event: valid,
	}); err != nil {
		t.Fatalf("valid acknowledgement replay: %v", err)
	}

	tampered, err := NewAttemptOrchestrationAcknowledgedJournalEvent(
		workspaceID, attemptID, boundaryID, generation, AcknowledgementGoalCompleted,
		boundary.directiveDigest, goal, boundary.idempotencyKey,
		DigestBytes([]byte("stored-but-wrong-request")),
	)
	if err != nil {
		t.Fatal(err)
	}
	tamperedNext := current
	tamperedNext.attempts = cloneRuntimeAttempts(current.attempts)
	if err := reduceAttemptRuntime(current, &tamperedNext, JournalRecord{
		sequence: 3, generation: generation, event: tampered,
	}); err == nil || !strings.Contains(err.Error(), "invalid request digest") {
		t.Fatalf("tampered acknowledgement replay error = %v", err)
	}
}

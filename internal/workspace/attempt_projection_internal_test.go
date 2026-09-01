package workspace

import (
	"strings"
	"testing"
)

func TestAttemptProjectionRejectsBoundaryKindsDisallowedByReservedPolicy(t *testing.T) {
	workspaceID := MustID("boundary-policy-workspace")
	generation := DigestBytes([]byte("boundary-policy-generation"))
	attemptID := MustID("boundary-policy-attempt")
	leaseID := MustID("boundary-policy-lease")
	goal, err := NewGoalBinding(MustID("boundary-policy-goal"), GoalScopeMergeUnit)
	if err != nil {
		t.Fatal(err)
	}
	head, err := ParseGitObjectID("sha1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	item, err := NewEvidenceItem(MustID("boundary-policy-evidence-item"), "boundary-policy")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewEvidence(
		MustID("boundary-policy-evidence"), DigestBytes([]byte("boundary-policy-evidence")), []EvidenceItem{item},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		checkpoint AttemptCheckpointMode
		escalation AttemptEscalationPolicy
		kind       AttemptBoundaryKind
		shape      AttemptCheckpointMode
		want       string
	}{
		{
			name: "checkpoint none", checkpoint: AttemptCheckpointNone,
			escalation: AttemptEscalationAllowed, kind: AttemptBoundaryKindCheckpoint,
			shape: AttemptCheckpointPauseOnly, want: "checkpoint policy",
		},
		{
			name: "forbidden escalation", checkpoint: AttemptCheckpointPauseOnly,
			escalation: AttemptEscalationForbidden, kind: AttemptBoundaryKindEscalation,
			shape: AttemptCheckpointPauseOnly, want: "escalation policy",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, err := NewAttemptBoundaryReachedJournalEvent(
				workspaceID, attemptID, generation, 1, test.kind, test.shape,
				ID{}, leaseID, goal, head, []Evidence{evidence},
			)
			if err != nil {
				t.Fatal(err)
			}
			current := WorkspaceRuntimeProjection{
				workspaceID: workspaceID, activeGeneration: generation,
				attempts: []RuntimeAttemptProjection{{
					attemptID: attemptID, generation: generation, phase: AttemptActive,
					checkpoint: test.checkpoint, escalation: test.escalation,
					leaseID: leaseID, goal: goal,
				}},
			}
			next := current
			next.attempts = cloneRuntimeAttempts(current.attempts)
			if err := reduceAttemptRuntime(current, &next, JournalRecord{
				sequence: 1, generation: generation, event: event,
			}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("disallowed replay error = %v", err)
			}
		})
	}
}

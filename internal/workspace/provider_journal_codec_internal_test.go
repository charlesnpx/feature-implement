package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProviderReservationCodecRoundTripsFirstCheckpointAndRejectsTampering(t *testing.T) {
	workspaceID := MustID("codec-workspace")
	generation := DigestBytes([]byte("codec-generation"))
	repository, _ := NewRepositoryIdentity("https://github.com/example/provider-codec.git")
	base, _ := ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	head, _ := ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	frontier, _ := NewAuthorizationFrontier(base, head)
	mergeUnit, _ := NewMergeUnitReference(MustID("codec-plan"), MustID("codec-unit"))
	intent, err := NewProviderPushIntent(ProviderPushIntentOptions{
		Scope: ProviderIntentScopeOptions{
			WorkspaceID: workspaceID, Generation: generation, AttemptID: MustID("codec-attempt"),
			MergeUnit: mergeUnit, Repository: repository, Remote: "origin",
			SerialSegment: MustID("codec-segment"), Frontier: frontier, Epoch: 1,
		},
		Branch: "mu/provider-codec-a1-0123456789ab", ExpectRemoteAbsent: true, Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := AuthorizationSnapshotBinding{
		journalHead: DigestBytes([]byte("codec-journal-head")), authorizationRevision: 7,
	}
	planning := AuthorizationCapability{
		grantID: DigestBytes([]byte("codec-grant")), requestDigest: intent.authorization.digest,
		stateDigest: DigestBytes([]byte("codec-state")), snapshot: snapshot,
		checkpoint: AuthorizationAtPlanning, epoch: 1,
		evaluatedAt: time.Date(2026, 7, 21, 14, 0, 0, 0, time.UTC),
		expiresAt:   time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC),
	}
	planning.digest = capabilityDigest(planning)
	reservation := planning
	reservation.checkpoint = AuthorizationAtIntentReservation
	reservation.priorDigest = planning.digest
	reservation.digest = capabilityDigest(reservation)
	event, err := NewProviderIntentReservedJournalEvent(
		workspaceID, generation, intent, planning, reservation,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalProviderJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf("marshal provider reservation: supported=%v err=%v", supported, err)
	}
	decoded, supported, err := decodeProviderJournalEvent(JournalEventProviderIntentReserved, payload)
	if err != nil || !supported {
		t.Fatalf("decode provider reservation: supported=%v err=%v", supported, err)
	}
	roundTrip := decoded.(ProviderIntentReservedJournalEvent)
	if roundTrip.intent.digest != intent.digest || roundTrip.planning.priorDigest != (Digest{}) ||
		roundTrip.reservation.priorDigest != planning.digest {
		t.Fatalf("provider reservation codec changed authorization chain: %#v", roundTrip)
	}

	var unknown map[string]any
	if err := json.Unmarshal(payload, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["unexpected"] = true
	tampered, _ := json.Marshal(unknown)
	if _, _, err := decodeProviderJournalEvent(JournalEventProviderIntentReserved, tampered); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("provider codec accepted unknown field: %v", err)
	}

	var changed map[string]any
	_ = json.Unmarshal(payload, &changed)
	intentWire := changed["intent"].(map[string]any)
	intentWire["digest"] = DigestBytes([]byte("different-intent")).String()
	tampered, _ = json.Marshal(changed)
	if _, _, err := decodeProviderJournalEvent(JournalEventProviderIntentReserved, tampered); err == nil ||
		!strings.Contains(err.Error(), "canonical identity mismatch") {
		t.Fatalf("provider codec accepted tampered intent digest: %v", err)
	}
}

func TestProviderPreflightAndAbandonmentCodecsRoundTripCanonicalEvidence(t *testing.T) {
	workspaceID := MustID("codec-workspace")
	generation := DigestBytes([]byte("codec-generation"))
	repository, _ := NewRepositoryIdentity("https://github.com/example/provider-codec.git")
	base, _ := ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	head, _ := ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	tree, _ := ParseGitObjectID("sha1:" + strings.Repeat("c", 40))
	frontier, _ := NewAuthorizationFrontier(base, head)
	mergeUnit, _ := NewMergeUnitReference(MustID("codec-plan"), MustID("codec-unit"))
	pullRequest, _ := newPullRequestIdentity(MustID("github"), repository, 71)
	intent, err := NewProviderMergeIntent(ProviderMergeIntentOptions{
		Scope: ProviderIntentScopeOptions{
			WorkspaceID: workspaceID, Generation: generation, AttemptID: MustID("codec-attempt"),
			MergeUnit: mergeUnit, Repository: repository, Remote: "origin",
			SerialSegment: MustID("codec-segment"), Frontier: frontier, PullRequest: pullRequest, Epoch: 1,
		},
		Branch: "mu/provider-codec-a1-0123456789ab", BaseRef: "feature/provider-codec",
		Head: head, Tree: tree, Strategy: ProviderMergeCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	check, _ := NewProviderCheckState(MustID("ci"), true, ProviderCheckPassed, DigestBytes([]byte("ci-evidence")))
	review, _ := NewProviderReviewState(
		MustID("owners"), true, ProviderReviewApproved, DigestBytes([]byte("review-evidence")),
	)
	state, err := NewProviderPullRequestState(ProviderPullRequestStateOptions{
		Repository: repository, PullRequest: pullRequest, BaseRef: intent.baseRef, Branch: intent.branch,
		Head: head, HeadTree: tree, RemoteBranchHead: head, BaseHeadBeforeMerge: base,
		Checks: []ProviderCheckState{check}, Reviews: []ProviderReviewState{review}, RequestMarker: "codec-preflight",
	})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := newProviderMergePreflight(intent, state)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewProviderMergePreflightRecordedJournalEvent(workspaceID, generation, preflight)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalProviderJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf("marshal provider preflight: supported=%v err=%v", supported, err)
	}
	decoded, supported, err := decodeProviderJournalEvent(JournalEventProviderMergePreflightRecorded, payload)
	if err != nil || !supported {
		t.Fatalf("decode provider preflight: supported=%v err=%v", supported, err)
	}
	roundTrip := decoded.(ProviderMergePreflightRecordedJournalEvent)
	if roundTrip.preflight.digest != preflight.digest ||
		len(roundTrip.preflight.requiredChecks) != 1 || len(roundTrip.preflight.requiredReviews) != 1 {
		t.Fatalf("provider preflight codec changed evidence: %#v", roundTrip.preflight)
	}
	var wire map[string]any
	_ = json.Unmarshal(payload, &wire)
	preflightWire := wire["preflight"].(map[string]any)
	preflightWire["digest"] = DigestBytes([]byte("tampered-preflight")).String()
	tampered, _ := json.Marshal(wire)
	if _, _, err := decodeProviderJournalEvent(JournalEventProviderMergePreflightRecorded, tampered); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("provider preflight codec accepted tampered digest: %v", err)
	}
	abandoned, err := NewProviderIntentAbandonedJournalEvent(workspaceID, generation, intent)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err = marshalProviderJournalEvent(abandoned)
	if err != nil || !supported {
		t.Fatalf("marshal provider abandonment: supported=%v err=%v", supported, err)
	}
	decoded, supported, err = decodeProviderJournalEvent(JournalEventProviderIntentAbandoned, payload)
	if err != nil || !supported {
		t.Fatalf("decode provider abandonment: supported=%v err=%v", supported, err)
	}
	abandonRoundTrip := decoded.(ProviderIntentAbandonedJournalEvent)
	if abandonRoundTrip.intentID != intent.intentID || abandonRoundTrip.intentDigest != intent.digest {
		t.Fatalf("provider abandonment codec changed intent binding: %#v", abandonRoundTrip)
	}
}

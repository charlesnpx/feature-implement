package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ReviewInvocationReservation is the durable, single-executor claim made
// before an isolated reviewer is invoked. The reviewer identity is selected
// by the trusted coordinator so freshness policy also accounts for crashed or
// otherwise discarded invocations.
type ReviewInvocationReservation struct {
	request          ReviewRequest
	reviewerInstance ID
	idempotencyKey   Digest
	digest           Digest
}

func NewReviewInvocationReservation(
	request ReviewRequest,
	reviewerInstance ID,
	idempotencyKey Digest,
) (ReviewInvocationReservation, error) {
	if request.digest.IsZero() || reviewerInstance.IsZero() || idempotencyKey.IsZero() {
		return ReviewInvocationReservation{}, fmt.Errorf("review invocation reservation requires request, reviewer, and idempotency key")
	}
	reservation := ReviewInvocationReservation{
		request: request, reviewerInstance: reviewerInstance, idempotencyKey: idempotencyKey,
	}
	canonical, err := canonicalReviewInvocationReservation(reservation)
	if err != nil {
		return ReviewInvocationReservation{}, err
	}
	reservation.digest = DigestBytes(canonical)
	return reservation, nil
}

func (reservation ReviewInvocationReservation) Request() ReviewRequest { return reservation.request }
func (reservation ReviewInvocationReservation) ReviewerInstance() ID {
	return reservation.reviewerInstance
}
func (reservation ReviewInvocationReservation) IdempotencyKey() Digest {
	return reservation.idempotencyKey
}
func (reservation ReviewInvocationReservation) Digest() Digest { return reservation.digest }

func canonicalReviewInvocationReservation(reservation ReviewInvocationReservation) ([]byte, error) {
	if reservation.request.digest.IsZero() || reservation.reviewerInstance.IsZero() ||
		reservation.idempotencyKey.IsZero() {
		return nil, fmt.Errorf("review invocation reservation is incomplete")
	}
	type reservationJSON struct {
		SchemaVersion  int    `json:"schema_version"`
		Request        string `json:"request_digest"`
		Reviewer       string `json:"reviewer_instance"`
		IdempotencyKey string `json:"idempotency_key"`
		WorkspaceID    string `json:"workspace_id"`
		Generation     string `json:"generation"`
		AttemptID      string `json:"attempt_id"`
		Round          uint16 `json:"round"`
		ProfileOrdinal uint16 `json:"profile_ordinal"`
		Invocation     uint16 `json:"invocation"`
	}
	request := reservation.request
	return json.Marshal(reservationJSON{
		SchemaVersion: 2, Request: request.digest.String(), Reviewer: reservation.reviewerInstance.String(),
		IdempotencyKey: reservation.idempotencyKey.String(), WorkspaceID: request.workspaceID.String(),
		Generation: request.generation.String(), AttemptID: request.attemptID.String(), Round: request.round,
		ProfileOrdinal: request.profileOrdinal, Invocation: request.invocation,
	})
}

type ReviewInvocationFailure struct {
	reservationDigest Digest
	failureDigest     Digest
}

func NewReviewInvocationFailure(
	reservationDigest, failureDigest Digest,
) (ReviewInvocationFailure, error) {
	if reservationDigest.IsZero() || failureDigest.IsZero() {
		return ReviewInvocationFailure{}, fmt.Errorf("review invocation failure requires reservation and failure digests")
	}
	return ReviewInvocationFailure{
		reservationDigest: reservationDigest, failureDigest: failureDigest,
	}, nil
}

func (failure ReviewInvocationFailure) ReservationDigest() Digest {
	return failure.reservationDigest
}
func (failure ReviewInvocationFailure) FailureDigest() Digest { return failure.failureDigest }

// ReviewFixReservation durably binds the exact accepted findings and reviewed
// parent before the implementing-agent commit protocol can mutate Git.
type ReviewFixReservation struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	loopDigest  Digest
	ordinal     uint16
	round       uint16
	head        GitObjectID
	tree        GitObjectID
	findings    []Digest
	digest      Digest
}

func NewReviewFixReservation(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	loopDigest Digest,
	ordinal, round uint16,
	head, tree GitObjectID,
	findings []Digest,
) (ReviewFixReservation, error) {
	normalized, err := normalizeReviewFindingIDs(findings)
	if err != nil {
		return ReviewFixReservation{}, err
	}
	if workspaceID.IsZero() || generation.IsZero() || attemptID.IsZero() || loopDigest.IsZero() ||
		ordinal == 0 || round == 0 || head.IsZero() || tree.IsZero() || head.Algorithm() != tree.Algorithm() {
		return ReviewFixReservation{}, fmt.Errorf("review fix reservation requires exact workspace, loop, ordinal, round, Git, and findings")
	}
	reservation := ReviewFixReservation{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, loopDigest: loopDigest,
		ordinal: ordinal, round: round, head: head, tree: tree, findings: normalized,
	}
	canonical, err := canonicalReviewFixReservation(reservation)
	if err != nil {
		return ReviewFixReservation{}, err
	}
	reservation.digest = DigestBytes(canonical)
	return reservation, nil
}

func (reservation ReviewFixReservation) WorkspaceID() ID    { return reservation.workspaceID }
func (reservation ReviewFixReservation) Generation() Digest { return reservation.generation }
func (reservation ReviewFixReservation) AttemptID() ID      { return reservation.attemptID }
func (reservation ReviewFixReservation) LoopDigest() Digest { return reservation.loopDigest }
func (reservation ReviewFixReservation) Ordinal() uint16    { return reservation.ordinal }
func (reservation ReviewFixReservation) Round() uint16      { return reservation.round }
func (reservation ReviewFixReservation) Head() GitObjectID  { return reservation.head }
func (reservation ReviewFixReservation) Tree() GitObjectID  { return reservation.tree }
func (reservation ReviewFixReservation) FindingIDs() []Digest {
	return append([]Digest(nil), reservation.findings...)
}
func (reservation ReviewFixReservation) Digest() Digest { return reservation.digest }

func canonicalReviewFixReservation(reservation ReviewFixReservation) ([]byte, error) {
	if reservation.workspaceID.IsZero() || reservation.generation.IsZero() || reservation.attemptID.IsZero() ||
		reservation.loopDigest.IsZero() || reservation.ordinal == 0 || reservation.round == 0 ||
		reservation.head.IsZero() || reservation.tree.IsZero() || len(reservation.findings) == 0 {
		return nil, fmt.Errorf("review fix reservation is incomplete")
	}
	type reservationJSON struct {
		SchemaVersion int      `json:"schema_version"`
		WorkspaceID   string   `json:"workspace_id"`
		Generation    string   `json:"generation"`
		AttemptID     string   `json:"attempt_id"`
		Loop          string   `json:"loop_digest"`
		Ordinal       uint16   `json:"ordinal"`
		Round         uint16   `json:"round"`
		Head          string   `json:"head"`
		Tree          string   `json:"tree"`
		Findings      []string `json:"finding_ids"`
	}
	findingIDs := make([]string, 0, len(reservation.findings))
	for _, finding := range reservation.findings {
		findingIDs = append(findingIDs, finding.String())
	}
	return json.Marshal(reservationJSON{
		SchemaVersion: 2, WorkspaceID: reservation.workspaceID.String(),
		Generation: reservation.generation.String(), AttemptID: reservation.attemptID.String(),
		Loop: reservation.loopDigest.String(), Ordinal: reservation.ordinal, Round: reservation.round,
		Head: reservation.head.String(), Tree: reservation.tree.String(), Findings: findingIDs,
	})
}

func normalizeReviewFindingIDs(findings []Digest) ([]Digest, error) {
	if len(findings) == 0 || len(findings) > maxReviewFindings*maxReviewProfiles {
		return nil, fmt.Errorf("review fix finding identities must be nonempty and bounded")
	}
	normalized := append([]Digest(nil), findings...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].String() < normalized[j].String() })
	for index, finding := range normalized {
		if finding.IsZero() || index > 0 && finding == normalized[index-1] {
			return nil, fmt.Errorf("review fix finding identities must be nonzero and unique")
		}
	}
	return normalized, nil
}

func cloneReviewInvocationReservations(values []ReviewInvocationReservation) []ReviewInvocationReservation {
	return append([]ReviewInvocationReservation(nil), values...)
}

func cloneReviewInvocationFailures(values []ReviewInvocationFailure) []ReviewInvocationFailure {
	return append([]ReviewInvocationFailure(nil), values...)
}

func cloneReviewFixReservation(value *ReviewFixReservation) *ReviewFixReservation {
	if value == nil {
		return nil
	}
	result := *value
	result.findings = append([]Digest(nil), value.findings...)
	return &result
}

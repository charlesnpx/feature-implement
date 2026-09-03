package workspace

import (
	"encoding/json"
	"fmt"
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

func cloneReviewInvocationReservations(values []ReviewInvocationReservation) []ReviewInvocationReservation {
	return append([]ReviewInvocationReservation(nil), values...)
}

func cloneReviewInvocationFailures(values []ReviewInvocationFailure) []ReviewInvocationFailure {
	return append([]ReviewInvocationFailure(nil), values...)
}

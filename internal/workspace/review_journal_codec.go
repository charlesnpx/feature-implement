package workspace

import (
	"encoding/json"
	"fmt"
)

type reviewProfilePayloadWire struct {
	ID             string               `json:"id"`
	Runner         string               `json:"runner"`
	ReviewerPolicy ReviewReviewerPolicy `json:"reviewer_policy"`
}

type reviewLoopPayloadWire struct {
	Profiles                 []reviewProfilePayloadWire `json:"profiles"`
	MaxReviewRounds          uint16                     `json:"max_review_rounds"`
	MaxReviewFixes           uint16                     `json:"max_review_fixes"`
	MaxInfrastructureRetries uint16                     `json:"max_infrastructure_retries"`
	Digest                   string                     `json:"digest"`
}

type reviewFindingPayloadWire struct {
	ID       string         `json:"id"`
	Severity ReviewSeverity `json:"severity"`
	Category string         `json:"category"`
	Path     string         `json:"path,omitempty"`
	Line     uint32         `json:"line,omitempty"`
	Summary  string         `json:"summary"`
	Evidence string         `json:"evidence_digest"`
}

type reviewIsolationPayloadWire struct {
	RepositoryReadOnly   bool   `json:"repository_read_only"`
	ScratchEphemeral     bool   `json:"scratch_ephemeral"`
	CredentialsAvailable bool   `json:"credentials_available"`
	RepositoryHooks      bool   `json:"repository_hooks"`
	WriteNetwork         bool   `json:"write_network"`
	ProviderBroker       bool   `json:"provider_broker"`
	ExternalWrite        bool   `json:"external_write"`
	Digest               string `json:"digest"`
}

type reviewResultPayloadWire struct {
	RequestDigest         string                     `json:"request_digest"`
	ReviewerInstance      string                     `json:"reviewer_instance"`
	Status                ReviewResultStatus         `json:"status"`
	Findings              []reviewFindingPayloadWire `json:"findings"`
	InfrastructureFailure string                     `json:"infrastructure_failure_digest,omitempty"`
	Isolation             reviewIsolationPayloadWire `json:"isolation"`
	Digest                string                     `json:"digest"`
}

type reviewRequestPayloadWire struct {
	WorkspaceID     string               `json:"workspace_id"`
	Generation      string               `json:"generation"`
	AttemptID       string               `json:"attempt_id"`
	PlanID          string               `json:"plan_id"`
	MergeUnitID     string               `json:"merge_unit_id"`
	LoopDigest      string               `json:"loop_digest"`
	Round           uint16               `json:"round"`
	ProfileID       string               `json:"profile_id"`
	Runner          string               `json:"runner"`
	ReviewerPolicy  ReviewReviewerPolicy `json:"reviewer_policy"`
	ProfileOrdinal  uint16               `json:"profile_ordinal"`
	Invocation      uint16               `json:"invocation"`
	Head            string               `json:"head"`
	Tree            string               `json:"tree"`
	IsolationDigest string               `json:"isolation_digest"`
	Digest          string               `json:"digest"`
}

type reviewRoundStartedPayloadWire struct {
	WorkspaceID string                `json:"workspace_id"`
	Generation  string                `json:"generation"`
	AttemptID   string                `json:"attempt_id"`
	PlanID      string                `json:"plan_id"`
	MergeUnitID string                `json:"merge_unit_id"`
	Loop        reviewLoopPayloadWire `json:"loop"`
	Ordinal     uint16                `json:"ordinal"`
	Head        string                `json:"head"`
	Tree        string                `json:"tree"`
}

type reviewHeadAdoptedPayloadWire struct {
	WorkspaceID    string `json:"workspace_id"`
	Generation     string `json:"generation"`
	AttemptID      string `json:"attempt_id"`
	PlanID         string `json:"plan_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	PriorHead      string `json:"prior_head"`
	Head           string `json:"head"`
	Tree           string `json:"tree"`
	SnapshotDigest string `json:"snapshot_digest"`
}

type reviewResultRecordedPayloadWire struct {
	WorkspaceID    string                  `json:"workspace_id"`
	Generation     string                  `json:"generation"`
	AttemptID      string                  `json:"attempt_id"`
	LoopDigest     string                  `json:"loop_digest"`
	Round          uint16                  `json:"round"`
	ProfileOrdinal uint16                  `json:"profile_ordinal"`
	Invocation     uint16                  `json:"invocation"`
	Reservation    string                  `json:"reservation_digest"`
	Result         reviewResultPayloadWire `json:"result"`
}

type reviewInvocationReservedPayloadWire struct {
	WorkspaceID       string                   `json:"workspace_id"`
	Generation        string                   `json:"generation"`
	AttemptID         string                   `json:"attempt_id"`
	LoopDigest        string                   `json:"loop_digest"`
	Request           reviewRequestPayloadWire `json:"request"`
	ReviewerInstance  string                   `json:"reviewer_instance"`
	IdempotencyKey    string                   `json:"idempotency_key"`
	ReservationDigest string                   `json:"reservation_digest"`
}

type reviewInvocationFailedPayloadWire struct {
	WorkspaceID       string `json:"workspace_id"`
	Generation        string `json:"generation"`
	AttemptID         string `json:"attempt_id"`
	LoopDigest        string `json:"loop_digest"`
	ReservationDigest string `json:"reservation_digest"`
	FailureDigest     string `json:"failure_digest"`
}

type reviewFindingFixReservedPayloadWire struct {
	WorkspaceID       string   `json:"workspace_id"`
	Generation        string   `json:"generation"`
	AttemptID         string   `json:"attempt_id"`
	LoopDigest        string   `json:"loop_digest"`
	Ordinal           uint16   `json:"ordinal"`
	Round             uint16   `json:"round"`
	Head              string   `json:"head"`
	Tree              string   `json:"tree"`
	Findings          []string `json:"finding_ids"`
	ReservationDigest string   `json:"reservation_digest"`
}

type reviewFixAppliedPayloadWire struct {
	WorkspaceID string   `json:"workspace_id"`
	Generation  string   `json:"generation"`
	AttemptID   string   `json:"attempt_id"`
	LoopDigest  string   `json:"loop_digest"`
	Ordinal     uint16   `json:"ordinal"`
	Reservation string   `json:"reservation_digest"`
	PriorHead   string   `json:"prior_head"`
	PriorTree   string   `json:"prior_tree"`
	Head        string   `json:"head"`
	Tree        string   `json:"tree"`
	Evidence    string   `json:"evidence_digest"`
	Findings    []string `json:"finding_ids"`
}

func marshalReviewJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		value = reviewHeadAdoptedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), PlanID: event.mergeUnit.planID.String(),
			MergeUnitID: event.mergeUnit.mergeUnitID.String(), PriorHead: event.priorHead.String(),
			Head: event.head.String(), Tree: event.tree.String(), SnapshotDigest: event.snapshotDigest.String(),
		}
	case ReviewRoundStartedJournalEvent:
		value = reviewRoundStartedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), PlanID: event.mergeUnit.planID.String(),
			MergeUnitID: event.mergeUnit.mergeUnitID.String(), Loop: reviewLoopToWire(event.loop),
			Ordinal: event.ordinal, Head: event.head.String(), Tree: event.tree.String(),
		}
	case ReviewInvocationReservedJournalEvent:
		request := event.reservation.request
		value = reviewInvocationReservedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			Request: reviewRequestToWire(request), ReviewerInstance: event.reservation.reviewerInstance.String(),
			IdempotencyKey:    event.reservation.idempotencyKey.String(),
			ReservationDigest: event.reservation.digest.String(),
		}
	case ReviewInvocationFailedJournalEvent:
		value = reviewInvocationFailedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			ReservationDigest: event.reservationDigest.String(), FailureDigest: event.failureDigest.String(),
		}
	case ReviewResultRecordedJournalEvent:
		value = reviewResultRecordedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			Round: event.round, ProfileOrdinal: event.profileOrdinal, Invocation: event.invocation,
			Reservation: event.reservationDigest.String(), Result: reviewResultToWire(event.result),
		}
	case ReviewFindingFixReservedJournalEvent:
		findings := make([]string, 0, len(event.reservation.findings))
		for _, finding := range event.reservation.findings {
			findings = append(findings, finding.String())
		}
		value = reviewFindingFixReservedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			Ordinal: event.reservation.ordinal, Round: event.reservation.round,
			Head: event.reservation.head.String(), Tree: event.reservation.tree.String(), Findings: findings,
			ReservationDigest: event.reservation.digest.String(),
		}
	case ReviewFixAppliedJournalEvent:
		findings := make([]string, 0, len(event.fix.findings))
		for _, finding := range event.fix.findings {
			findings = append(findings, finding.String())
		}
		value = reviewFixAppliedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			Ordinal: event.fix.ordinal, Reservation: event.fix.reservationDigest.String(),
			PriorHead: event.fix.priorHead.String(), PriorTree: event.fix.priorTree.String(),
			Head: event.fix.head.String(), Tree: event.fix.tree.String(), Evidence: event.fix.evidence.String(),
			Findings: findings,
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeReviewJournalEvent(
	eventType JournalEventType, payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventReviewHeadAdopted:
		var wire reviewHeadAdoptedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review head adoption: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		planID, err := NewID(wire.PlanID)
		if err != nil {
			return nil, true, err
		}
		mergeUnitID, err := NewID(wire.MergeUnitID)
		if err != nil {
			return nil, true, err
		}
		mergeUnit, err := NewMergeUnitReference(planID, mergeUnitID)
		if err != nil {
			return nil, true, err
		}
		objects := make([]GitObjectID, 0, 3)
		for _, raw := range []string{wire.PriorHead, wire.Head, wire.Tree} {
			object, err := ParseGitObjectID(raw)
			if err != nil {
				return nil, true, err
			}
			objects = append(objects, object)
		}
		snapshotDigest, err := ParseDigest(wire.SnapshotDigest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewHeadAdoptedJournalEvent(
			workspaceID, generation, attemptID, mergeUnit,
			objects[0], objects[1], objects[2], snapshotDigest,
		)
		return event, true, err
	case JournalEventReviewRoundStarted:
		var wire reviewRoundStartedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review round: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		planID, err := NewID(wire.PlanID)
		if err != nil {
			return nil, true, err
		}
		unitID, err := NewID(wire.MergeUnitID)
		if err != nil {
			return nil, true, err
		}
		mergeUnit, _ := NewMergeUnitReference(planID, unitID)
		loop, err := reviewLoopFromWire(wire.Loop)
		if err != nil {
			return nil, true, err
		}
		head, err := ParseGitObjectID(wire.Head)
		if err != nil {
			return nil, true, err
		}
		tree, err := ParseGitObjectID(wire.Tree)
		if err != nil {
			return nil, true, err
		}
		start, err := NewStartReviewRound(workspaceID, generation, attemptID, mergeUnit, loop, wire.Ordinal, head, tree)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewRoundStartedJournalEvent(start)
		return event, true, err
	case JournalEventReviewInvocationReserved:
		var wire reviewInvocationReservedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review invocation reservation: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		loopDigest, err := ParseDigest(wire.LoopDigest)
		if err != nil {
			return nil, true, err
		}
		request, err := reviewRequestFromWire(wire.Request)
		if err != nil || request.workspaceID != workspaceID || request.generation != generation ||
			request.attemptID != attemptID || request.loopDigest != loopDigest {
			return nil, true, fmt.Errorf("review invocation reservation request envelope mismatch")
		}
		reviewer, err := NewID(wire.ReviewerInstance)
		if err != nil {
			return nil, true, err
		}
		idempotencyKey, err := ParseDigest(wire.IdempotencyKey)
		if err != nil {
			return nil, true, err
		}
		reservation, err := NewReviewInvocationReservation(request, reviewer, idempotencyKey)
		if err != nil {
			return nil, true, err
		}
		stored, err := ParseDigest(wire.ReservationDigest)
		if err != nil || stored != reservation.digest {
			return nil, true, fmt.Errorf("review invocation reservation digest mismatch")
		}
		event, err := NewReviewInvocationReservedJournalEvent(workspaceID, generation, attemptID, loopDigest, reservation)
		return event, true, err
	case JournalEventReviewInvocationFailed:
		var wire reviewInvocationFailedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review invocation failure: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		loopDigest, err := ParseDigest(wire.LoopDigest)
		if err != nil {
			return nil, true, err
		}
		reservation, err := ParseDigest(wire.ReservationDigest)
		if err != nil {
			return nil, true, err
		}
		failureDigest, err := ParseDigest(wire.FailureDigest)
		if err != nil {
			return nil, true, err
		}
		failure, err := NewRecordReviewInvocationFailure(reservation, failureDigest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewInvocationFailedJournalEvent(workspaceID, generation, attemptID, loopDigest, failure)
		return event, true, err
	case JournalEventReviewResultRecorded:
		var wire reviewResultRecordedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review result: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		loopDigest, err := ParseDigest(wire.LoopDigest)
		if err != nil {
			return nil, true, err
		}
		result, err := reviewResultFromWire(wire.Result)
		if err != nil {
			return nil, true, err
		}
		reservation, err := ParseDigest(wire.Reservation)
		if err != nil {
			return nil, true, err
		}
		record, err := NewRecordReviewResult(
			wire.Round, wire.ProfileOrdinal, wire.Invocation, reservation, result,
		)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewResultRecordedJournalEvent(workspaceID, generation, attemptID, loopDigest, record)
		return event, true, err
	case JournalEventReviewFindingFixReserved:
		var wire reviewFindingFixReservedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review finding-fix reservation: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		loopDigest, err := ParseDigest(wire.LoopDigest)
		if err != nil {
			return nil, true, err
		}
		head, err := ParseGitObjectID(wire.Head)
		if err != nil {
			return nil, true, err
		}
		tree, err := ParseGitObjectID(wire.Tree)
		if err != nil {
			return nil, true, err
		}
		findings, err := parseReviewFindingDigests(wire.Findings)
		if err != nil {
			return nil, true, err
		}
		reservation, err := NewReviewFixReservation(
			workspaceID, generation, attemptID, loopDigest, wire.Ordinal, wire.Round, head, tree, findings,
		)
		if err != nil {
			return nil, true, err
		}
		stored, err := ParseDigest(wire.ReservationDigest)
		if err != nil || stored != reservation.digest {
			return nil, true, fmt.Errorf("review finding-fix reservation digest mismatch")
		}
		event, err := NewReviewFindingFixReservedJournalEvent(reservation)
		return event, true, err
	case JournalEventReviewFixApplied:
		var wire reviewFixAppliedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review fix: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		loopDigest, err := ParseDigest(wire.LoopDigest)
		if err != nil {
			return nil, true, err
		}
		objects := make([]GitObjectID, 0, 4)
		for _, raw := range []string{wire.PriorHead, wire.PriorTree, wire.Head, wire.Tree} {
			object, err := ParseGitObjectID(raw)
			if err != nil {
				return nil, true, err
			}
			objects = append(objects, object)
		}
		evidence, err := ParseDigest(wire.Evidence)
		if err != nil {
			return nil, true, err
		}
		findings, err := parseReviewFindingDigests(wire.Findings)
		if err != nil {
			return nil, true, err
		}
		reservation, err := ParseDigest(wire.Reservation)
		if err != nil {
			return nil, true, err
		}
		fix, err := NewApplyReviewFix(
			wire.Ordinal, reservation, objects[0], objects[1], objects[2], objects[3], evidence, findings,
		)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewFixAppliedJournalEvent(workspaceID, generation, attemptID, loopDigest, fix)
		return event, true, err
	default:
		return nil, false, nil
	}
}

func reviewRequestToWire(request ReviewRequest) reviewRequestPayloadWire {
	return reviewRequestPayloadWire{
		WorkspaceID: request.workspaceID.String(), Generation: request.generation.String(),
		AttemptID: request.attemptID.String(), PlanID: request.mergeUnit.planID.String(),
		MergeUnitID: request.mergeUnit.mergeUnitID.String(), LoopDigest: request.loopDigest.String(),
		Round: request.round, ProfileID: request.profile.id.String(), Runner: request.profile.runner.String(),
		ReviewerPolicy: request.profile.reviewerPolicy, ProfileOrdinal: request.profileOrdinal,
		Invocation: request.invocation, Head: request.head.String(), Tree: request.tree.String(),
		IsolationDigest: request.isolationRequired.digest.String(), Digest: request.digest.String(),
	}
}

func reviewRequestFromWire(wire reviewRequestPayloadWire) (ReviewRequest, error) {
	workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
	if err != nil {
		return ReviewRequest{}, err
	}
	planID, err := NewID(wire.PlanID)
	if err != nil {
		return ReviewRequest{}, err
	}
	mergeUnitID, err := NewID(wire.MergeUnitID)
	if err != nil {
		return ReviewRequest{}, err
	}
	mergeUnit, err := NewMergeUnitReference(planID, mergeUnitID)
	if err != nil {
		return ReviewRequest{}, err
	}
	loopDigest, err := ParseDigest(wire.LoopDigest)
	if err != nil {
		return ReviewRequest{}, err
	}
	profileID, err := NewID(wire.ProfileID)
	if err != nil {
		return ReviewRequest{}, err
	}
	runner, err := NewID(wire.Runner)
	if err != nil {
		return ReviewRequest{}, err
	}
	if !wire.ReviewerPolicy.valid() {
		return ReviewRequest{}, fmt.Errorf("review request wire reviewer policy is invalid")
	}
	head, err := ParseGitObjectID(wire.Head)
	if err != nil {
		return ReviewRequest{}, err
	}
	tree, err := ParseGitObjectID(wire.Tree)
	if err != nil {
		return ReviewRequest{}, err
	}
	request, err := newReviewRequest(
		workspaceID, generation, attemptID, mergeUnit, loopDigest, wire.Round,
		ReviewProfile{id: profileID, runner: runner, reviewerPolicy: wire.ReviewerPolicy},
		wire.ProfileOrdinal, wire.Invocation, head, tree,
	)
	if err != nil {
		return ReviewRequest{}, err
	}
	isolation, err := ParseDigest(wire.IsolationDigest)
	if err != nil || isolation != request.isolationRequired.digest {
		return ReviewRequest{}, fmt.Errorf("review request isolation digest mismatch")
	}
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != request.digest {
		return ReviewRequest{}, fmt.Errorf("review request digest mismatch")
	}
	return request, nil
}

func parseReviewFindingDigests(values []string) ([]Digest, error) {
	findings := make([]Digest, 0, len(values))
	for _, raw := range values {
		finding, err := ParseDigest(raw)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func reviewLoopToWire(loop ReviewLoop) reviewLoopPayloadWire {
	wire := reviewLoopPayloadWire{
		Profiles:        make([]reviewProfilePayloadWire, 0, len(loop.profiles)),
		MaxReviewRounds: loop.maxRounds, MaxReviewFixes: loop.maxFixes,
		MaxInfrastructureRetries: loop.maxInfrastructureRetries, Digest: loop.digest.String(),
	}
	for _, profile := range loop.profiles {
		wire.Profiles = append(wire.Profiles, reviewProfilePayloadWire{
			ID: profile.id.String(), Runner: profile.runner.String(), ReviewerPolicy: profile.reviewerPolicy,
		})
	}
	return wire
}

func reviewLoopFromWire(wire reviewLoopPayloadWire) (ReviewLoop, error) {
	if len(wire.Profiles) == 0 || len(wire.Profiles) > maxReviewProfiles {
		return ReviewLoop{}, fmt.Errorf("review loop profile count is invalid")
	}
	loop := ReviewLoop{
		profiles: make([]ReviewProfile, 0, len(wire.Profiles)), maxRounds: wire.MaxReviewRounds,
		maxFixes: wire.MaxReviewFixes, maxInfrastructureRetries: wire.MaxInfrastructureRetries,
	}
	seen := make(map[string]struct{}, len(wire.Profiles))
	for _, item := range wire.Profiles {
		id, err := NewID(item.ID)
		if err != nil {
			return ReviewLoop{}, err
		}
		runner, err := NewID(item.Runner)
		if err != nil {
			return ReviewLoop{}, err
		}
		if !item.ReviewerPolicy.valid() {
			return ReviewLoop{}, fmt.Errorf("review profile %s has unsupported reviewer policy", id)
		}
		if _, exists := seen[id.String()]; exists {
			return ReviewLoop{}, fmt.Errorf("review loop duplicates profile %s", id)
		}
		seen[id.String()] = struct{}{}
		loop.profiles = append(loop.profiles, ReviewProfile{id: id, runner: runner, reviewerPolicy: item.ReviewerPolicy})
	}
	canonical, err := canonicalReviewLoopBytes(loop)
	if err != nil {
		return ReviewLoop{}, err
	}
	loop.digest = DigestBytes(canonical)
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != loop.digest {
		return ReviewLoop{}, fmt.Errorf("review loop digest mismatch")
	}
	return loop, nil
}

func reviewResultToWire(result ReviewResultSubmission) reviewResultPayloadWire {
	findings := make([]reviewFindingPayloadWire, 0, len(result.findings))
	for _, finding := range result.findings {
		findings = append(findings, reviewFindingPayloadWire{
			ID: finding.id.String(), Severity: finding.severity, Category: finding.category.String(),
			Path: finding.path, Line: finding.line, Summary: finding.summary,
			Evidence: finding.evidenceDigest.String(),
		})
	}
	return reviewResultPayloadWire{
		RequestDigest: result.requestDigest.String(), ReviewerInstance: result.reviewerInstance.String(),
		Status: result.status, Findings: findings, InfrastructureFailure: result.infrastructureFailure.String(),
		Isolation: reviewIsolationPayloadWire{
			RepositoryReadOnly: result.isolation.repositoryReadOnly, ScratchEphemeral: result.isolation.scratchEphemeral,
			CredentialsAvailable: result.isolation.credentialsAvailable, RepositoryHooks: result.isolation.repositoryHooks,
			WriteNetwork: result.isolation.writeNetwork, ProviderBroker: result.isolation.providerBroker,
			ExternalWrite: result.isolation.externalWrite, Digest: result.isolation.digest.String(),
		},
		Digest: result.digest.String(),
	}
}

func reviewResultFromWire(wire reviewResultPayloadWire) (ReviewResultSubmission, error) {
	request, err := ParseDigest(wire.RequestDigest)
	if err != nil {
		return ReviewResultSubmission{}, err
	}
	instance, err := NewID(wire.ReviewerInstance)
	if err != nil {
		return ReviewResultSubmission{}, err
	}
	findings := make([]ReviewFinding, 0, len(wire.Findings))
	for _, item := range wire.Findings {
		category, err := NewID(item.Category)
		if err != nil {
			return ReviewResultSubmission{}, err
		}
		evidence, err := ParseDigest(item.Evidence)
		if err != nil {
			return ReviewResultSubmission{}, err
		}
		finding, err := NewReviewFinding(ReviewFindingOptions{
			Severity: item.Severity, Category: category, Path: item.Path, Line: item.Line,
			Summary: item.Summary, EvidenceDigest: evidence,
		})
		if err != nil {
			return ReviewResultSubmission{}, err
		}
		id, err := ParseDigest(item.ID)
		if err != nil || id != finding.id {
			return ReviewResultSubmission{}, fmt.Errorf("review finding wire identity mismatch")
		}
		findings = append(findings, finding)
	}
	var infrastructure Digest
	if wire.InfrastructureFailure != "" {
		infrastructure, err = ParseDigest(wire.InfrastructureFailure)
		if err != nil {
			return ReviewResultSubmission{}, err
		}
	}
	isolation := NewReviewIsolationProof(
		wire.Isolation.RepositoryReadOnly, wire.Isolation.ScratchEphemeral,
		wire.Isolation.CredentialsAvailable, wire.Isolation.RepositoryHooks,
		wire.Isolation.WriteNetwork, wire.Isolation.ProviderBroker, wire.Isolation.ExternalWrite,
	)
	isolationDigest, err := ParseDigest(wire.Isolation.Digest)
	if err != nil || isolationDigest != isolation.digest {
		return ReviewResultSubmission{}, fmt.Errorf("review isolation digest mismatch")
	}
	result, err := NewReviewResultSubmission(ReviewResultSubmissionOptions{
		RequestDigest: request, ReviewerInstance: instance, Status: wire.Status,
		Findings: findings, InfrastructureFailure: infrastructure, Isolation: isolation,
	})
	if err != nil {
		return ReviewResultSubmission{}, err
	}
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != result.digest {
		return ReviewResultSubmission{}, fmt.Errorf("review result digest mismatch")
	}
	return result, nil
}

func parseReviewEnvelope(workspaceRaw, generationRaw, attemptRaw string) (ID, Digest, ID, error) {
	workspaceID, err := NewID(workspaceRaw)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	generation, err := ParseDigest(generationRaw)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	attemptID, err := NewID(attemptRaw)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	return workspaceID, generation, attemptID, nil
}

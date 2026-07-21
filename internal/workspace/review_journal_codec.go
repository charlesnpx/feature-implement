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

type reviewResultRecordedPayloadWire struct {
	WorkspaceID    string                  `json:"workspace_id"`
	Generation     string                  `json:"generation"`
	AttemptID      string                  `json:"attempt_id"`
	LoopDigest     string                  `json:"loop_digest"`
	Round          uint16                  `json:"round"`
	ProfileOrdinal uint16                  `json:"profile_ordinal"`
	Invocation     uint16                  `json:"invocation"`
	Result         reviewResultPayloadWire `json:"result"`
	ReceiptDigest  string                  `json:"receipt_digest"`
}

type reviewFixAppliedPayloadWire struct {
	WorkspaceID string   `json:"workspace_id"`
	Generation  string   `json:"generation"`
	AttemptID   string   `json:"attempt_id"`
	LoopDigest  string   `json:"loop_digest"`
	Ordinal     uint16   `json:"ordinal"`
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
	case ReviewRoundStartedJournalEvent:
		value = reviewRoundStartedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), PlanID: event.mergeUnit.planID.String(),
			MergeUnitID: event.mergeUnit.mergeUnitID.String(), Loop: reviewLoopToWire(event.loop),
			Ordinal: event.ordinal, Head: event.head.String(), Tree: event.tree.String(),
		}
	case ReviewResultRecordedJournalEvent:
		value = reviewResultRecordedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			Round: event.round, ProfileOrdinal: event.profileOrdinal, Invocation: event.invocation,
			Result: reviewResultToWire(event.result), ReceiptDigest: event.receiptDigest.String(),
		}
	case ReviewFixAppliedJournalEvent:
		findings := make([]string, 0, len(event.fix.findings))
		for _, finding := range event.fix.findings {
			findings = append(findings, finding.String())
		}
		value = reviewFixAppliedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), LoopDigest: event.loopDigest.String(),
			Ordinal: event.fix.ordinal, PriorHead: event.fix.priorHead.String(), PriorTree: event.fix.priorTree.String(),
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
		receipt, err := ParseDigest(wire.ReceiptDigest)
		if err != nil {
			return nil, true, err
		}
		record, err := NewRecordReviewResult(wire.Round, wire.ProfileOrdinal, wire.Invocation, result, receipt)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewResultRecordedJournalEvent(workspaceID, generation, attemptID, loopDigest, record)
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
		findings := make([]Digest, 0, len(wire.Findings))
		for _, raw := range wire.Findings {
			finding, err := ParseDigest(raw)
			if err != nil {
				return nil, true, err
			}
			findings = append(findings, finding)
		}
		fix, err := NewApplyReviewFix(wire.Ordinal, objects[0], objects[1], objects[2], objects[3], evidence, findings)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewFixAppliedJournalEvent(workspaceID, generation, attemptID, loopDigest, fix)
		return event, true, err
	default:
		return nil, false, nil
	}
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

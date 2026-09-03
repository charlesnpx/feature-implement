package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const maxReviewFindings = 128

type ReviewSeverity string

const (
	ReviewSeverityCritical ReviewSeverity = "critical"
	ReviewSeverityHigh     ReviewSeverity = "high"
	ReviewSeverityMedium   ReviewSeverity = "medium"
	ReviewSeverityLow      ReviewSeverity = "low"
)

func (severity ReviewSeverity) valid() bool {
	switch severity {
	case ReviewSeverityCritical, ReviewSeverityHigh, ReviewSeverityMedium, ReviewSeverityLow:
		return true
	default:
		return false
	}
}

func (severity ReviewSeverity) Blocking() bool {
	return severity == ReviewSeverityCritical || severity == ReviewSeverityHigh
}

type ReviewFindingOptions struct {
	Severity       ReviewSeverity
	Category       ID
	Path           string
	Line           uint32
	Summary        string
	EvidenceDigest Digest
}

type ReviewFinding struct {
	id             Digest
	severity       ReviewSeverity
	category       ID
	path           string
	line           uint32
	summary        string
	evidenceDigest Digest
}

func NewReviewFinding(options ReviewFindingOptions) (ReviewFinding, error) {
	path := strings.TrimSpace(options.Path)
	if path != "" {
		normalized, err := normalizeSourcePath(path)
		if err != nil {
			return ReviewFinding{}, fmt.Errorf("review finding path: %w", err)
		}
		path = normalized
	}
	summary := strings.TrimSpace(options.Summary)
	if !options.Severity.valid() || options.Category.IsZero() || options.EvidenceDigest.IsZero() {
		return ReviewFinding{}, fmt.Errorf("review finding requires severity, category, and evidence")
	}
	if err := validateBoundedText("review finding summary", summary, 8192); err != nil {
		return ReviewFinding{}, err
	}
	if options.Line != 0 && path == "" {
		return ReviewFinding{}, fmt.Errorf("review finding line requires a repository path")
	}
	finding := ReviewFinding{
		severity: options.Severity, category: options.Category, path: path,
		line: options.Line, summary: summary, evidenceDigest: options.EvidenceDigest,
	}
	canonical, err := canonicalReviewFinding(finding)
	if err != nil {
		return ReviewFinding{}, err
	}
	finding.id = DigestBytes(canonical)
	return finding, nil
}

func (finding ReviewFinding) ID() Digest               { return finding.id }
func (finding ReviewFinding) Severity() ReviewSeverity { return finding.severity }
func (finding ReviewFinding) Category() ID             { return finding.category }
func (finding ReviewFinding) Path() string             { return finding.path }
func (finding ReviewFinding) Line() uint32             { return finding.line }
func (finding ReviewFinding) Summary() string          { return finding.summary }
func (finding ReviewFinding) EvidenceDigest() Digest   { return finding.evidenceDigest }
func (finding ReviewFinding) Blocking() bool           { return finding.severity.Blocking() }

func canonicalReviewFinding(finding ReviewFinding) ([]byte, error) {
	if !finding.severity.valid() || finding.category.IsZero() || finding.evidenceDigest.IsZero() {
		return nil, fmt.Errorf("review finding is incomplete")
	}
	type findingJSON struct {
		SchemaVersion int            `json:"schema_version"`
		Severity      ReviewSeverity `json:"severity"`
		Category      string         `json:"category"`
		Path          string         `json:"path,omitempty"`
		Line          uint32         `json:"line,omitempty"`
		Summary       string         `json:"summary"`
		Evidence      string         `json:"evidence_digest"`
	}
	return json.Marshal(findingJSON{
		SchemaVersion: 2, Severity: finding.severity, Category: finding.category.String(),
		Path: finding.path, Line: finding.line, Summary: finding.summary,
		Evidence: finding.evidenceDigest.String(),
	})
}

func normalizeReviewFindings(values []ReviewFinding) ([]ReviewFinding, error) {
	if len(values) > maxReviewFindings {
		return nil, fmt.Errorf("review result exceeds %d findings", maxReviewFindings)
	}
	result := append([]ReviewFinding(nil), values...)
	seen := make(map[string][]byte, len(result))
	for index := range result {
		canonical, err := canonicalReviewFinding(result[index])
		if err != nil {
			return nil, err
		}
		derived := DigestBytes(canonical)
		if result[index].id.IsZero() {
			result[index].id = derived
		}
		if result[index].id != derived {
			return nil, fmt.Errorf("review finding identity does not match engine-derived content")
		}
		key := result[index].id.String()
		if prior, exists := seen[key]; exists {
			if string(prior) == string(canonical) {
				return nil, fmt.Errorf("duplicate review finding %s", result[index].id)
			}
			return nil, fmt.Errorf("review finding identity collision %s", result[index].id)
		}
		seen[key] = append([]byte(nil), canonical...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id.String() < result[j].id.String() })
	return result, nil
}

type ReviewIsolationProof struct {
	repositoryReadOnly bool
	scratchEphemeral   bool
	repositoryHooks    bool
	writeNetwork       bool
	externalWrite      bool
	digest             Digest
}

func NewReviewIsolationProof(
	repositoryReadOnly, scratchEphemeral, repositoryHooks,
	writeNetwork, externalWrite bool,
) ReviewIsolationProof {
	type proofJSON struct {
		SchemaVersion      int  `json:"schema_version"`
		RepositoryReadOnly bool `json:"repository_read_only"`
		ScratchEphemeral   bool `json:"scratch_ephemeral"`
		RepositoryHooks    bool `json:"repository_hooks"`
		WriteNetwork       bool `json:"write_network"`
		ExternalWrite      bool `json:"external_write"`
	}
	canonical, _ := json.Marshal(proofJSON{
		SchemaVersion:      2,
		RepositoryReadOnly: repositoryReadOnly, ScratchEphemeral: scratchEphemeral,
		RepositoryHooks: repositoryHooks,
		WriteNetwork:    writeNetwork, ExternalWrite: externalWrite,
	})
	return ReviewIsolationProof{
		repositoryReadOnly: repositoryReadOnly, scratchEphemeral: scratchEphemeral,
		repositoryHooks: repositoryHooks,
		writeNetwork:    writeNetwork, externalWrite: externalWrite,
		digest: DigestBytes(canonical),
	}
}

func StrictReviewIsolationProof() ReviewIsolationProof {
	return NewReviewIsolationProof(true, true, false, false, false)
}

func (proof ReviewIsolationProof) RepositoryReadOnly() bool { return proof.repositoryReadOnly }
func (proof ReviewIsolationProof) ScratchEphemeral() bool   { return proof.scratchEphemeral }
func (proof ReviewIsolationProof) RepositoryHooks() bool    { return proof.repositoryHooks }
func (proof ReviewIsolationProof) WriteNetwork() bool       { return proof.writeNetwork }
func (proof ReviewIsolationProof) ExternalWrite() bool      { return proof.externalWrite }
func (proof ReviewIsolationProof) Digest() Digest           { return proof.digest }
func (proof ReviewIsolationProof) Strict() bool {
	return !proof.digest.IsZero() && proof.repositoryReadOnly && proof.scratchEphemeral &&
		!proof.repositoryHooks && !proof.writeNetwork && !proof.externalWrite
}

type ReviewResultStatus string

const (
	ReviewResultCompleted             ReviewResultStatus = "completed"
	ReviewResultInfrastructureFailure ReviewResultStatus = "infrastructure_failure"
)

func (status ReviewResultStatus) valid() bool {
	return status == ReviewResultCompleted || status == ReviewResultInfrastructureFailure
}

type ReviewResultSubmissionOptions struct {
	RequestDigest         Digest
	ReviewerInstance      ID
	Status                ReviewResultStatus
	Findings              []ReviewFinding
	InfrastructureFailure Digest
	Isolation             ReviewIsolationProof
}

type ReviewResultSubmission struct {
	requestDigest         Digest
	reviewerInstance      ID
	status                ReviewResultStatus
	findings              []ReviewFinding
	infrastructureFailure Digest
	isolation             ReviewIsolationProof
	digest                Digest
}

func NewReviewResultSubmission(options ReviewResultSubmissionOptions) (ReviewResultSubmission, error) {
	if options.RequestDigest.IsZero() || options.ReviewerInstance.IsZero() || !options.Status.valid() ||
		options.Isolation.digest.IsZero() {
		return ReviewResultSubmission{}, fmt.Errorf("review result requires request, reviewer instance, status, and isolation proof")
	}
	findings, err := normalizeReviewFindings(options.Findings)
	if err != nil {
		return ReviewResultSubmission{}, err
	}
	if options.Status == ReviewResultCompleted {
		if !options.InfrastructureFailure.IsZero() {
			return ReviewResultSubmission{}, fmt.Errorf("completed review result cannot carry infrastructure failure")
		}
	} else if len(findings) != 0 || options.InfrastructureFailure.IsZero() {
		return ReviewResultSubmission{}, fmt.Errorf("infrastructure review result requires only a failure digest")
	}
	result := ReviewResultSubmission{
		requestDigest: options.RequestDigest, reviewerInstance: options.ReviewerInstance,
		status: options.Status, findings: findings, infrastructureFailure: options.InfrastructureFailure,
		isolation: options.Isolation,
	}
	canonical, err := canonicalReviewResult(result)
	if err != nil {
		return ReviewResultSubmission{}, err
	}
	if len(canonical) > MaxJournalRecordBytes-2*reviewJournalRecordSafetyBytes {
		return ReviewResultSubmission{}, fmt.Errorf("review result exceeds the aggregate safe journal bound")
	}
	result.digest = DigestBytes(canonical)
	return result, nil
}

func (result ReviewResultSubmission) RequestDigest() Digest      { return result.requestDigest }
func (result ReviewResultSubmission) ReviewerInstance() ID       { return result.reviewerInstance }
func (result ReviewResultSubmission) Status() ReviewResultStatus { return result.status }
func (result ReviewResultSubmission) Findings() []ReviewFinding {
	return append([]ReviewFinding(nil), result.findings...)
}
func (result ReviewResultSubmission) InfrastructureFailureDigest() Digest {
	return result.infrastructureFailure
}
func (result ReviewResultSubmission) Isolation() ReviewIsolationProof { return result.isolation }
func (result ReviewResultSubmission) Digest() Digest                  { return result.digest }

func canonicalReviewResult(result ReviewResultSubmission) ([]byte, error) {
	if result.requestDigest.IsZero() || result.reviewerInstance.IsZero() || !result.status.valid() ||
		result.isolation.digest.IsZero() {
		return nil, fmt.Errorf("review result is incomplete")
	}
	type findingJSON struct {
		ID       string         `json:"id"`
		Severity ReviewSeverity `json:"severity"`
		Category string         `json:"category"`
		Path     string         `json:"path,omitempty"`
		Line     uint32         `json:"line,omitempty"`
		Summary  string         `json:"summary"`
		Evidence string         `json:"evidence_digest"`
	}
	type resultJSON struct {
		SchemaVersion         int                `json:"schema_version"`
		Request               string             `json:"request_digest"`
		ReviewerInstance      string             `json:"reviewer_instance"`
		Status                ReviewResultStatus `json:"status"`
		Findings              []findingJSON      `json:"findings"`
		InfrastructureFailure string             `json:"infrastructure_failure_digest,omitempty"`
		Isolation             string             `json:"isolation_digest"`
	}
	value := resultJSON{
		SchemaVersion: 2, Request: result.requestDigest.String(), ReviewerInstance: result.reviewerInstance.String(),
		Status: result.status, Findings: make([]findingJSON, 0, len(result.findings)),
		InfrastructureFailure: result.infrastructureFailure.String(), Isolation: result.isolation.digest.String(),
	}
	for _, finding := range result.findings {
		value.Findings = append(value.Findings, findingJSON{
			ID: finding.id.String(), Severity: finding.severity, Category: finding.category.String(),
			Path: finding.path, Line: finding.line, Summary: finding.summary,
			Evidence: finding.evidenceDigest.String(),
		})
	}
	return json.Marshal(value)
}

type ReviewRequest struct {
	workspaceID       ID
	generation        Digest
	attemptID         ID
	mergeUnit         MergeUnitReference
	loopDigest        Digest
	round             uint16
	profile           ReviewProfile
	profileOrdinal    uint16
	invocation        uint16
	head              GitObjectID
	tree              GitObjectID
	isolationRequired ReviewIsolationProof
	digest            Digest
}

func newReviewRequest(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	mergeUnit MergeUnitReference,
	loopDigest Digest,
	round uint16,
	profile ReviewProfile,
	profileOrdinal, invocation uint16,
	head, tree GitObjectID,
) (ReviewRequest, error) {
	request := ReviewRequest{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, mergeUnit: mergeUnit,
		loopDigest: loopDigest, round: round, profile: profile, profileOrdinal: profileOrdinal,
		invocation: invocation, head: head, tree: tree, isolationRequired: StrictReviewIsolationProof(),
	}
	canonical, err := canonicalReviewRequest(request)
	if err != nil {
		return ReviewRequest{}, err
	}
	request.digest = DigestBytes(canonical)
	return request, nil
}

func (request ReviewRequest) WorkspaceID() ID               { return request.workspaceID }
func (request ReviewRequest) Generation() Digest            { return request.generation }
func (request ReviewRequest) AttemptID() ID                 { return request.attemptID }
func (request ReviewRequest) MergeUnit() MergeUnitReference { return request.mergeUnit }
func (request ReviewRequest) LoopDigest() Digest            { return request.loopDigest }
func (request ReviewRequest) Round() uint16                 { return request.round }
func (request ReviewRequest) Profile() ReviewProfile        { return request.profile }
func (request ReviewRequest) ProfileOrdinal() uint16        { return request.profileOrdinal }
func (request ReviewRequest) Invocation() uint16            { return request.invocation }
func (request ReviewRequest) Head() GitObjectID             { return request.head }
func (request ReviewRequest) Tree() GitObjectID             { return request.tree }
func (request ReviewRequest) IsolationRequirements() ReviewIsolationProof {
	return request.isolationRequired
}
func (request ReviewRequest) Digest() Digest { return request.digest }

func canonicalReviewRequest(request ReviewRequest) ([]byte, error) {
	if request.workspaceID.IsZero() || request.generation.IsZero() || request.attemptID.IsZero() ||
		request.mergeUnit.planID.IsZero() || request.mergeUnit.mergeUnitID.IsZero() || request.loopDigest.IsZero() ||
		request.round == 0 || request.profile.id.IsZero() || request.profile.runner.IsZero() ||
		!request.profile.reviewerPolicy.valid() || request.profileOrdinal == 0 || request.invocation == 0 ||
		request.head.IsZero() || request.tree.IsZero() || request.head.Algorithm() != request.tree.Algorithm() ||
		!request.isolationRequired.Strict() {
		return nil, fmt.Errorf("review request requires exact loop, profile, Git, and isolation bindings")
	}
	type requestJSON struct {
		SchemaVersion  int                  `json:"schema_version"`
		WorkspaceID    string               `json:"workspace_id"`
		Generation     string               `json:"generation"`
		AttemptID      string               `json:"attempt_id"`
		PlanID         string               `json:"plan_id"`
		MergeUnitID    string               `json:"merge_unit_id"`
		Loop           string               `json:"loop_digest"`
		Round          uint16               `json:"round"`
		Profile        string               `json:"profile"`
		Runner         string               `json:"runner"`
		ReviewerPolicy ReviewReviewerPolicy `json:"reviewer_policy"`
		ProfileOrdinal uint16               `json:"profile_ordinal"`
		Invocation     uint16               `json:"invocation"`
		Head           string               `json:"head"`
		Tree           string               `json:"tree"`
		Isolation      string               `json:"isolation_requirements_digest"`
	}
	return json.Marshal(requestJSON{
		SchemaVersion: 2, WorkspaceID: request.workspaceID.String(), Generation: request.generation.String(),
		AttemptID: request.attemptID.String(), PlanID: request.mergeUnit.planID.String(),
		MergeUnitID: request.mergeUnit.mergeUnitID.String(), Loop: request.loopDigest.String(), Round: request.round,
		Profile: request.profile.id.String(), Runner: request.profile.runner.String(),
		ReviewerPolicy: request.profile.reviewerPolicy, ProfileOrdinal: request.profileOrdinal,
		Invocation: request.invocation, Head: request.head.String(), Tree: request.tree.String(),
		Isolation: request.isolationRequired.digest.String(),
	})
}

type VerifiedReviewResult struct {
	request           ReviewRequest
	submission        ReviewResultSubmission
	reservationDigest Digest
}

func (result VerifiedReviewResult) Request() ReviewRequest { return result.request }
func (result VerifiedReviewResult) Submission() ReviewResultSubmission {
	return cloneReviewResult(result.submission)
}
func (result VerifiedReviewResult) ReservationDigest() Digest { return result.reservationDigest }

type ReviewRoundState struct {
	ordinal      uint16
	head         GitObjectID
	tree         GitObjectID
	reservations []ReviewInvocationReservation
	failures     []ReviewInvocationFailure
	attempts     []VerifiedReviewResult
	results      []VerifiedReviewResult
}

func (round ReviewRoundState) Ordinal() uint16   { return round.ordinal }
func (round ReviewRoundState) Head() GitObjectID { return round.head }
func (round ReviewRoundState) Tree() GitObjectID { return round.tree }
func (round ReviewRoundState) Reservations() []ReviewInvocationReservation {
	return cloneReviewInvocationReservations(round.reservations)
}
func (round ReviewRoundState) Failures() []ReviewInvocationFailure {
	return cloneReviewInvocationFailures(round.failures)
}
func (round ReviewRoundState) Attempts() []VerifiedReviewResult {
	return cloneVerifiedReviewResults(round.attempts)
}
func (round ReviewRoundState) Results() []VerifiedReviewResult {
	return cloneVerifiedReviewResults(round.results)
}
func (round ReviewRoundState) Complete(required int) bool { return len(round.results) == required }
func (round ReviewRoundState) Findings() []ReviewFinding {
	var findings []ReviewFinding
	for _, result := range round.results {
		findings = append(findings, result.submission.findings...)
	}
	return append([]ReviewFinding(nil), findings...)
}
func (round ReviewRoundState) HasBlockingFindings() bool {
	for _, finding := range round.Findings() {
		if finding.Blocking() {
			return true
		}
	}
	return false
}

type ReviewExhaustionReason string

const (
	ReviewExhaustedRounds         ReviewExhaustionReason = "round_budget"
	ReviewExhaustedInfrastructure ReviewExhaustionReason = "infrastructure_budget"
)

func (reason ReviewExhaustionReason) valid() bool {
	return reason == ReviewExhaustedRounds || reason == ReviewExhaustedInfrastructure
}

type ReviewExhaustionOwnerChoice string

const ReviewExhaustionStop ReviewExhaustionOwnerChoice = "stop"

type ReviewExhaustionDirective struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	head           GitObjectID
	tree           GitObjectID
	reason         ReviewExhaustionReason
	roundsUsed     uint16
	infrastructure uint16
	digest         Digest
}

func (directive ReviewExhaustionDirective) WorkspaceID() ID                { return directive.workspaceID }
func (directive ReviewExhaustionDirective) Generation() Digest             { return directive.generation }
func (directive ReviewExhaustionDirective) AttemptID() ID                  { return directive.attemptID }
func (directive ReviewExhaustionDirective) Head() GitObjectID              { return directive.head }
func (directive ReviewExhaustionDirective) Tree() GitObjectID              { return directive.tree }
func (directive ReviewExhaustionDirective) Reason() ReviewExhaustionReason { return directive.reason }
func (directive ReviewExhaustionDirective) RoundsUsed() uint16             { return directive.roundsUsed }
func (directive ReviewExhaustionDirective) InfrastructureRetriesUsed() uint16 {
	return directive.infrastructure
}
func (directive ReviewExhaustionDirective) Digest() Digest { return directive.digest }
func (ReviewExhaustionDirective) Choices() []ReviewExhaustionOwnerChoice {
	return []ReviewExhaustionOwnerChoice{ReviewExhaustionStop}
}

type ReviewState struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	mergeUnit   MergeUnitReference
	loop        ReviewLoop
	head        GitObjectID
	tree        GitObjectID
	rounds      []ReviewRoundState
	exhaustion  *ReviewExhaustionDirective
}

func (state ReviewState) WorkspaceID() ID               { return state.workspaceID }
func (state ReviewState) Generation() Digest            { return state.generation }
func (state ReviewState) AttemptID() ID                 { return state.attemptID }
func (state ReviewState) MergeUnit() MergeUnitReference { return state.mergeUnit }
func (state ReviewState) Loop() ReviewLoop              { return cloneReviewLoop(state.loop) }
func (state ReviewState) Head() GitObjectID             { return state.head }
func (state ReviewState) Tree() GitObjectID             { return state.tree }
func (state ReviewState) Rounds() []ReviewRoundState    { return cloneReviewRounds(state.rounds) }
func (state ReviewState) RoundsUsed() uint16            { return uint16(len(state.rounds)) }
func (state ReviewState) InfrastructureRetriesUsed() uint16 {
	var used uint16
	for _, round := range state.rounds {
		for _, reservation := range round.reservations {
			// Invocation one is the substantive profile attempt. Every later
			// reserved invocation is a retry, regardless of whether it eventually
			// succeeds, reports a typed failure, or crashes in the runner.
			if reservation.request.invocation > 1 {
				used++
			}
		}
	}
	return used
}
func (state ReviewState) Exhaustion() (ReviewExhaustionDirective, bool) {
	if state.exhaustion == nil {
		return ReviewExhaustionDirective{}, false
	}
	return *state.exhaustion, true
}
func (state ReviewState) MergeReady() bool {
	if state.exhaustion != nil || len(state.rounds) == 0 {
		return false
	}
	round := state.rounds[len(state.rounds)-1]
	return round.Complete(len(state.loop.profiles)) && round.head == state.head && round.tree == state.tree &&
		!round.HasBlockingFindings()
}

func (state ReviewState) NextRequest() (ReviewRequest, bool, error) {
	if state.exhaustion != nil || len(state.rounds) == 0 {
		return ReviewRequest{}, false, nil
	}
	round := state.rounds[len(state.rounds)-1]
	if pending, ok := pendingReviewInvocation(round); ok {
		return pending.request, true, nil
	}
	if round.Complete(len(state.loop.profiles)) {
		return ReviewRequest{}, false, nil
	}
	profileIndex := len(round.results)
	profile := state.loop.profiles[profileIndex]
	invocation := uint16(1)
	for _, reservation := range round.reservations {
		if reservation.request.profileOrdinal == uint16(profileIndex+1) {
			invocation++
		}
	}
	request, err := newReviewRequest(
		state.workspaceID, state.generation, state.attemptID, state.mergeUnit, state.loop.digest,
		round.ordinal, profile, uint16(profileIndex+1), invocation, round.head, round.tree,
	)
	return request, err == nil, err
}

type ReviewEvent interface{ isReviewEvent() }

type StartReviewRound struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	mergeUnit   MergeUnitReference
	loop        ReviewLoop
	ordinal     uint16
	head        GitObjectID
	tree        GitObjectID
}

func NewStartReviewRound(
	workspaceID ID, generation Digest, attemptID ID, mergeUnit MergeUnitReference,
	loop ReviewLoop, ordinal uint16, head, tree GitObjectID,
) (StartReviewRound, error) {
	if workspaceID.IsZero() || generation.IsZero() || attemptID.IsZero() || mergeUnit.planID.IsZero() ||
		mergeUnit.mergeUnitID.IsZero() || loop.digest.IsZero() || ordinal == 0 || head.IsZero() || tree.IsZero() ||
		head.Algorithm() != tree.Algorithm() {
		return StartReviewRound{}, fmt.Errorf("review round start requires workspace, attempt, loop, ordinal, head, and tree")
	}
	return StartReviewRound{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, mergeUnit: mergeUnit,
		loop: cloneReviewLoop(loop), ordinal: ordinal, head: head, tree: tree,
	}, nil
}
func (StartReviewRound) isReviewEvent() {}

type ReserveReviewInvocation struct {
	reservation ReviewInvocationReservation
}

func NewReserveReviewInvocation(
	request ReviewRequest,
	reviewerInstance ID,
	idempotencyKey Digest,
) (ReserveReviewInvocation, error) {
	reservation, err := NewReviewInvocationReservation(request, reviewerInstance, idempotencyKey)
	if err != nil {
		return ReserveReviewInvocation{}, err
	}
	return ReserveReviewInvocation{reservation: reservation}, nil
}
func (ReserveReviewInvocation) isReviewEvent() {}

type RecordReviewInvocationFailure struct {
	reservationDigest Digest
	failureDigest     Digest
}

func NewRecordReviewInvocationFailure(
	reservationDigest, failureDigest Digest,
) (RecordReviewInvocationFailure, error) {
	failure, err := NewReviewInvocationFailure(reservationDigest, failureDigest)
	if err != nil {
		return RecordReviewInvocationFailure{}, err
	}
	return RecordReviewInvocationFailure{
		reservationDigest: failure.reservationDigest, failureDigest: failure.failureDigest,
	}, nil
}
func (RecordReviewInvocationFailure) isReviewEvent() {}

type RecordReviewResult struct {
	round             uint16
	profileOrdinal    uint16
	invocation        uint16
	reservationDigest Digest
	result            ReviewResultSubmission
}

func NewRecordReviewResult(
	round, profileOrdinal, invocation uint16,
	reservationDigest Digest,
	result ReviewResultSubmission,
) (RecordReviewResult, error) {
	canonical, err := canonicalReviewResult(result)
	if round == 0 || profileOrdinal == 0 || invocation == 0 || err != nil ||
		reservationDigest.IsZero() || result.digest != DigestBytes(canonical) {
		return RecordReviewResult{}, fmt.Errorf(
			"review result record requires canonical local result bindings",
		)
	}
	return RecordReviewResult{
		round: round, profileOrdinal: profileOrdinal, invocation: invocation, reservationDigest: reservationDigest,
		result: cloneReviewResult(result),
	}, nil
}
func (RecordReviewResult) isReviewEvent() {}

func ReduceReview(current ReviewState, event ReviewEvent) (ReviewState, error) {
	if event == nil {
		return ReviewState{}, fmt.Errorf("review event is required")
	}
	next := cloneReviewState(current)
	switch value := event.(type) {
	case StartReviewRound:
		if current.workspaceID.IsZero() {
			if value.ordinal != 1 {
				return ReviewState{}, fmt.Errorf("first review round must be ordinal 1")
			}
			next = ReviewState{
				workspaceID: value.workspaceID, generation: value.generation, attemptID: value.attemptID,
				mergeUnit: value.mergeUnit, loop: cloneReviewLoop(value.loop), head: value.head, tree: value.tree,
			}
		} else {
			if current.exhaustion != nil {
				return ReviewState{}, fmt.Errorf("review loop is exhausted")
			}
			if value.workspaceID != current.workspaceID || value.generation != current.generation ||
				value.attemptID != current.attemptID || value.mergeUnit != current.mergeUnit ||
				value.loop.digest != current.loop.digest {
				return ReviewState{}, fmt.Errorf("review round cannot reset durable configuration or identity")
			}
			if len(current.rounds) == 0 || !current.rounds[len(current.rounds)-1].Complete(len(current.loop.profiles)) {
				return ReviewState{}, fmt.Errorf("review round cannot start while the prior round is incomplete")
			}
			if value.ordinal != uint16(len(current.rounds)+1) ||
				value.head == current.head && value.tree != current.tree {
				return ReviewState{}, fmt.Errorf("review round does not match the next ordinal and exact current head/tree")
			}
			next.head, next.tree = value.head, value.tree
		}
		if value.ordinal > next.loop.maxRounds {
			return ReviewState{}, fmt.Errorf("review round budget is exhausted")
		}
		next.rounds = append(next.rounds, ReviewRoundState{ordinal: value.ordinal, head: value.head, tree: value.tree})
	case ReserveReviewInvocation:
		if current.workspaceID.IsZero() || current.exhaustion != nil || len(current.rounds) == 0 {
			return ReviewState{}, fmt.Errorf("review invocation requires an active non-exhausted round")
		}
		round := &next.rounds[len(next.rounds)-1]
		if pending, exists := pendingReviewInvocation(*round); exists {
			if pending.digest == value.reservation.digest {
				return next, nil
			}
			return ReviewState{}, fmt.Errorf("review request is already reserved by a different invocation")
		}
		request, ok, err := current.NextRequest()
		if err != nil || !ok || request.digest != value.reservation.request.digest {
			return ReviewState{}, fmt.Errorf("review invocation reservation does not match the ordered exact-head request")
		}
		canonical, err := canonicalReviewInvocationReservation(value.reservation)
		if err != nil || value.reservation.digest != DigestBytes(canonical) {
			return ReviewState{}, fmt.Errorf("review invocation reservation is not canonical")
		}
		for _, priorRound := range current.rounds {
			for _, prior := range priorRound.reservations {
				if prior.idempotencyKey == value.reservation.idempotencyKey {
					return ReviewState{}, fmt.Errorf("review invocation idempotency key is already bound")
				}
			}
		}
		if err := validateReviewInstancePolicy(current, request.profile, value.reservation.reviewerInstance); err != nil {
			return ReviewState{}, err
		}
		round.reservations = append(round.reservations, value.reservation)
	case RecordReviewInvocationFailure:
		if current.workspaceID.IsZero() || current.exhaustion != nil || len(current.rounds) == 0 {
			return ReviewState{}, fmt.Errorf("review invocation failure requires an active non-exhausted round")
		}
		round := &next.rounds[len(next.rounds)-1]
		pending, exists := pendingReviewInvocation(*round)
		if !exists || pending.digest != value.reservationDigest || value.failureDigest.IsZero() {
			return ReviewState{}, fmt.Errorf("review invocation failure does not match the pending reservation")
		}
		round.failures = append(round.failures, ReviewInvocationFailure{
			reservationDigest: value.reservationDigest, failureDigest: value.failureDigest,
		})
	case RecordReviewResult:
		if current.workspaceID.IsZero() || current.exhaustion != nil || len(current.rounds) == 0 {
			return ReviewState{}, fmt.Errorf("review result requires an active non-exhausted round")
		}
		round := &next.rounds[len(next.rounds)-1]
		reservation, reserved := pendingReviewInvocation(*round)
		if !reserved || reservation.digest != value.reservationDigest {
			return ReviewState{}, fmt.Errorf("review result has no matching durable invocation reservation")
		}
		request := reservation.request
		if value.round != request.round || value.profileOrdinal != request.profileOrdinal ||
			value.invocation != request.invocation || value.result.requestDigest != request.digest ||
			value.result.reviewerInstance != reservation.reviewerInstance || !value.result.isolation.Strict() {
			return ReviewState{}, fmt.Errorf("review result does not match the ordered exact-head request and strict sandbox")
		}
		verified := VerifiedReviewResult{
			request: request, submission: cloneReviewResult(value.result),
			reservationDigest: value.reservationDigest,
		}
		round.attempts = append(round.attempts, verified)
		if value.result.status == ReviewResultCompleted {
			round.results = append(round.results, verified)
		}
	default:
		return ReviewState{}, fmt.Errorf("unsupported review event %T", event)
	}
	if next.exhaustion == nil {
		next.exhaustion = deriveReviewExhaustion(next)
	}
	return next, nil
}

func pendingReviewInvocation(round ReviewRoundState) (ReviewInvocationReservation, bool) {
	if len(round.reservations) == 0 {
		return ReviewInvocationReservation{}, false
	}
	latest := round.reservations[len(round.reservations)-1]
	for _, result := range round.attempts {
		if result.reservationDigest == latest.digest {
			return ReviewInvocationReservation{}, false
		}
	}
	for _, failure := range round.failures {
		if failure.reservationDigest == latest.digest {
			return ReviewInvocationReservation{}, false
		}
	}
	return latest, true
}

func latestReviewInvocationFailed(round ReviewRoundState) bool {
	if len(round.reservations) == 0 {
		return false
	}
	latest := round.reservations[len(round.reservations)-1]
	for _, failure := range round.failures {
		if failure.reservationDigest == latest.digest {
			return true
		}
	}
	for _, result := range round.attempts {
		if result.reservationDigest == latest.digest {
			return result.submission.status == ReviewResultInfrastructureFailure
		}
	}
	return false
}

func validateReviewInstancePolicy(state ReviewState, profile ReviewProfile, instance ID) error {
	var prior []ID
	for _, round := range state.rounds {
		for _, reservation := range round.reservations {
			if reservation.request.profile.id == profile.id {
				prior = append(prior, reservation.reviewerInstance)
			}
		}
	}
	if len(prior) == 0 {
		return nil
	}
	if profile.reviewerPolicy == ReviewReviewerRetain {
		if prior[0] != instance {
			return fmt.Errorf("review profile %s must retain reviewer instance %s", profile.id, prior[0])
		}
		return nil
	}
	for _, used := range prior {
		if used == instance {
			return fmt.Errorf("review profile %s requires a fresh reviewer instance", profile.id)
		}
	}
	return nil
}

func deriveReviewExhaustion(state ReviewState) *ReviewExhaustionDirective {
	reason := ReviewExhaustionReason("")
	latestInfrastructureFailure := false
	if len(state.rounds) != 0 {
		latestInfrastructureFailure = latestReviewInvocationFailed(state.rounds[len(state.rounds)-1])
	}
	if latestInfrastructureFailure && state.InfrastructureRetriesUsed() >= state.loop.maxInfrastructureRetries {
		reason = ReviewExhaustedInfrastructure
	} else if len(state.rounds) != 0 {
		round := state.rounds[len(state.rounds)-1]
		if round.Complete(len(state.loop.profiles)) && round.head == state.head && round.tree == state.tree &&
			round.HasBlockingFindings() {
			if state.RoundsUsed() >= state.loop.maxRounds {
				reason = ReviewExhaustedRounds
			}
		}
	}
	if !reason.valid() {
		return nil
	}
	directive := ReviewExhaustionDirective{
		workspaceID: state.workspaceID, generation: state.generation, attemptID: state.attemptID,
		head: state.head, tree: state.tree, reason: reason, roundsUsed: state.RoundsUsed(),
		infrastructure: state.InfrastructureRetriesUsed(),
	}
	type directiveJSON struct {
		SchemaVersion  int                    `json:"schema_version"`
		WorkspaceID    string                 `json:"workspace_id"`
		Generation     string                 `json:"generation"`
		AttemptID      string                 `json:"attempt_id"`
		Head           string                 `json:"head"`
		Tree           string                 `json:"tree"`
		Reason         ReviewExhaustionReason `json:"reason"`
		RoundsUsed     uint16                 `json:"rounds_used"`
		Infrastructure uint16                 `json:"infrastructure_retries_used"`
	}
	canonical, _ := json.Marshal(directiveJSON{
		SchemaVersion: 2, WorkspaceID: directive.workspaceID.String(), Generation: directive.generation.String(),
		AttemptID: directive.attemptID.String(), Head: directive.head.String(), Tree: directive.tree.String(),
		Reason: directive.reason, RoundsUsed: directive.roundsUsed, Infrastructure: directive.infrastructure,
	})
	directive.digest = DigestBytes(canonical)
	return &directive
}

func cloneReviewResult(result ReviewResultSubmission) ReviewResultSubmission {
	result.findings = append([]ReviewFinding(nil), result.findings...)
	return result
}

func cloneVerifiedReviewResults(values []VerifiedReviewResult) []VerifiedReviewResult {
	result := append([]VerifiedReviewResult(nil), values...)
	for index := range result {
		result[index].submission = cloneReviewResult(result[index].submission)
	}
	return result
}

func cloneReviewRounds(values []ReviewRoundState) []ReviewRoundState {
	result := append([]ReviewRoundState(nil), values...)
	for index := range result {
		result[index].reservations = cloneReviewInvocationReservations(result[index].reservations)
		result[index].failures = cloneReviewInvocationFailures(result[index].failures)
		result[index].attempts = cloneVerifiedReviewResults(result[index].attempts)
		result[index].results = cloneVerifiedReviewResults(result[index].results)
	}
	return result
}

func cloneReviewState(state ReviewState) ReviewState {
	state.loop = cloneReviewLoop(state.loop)
	state.rounds = cloneReviewRounds(state.rounds)
	if state.exhaustion != nil {
		directive := *state.exhaustion
		state.exhaustion = &directive
	}
	return state
}

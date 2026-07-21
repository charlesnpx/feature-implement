package workspace

import (
	"fmt"
	"time"
)

const commitJournalRecordSafetyBytes = 16 * 1024

type CommitProtocolStartedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	base        GitObjectID
	protocol    CommitProtocol
}

func NewCommitProtocolStartedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	base GitObjectID,
	protocol CommitProtocol,
) (CommitProtocolStartedJournalEvent, error) {
	event := CommitProtocolStartedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		base: base, protocol: *cloneCommitProtocol(&protocol),
	}
	if err := event.validate(); err != nil {
		return CommitProtocolStartedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return CommitProtocolStartedJournalEvent{}, err
	}
	return event, nil
}

func (CommitProtocolStartedJournalEvent) isWorkspaceJournalEvent() {}
func (CommitProtocolStartedJournalEvent) eventType() JournalEventType {
	return JournalEventCommitProtocolStarted
}
func (event CommitProtocolStartedJournalEvent) boundGeneration() Digest { return event.generation }
func (event CommitProtocolStartedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.base.IsZero() || event.protocol.digest.IsZero() {
		return fmt.Errorf("commit protocol start requires workspace, generation, attempt, base, and protocol")
	}
	_, err := NewCommitProtocolState(event.generation, event.base, event.protocol)
	return err
}
func (event CommitProtocolStartedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event CommitProtocolStartedJournalEvent) Generation() Digest { return event.generation }
func (event CommitProtocolStartedJournalEvent) AttemptID() ID      { return event.attemptID }
func (event CommitProtocolStartedJournalEvent) Base() GitObjectID  { return event.base }
func (event CommitProtocolStartedJournalEvent) Protocol() CommitProtocol {
	return *cloneCommitProtocol(&event.protocol)
}

type CommitStepIntendedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocolDigest Digest
	stepID         ID
	ordinal        uint16
	parent         GitObjectID
	inspection     StagedCommitInspection
	body           string
	rebaseEpoch    uint64
	idempotencyKey Digest
}

func NewCommitStepIntendedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	stepID ID,
	ordinal uint16,
	parent GitObjectID,
	inspection StagedCommitInspection,
	body string,
	rebaseEpoch uint64,
) (CommitStepIntendedJournalEvent, error) {
	key, err := commitEffectIdempotencyKey(
		generation, protocolDigest, stepID, ordinal, parent,
		inspection.stateDigest, body, rebaseEpoch,
	)
	if err != nil {
		return CommitStepIntendedJournalEvent{}, err
	}
	event := CommitStepIntendedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, stepID: stepID, ordinal: ordinal,
		parent: parent, inspection: cloneStagedCommitInspection(inspection), body: body,
		rebaseEpoch: rebaseEpoch, idempotencyKey: key,
	}
	if err := event.validate(); err != nil {
		return CommitStepIntendedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return CommitStepIntendedJournalEvent{}, err
	}
	return event, nil
}

func (CommitStepIntendedJournalEvent) isWorkspaceJournalEvent() {}
func (CommitStepIntendedJournalEvent) eventType() JournalEventType {
	return JournalEventCommitStepIntended
}
func (event CommitStepIntendedJournalEvent) boundGeneration() Digest { return event.generation }
func (event CommitStepIntendedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocolDigest.IsZero() || event.stepID.IsZero() || event.ordinal == 0 || event.parent.IsZero() ||
		event.inspection.stateDigest.IsZero() || event.idempotencyKey.IsZero() {
		return fmt.Errorf("commit step intent requires complete immutable bindings")
	}
	if err := validateCommitBody(event.body); err != nil {
		return err
	}
	key, err := commitEffectIdempotencyKey(
		event.generation, event.protocolDigest, event.stepID, event.ordinal, event.parent,
		event.inspection.stateDigest, event.body, event.rebaseEpoch,
	)
	if err != nil || key != event.idempotencyKey {
		return fmt.Errorf("commit step intent idempotency key does not match its bindings")
	}
	return nil
}
func (event CommitStepIntendedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event CommitStepIntendedJournalEvent) Generation() Digest     { return event.generation }
func (event CommitStepIntendedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event CommitStepIntendedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event CommitStepIntendedJournalEvent) StepID() ID             { return event.stepID }
func (event CommitStepIntendedJournalEvent) Ordinal() uint16        { return event.ordinal }
func (event CommitStepIntendedJournalEvent) Parent() GitObjectID    { return event.parent }
func (event CommitStepIntendedJournalEvent) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(event.inspection)
}
func (event CommitStepIntendedJournalEvent) Body() string           { return event.body }
func (event CommitStepIntendedJournalEvent) RebaseEpoch() uint64    { return event.rebaseEpoch }
func (event CommitStepIntendedJournalEvent) IdempotencyKey() Digest { return event.idempotencyKey }

type CommitStepRecordedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocolDigest Digest
	intentKey      Digest
	evidence       CommitObjectEvidence
}

func NewCommitStepRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest, intentKey Digest,
	evidence CommitObjectEvidence,
) (CommitStepRecordedJournalEvent, error) {
	event := CommitStepRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, intentKey: intentKey,
		evidence: cloneCommitObjectEvidence(evidence),
	}
	if err := event.validate(); err != nil {
		return CommitStepRecordedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return CommitStepRecordedJournalEvent{}, err
	}
	return event, nil
}

func (CommitStepRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (CommitStepRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventCommitStepRecorded
}
func (event CommitStepRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event CommitStepRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocolDigest.IsZero() || event.intentKey.IsZero() || event.evidence.evidence.IsZero() ||
		event.evidence.generation != event.generation {
		return fmt.Errorf("commit step record requires protocol intent and generation-bound evidence")
	}
	content, err := canonicalCommitObjectEvidence(event.evidence)
	if err != nil || DigestBytes(content) != event.evidence.evidence {
		return fmt.Errorf("commit step evidence digest does not match canonical bindings")
	}
	return nil
}
func (event CommitStepRecordedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event CommitStepRecordedJournalEvent) Generation() Digest     { return event.generation }
func (event CommitStepRecordedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event CommitStepRecordedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event CommitStepRecordedJournalEvent) IntentKey() Digest      { return event.intentKey }
func (event CommitStepRecordedJournalEvent) Evidence() CommitObjectEvidence {
	return cloneCommitObjectEvidence(event.evidence)
}

type CommitCheckRecordedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocolDigest Digest
	stepOrdinal    uint16
	checkOrdinal   uint16
	idempotencyKey Digest
	evidence       CommitCheckEvidence
}

func NewCommitCheckRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	stepOrdinal, checkOrdinal uint16,
	idempotencyKey Digest,
	evidence CommitCheckEvidence,
) (CommitCheckRecordedJournalEvent, error) {
	event := CommitCheckRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, stepOrdinal: stepOrdinal, checkOrdinal: checkOrdinal,
		idempotencyKey: idempotencyKey, evidence: cloneOneCommitCheckEvidence(evidence),
	}
	if err := event.validate(); err != nil {
		return CommitCheckRecordedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return CommitCheckRecordedJournalEvent{}, err
	}
	return event, nil
}

func (CommitCheckRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (CommitCheckRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventCommitCheckRecorded
}
func (event CommitCheckRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event CommitCheckRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocolDigest.IsZero() || event.stepOrdinal == 0 || event.checkOrdinal == 0 ||
		event.idempotencyKey.IsZero() || event.evidence.evidence.IsZero() || event.evidence.generation != event.generation {
		return fmt.Errorf("commit check record requires complete protocol and evidence bindings")
	}
	content, err := canonicalCommitCheckEvidence(event.evidence)
	if err != nil || DigestBytes(content) != event.evidence.evidence {
		return fmt.Errorf("commit check evidence digest does not match canonical bindings")
	}
	return nil
}
func (event CommitCheckRecordedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event CommitCheckRecordedJournalEvent) Generation() Digest     { return event.generation }
func (event CommitCheckRecordedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event CommitCheckRecordedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event CommitCheckRecordedJournalEvent) StepOrdinal() uint16    { return event.stepOrdinal }
func (event CommitCheckRecordedJournalEvent) CheckOrdinal() uint16   { return event.checkOrdinal }
func (event CommitCheckRecordedJournalEvent) IdempotencyKey() Digest { return event.idempotencyKey }
func (event CommitCheckRecordedJournalEvent) Evidence() CommitCheckEvidence {
	return cloneOneCommitCheckEvidence(event.evidence)
}

type CommitProtocolRebasedJournalEvent struct {
	workspaceID          ID
	generation           Digest
	attemptID            ID
	protocolDigest       Digest
	base                 GitObjectID
	commits              []CommitObjectEvidence
	reviewProtocolDigest Digest
	reviewCommits        []CommitObjectEvidence
	mappingDigest        Digest
}

func NewCommitProtocolRebasedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	base GitObjectID,
	commits []CommitObjectEvidence,
) (CommitProtocolRebasedJournalEvent, error) {
	return NewCommitProtocolChainRebasedJournalEvent(
		workspaceID, generation, attemptID, protocolDigest, base, commits, Digest{}, nil,
	)
}

func NewCommitProtocolChainRebasedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	base GitObjectID,
	commits []CommitObjectEvidence,
	reviewProtocolDigest Digest,
	reviewCommits []CommitObjectEvidence,
) (CommitProtocolRebasedJournalEvent, error) {
	event := CommitProtocolRebasedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, base: base,
		commits: cloneCommitObjects(commits), reviewProtocolDigest: reviewProtocolDigest,
		reviewCommits: cloneCommitObjects(reviewCommits),
	}
	digest, err := digestCommitRebaseMapping(event)
	if err != nil {
		return CommitProtocolRebasedJournalEvent{}, err
	}
	event.mappingDigest = digest
	if err := event.validate(); err != nil {
		return CommitProtocolRebasedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return CommitProtocolRebasedJournalEvent{}, err
	}
	return event, nil
}

func maxCommitJournalIdentifier() ID {
	value := "a"
	for len(value) < maxIdentifierBytes {
		value += "1"
	}
	identifier, _ := NewID(value)
	return identifier
}

func validateCommitJournalRecordFootprint(event WorkspaceJournalEvent) error {
	reads, writes, ok := commitJournalEventResources(event)
	if !ok {
		return fmt.Errorf("unsupported commit journal footprint event %T", event)
	}
	reads, _ = normalizeJournalWriteSet(reads)
	writes, _ = normalizeJournalWriteSet(writes)
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(resource, ^uint64(0))
		readSet = append(readSet, revision)
	}
	request, err := newPrivilegedJournalAppend(
		event,
		time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
		readSet,
		writes,
	)
	if err != nil {
		return err
	}
	record, err := buildJournalRecord(JournalSnapshot{}, request)
	if err != nil {
		return err
	}
	encoded, err := marshalJournalRecord(record)
	if err != nil {
		return err
	}
	if len(encoded) > MaxJournalRecordBytes-commitJournalRecordSafetyBytes {
		return fmt.Errorf(
			"commit journal record footprint %d exceeds its safe %d-byte bound",
			len(encoded), MaxJournalRecordBytes-commitJournalRecordSafetyBytes,
		)
	}
	return nil
}

func (CommitProtocolRebasedJournalEvent) isWorkspaceJournalEvent() {}
func (CommitProtocolRebasedJournalEvent) eventType() JournalEventType {
	return JournalEventCommitProtocolRebased
}
func (event CommitProtocolRebasedJournalEvent) boundGeneration() Digest { return event.generation }
func (event CommitProtocolRebasedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.base.IsZero() || event.mappingDigest.IsZero() {
		return fmt.Errorf("commit protocol rebase requires complete mapping bindings")
	}
	hasImplementation := !event.protocolDigest.IsZero()
	hasReviewFixes := !event.reviewProtocolDigest.IsZero()
	if !hasImplementation && !hasReviewFixes ||
		!hasImplementation && len(event.commits) != 0 ||
		hasReviewFixes != (len(event.reviewCommits) != 0) {
		return fmt.Errorf("commit protocol rebase requires complete implementation or review-fix mappings")
	}
	for _, commit := range event.commits {
		if commit.generation != event.generation || commit.evidence.IsZero() {
			return fmt.Errorf("rebased commit evidence does not match generation")
		}
	}
	for _, commit := range event.reviewCommits {
		if commit.generation != event.generation || commit.evidence.IsZero() {
			return fmt.Errorf("rebased review-fix evidence does not match generation")
		}
	}
	digest, err := digestCommitRebaseMapping(event)
	if err != nil || digest != event.mappingDigest {
		return fmt.Errorf("commit protocol rebase mapping digest does not match")
	}
	return nil
}
func (event CommitProtocolRebasedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event CommitProtocolRebasedJournalEvent) Generation() Digest     { return event.generation }
func (event CommitProtocolRebasedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event CommitProtocolRebasedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event CommitProtocolRebasedJournalEvent) Base() GitObjectID      { return event.base }
func (event CommitProtocolRebasedJournalEvent) Commits() []CommitObjectEvidence {
	return cloneCommitObjects(event.commits)
}
func (event CommitProtocolRebasedJournalEvent) ReviewProtocolDigest() Digest {
	return event.reviewProtocolDigest
}
func (event CommitProtocolRebasedJournalEvent) ReviewCommits() []CommitObjectEvidence {
	return cloneCommitObjects(event.reviewCommits)
}
func (event CommitProtocolRebasedJournalEvent) MappingDigest() Digest { return event.mappingDigest }

func digestCommitRebaseMapping(event CommitProtocolRebasedJournalEvent) (Digest, error) {
	if event.generation.IsZero() || event.base.IsZero() ||
		event.protocolDigest.IsZero() && (event.reviewProtocolDigest.IsZero() || len(event.reviewCommits) == 0) {
		return Digest{}, fmt.Errorf("commit rebase mapping is incomplete")
	}
	var bindings []byte
	if !event.protocolDigest.IsZero() {
		bindings = []byte(fmt.Sprintf(
			"commit_rebase_mapping_v2\ngeneration=%s\nprotocol=%s\nbase=%s\n",
			event.generation, event.protocolDigest, event.base,
		))
	} else {
		bindings = []byte(fmt.Sprintf(
			"protocol_chain_rebase_mapping_v2\ngeneration=%s\nbase=%s\n",
			event.generation, event.base,
		))
	}
	for index, commit := range event.commits {
		if commit.evidence.IsZero() {
			return Digest{}, fmt.Errorf("rebased commit %d lacks evidence digest", index+1)
		}
		bindings = append(bindings, []byte(fmt.Sprintf("commit_%d=%s\n", index+1, commit.evidence))...)
	}
	if len(event.reviewCommits) != 0 {
		bindings = append(bindings, []byte(fmt.Sprintf("review_protocol=%s\n", event.reviewProtocolDigest))...)
		for index, commit := range event.reviewCommits {
			if commit.evidence.IsZero() {
				return Digest{}, fmt.Errorf("rebased review fix %d lacks evidence digest", index+1)
			}
			bindings = append(bindings, []byte(fmt.Sprintf("review_commit_%d=%s\n", index+1, commit.evidence))...)
		}
	}
	return DigestBytes(bindings), nil
}

func CommitProtocolJournalResource(attemptID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceCommitProtocol, attemptID.String()+"/implementation")
	return resource
}

func CommitStepJournalResource(attemptID, stepID ID, ordinal uint16) JournalResource {
	identity := fmt.Sprintf("%s/%d/%s", attemptID, ordinal, stepID)
	resource, _ := NewJournalResource(JournalResourceCommitStep, identity)
	return resource
}

func CommitCheckJournalResource(attemptID, stepID, checkID ID, stepOrdinal, checkOrdinal uint16) JournalResource {
	identity := fmt.Sprintf("%s/%d/%s/%d/%s", attemptID, stepOrdinal, stepID, checkOrdinal, checkID)
	resource, _ := NewJournalResource(JournalResourceCheck, identity)
	return resource
}

func isCommitJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case CommitProtocolStartedJournalEvent, CommitStepIntendedJournalEvent,
		CommitStepRecordedJournalEvent, CommitCheckRecordedJournalEvent,
		CommitProtocolRebasedJournalEvent:
		return true
	default:
		return isReviewFixJournalEvent(event)
	}
}

func commitJournalEventResources(event WorkspaceJournalEvent) ([]JournalResource, []JournalResource, bool) {
	var workspaceID, attemptID ID
	var generation Digest
	var reads, writes []JournalResource
	switch event := event.(type) {
	case CommitProtocolStartedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		protocol := CommitProtocolJournalResource(attemptID)
		reads = []JournalResource{AttemptJournalResource(attemptID), protocol}
		writes = append([]JournalResource(nil), reads...)
	case CommitStepIntendedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		step := CommitStepJournalResource(attemptID, event.stepID, event.ordinal)
		reads = []JournalResource{AttemptJournalResource(attemptID), CommitProtocolJournalResource(attemptID), step}
		writes = append([]JournalResource(nil), reads...)
	case CommitStepRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		step := CommitStepJournalResource(attemptID, event.evidence.stepID, event.evidence.ordinal)
		evidence := EvidenceJournalResource(event.evidence.evidence)
		reads = []JournalResource{AttemptJournalResource(attemptID), CommitProtocolJournalResource(attemptID), step, evidence}
		writes = append([]JournalResource(nil), reads...)
	case CommitCheckRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		check := CommitCheckJournalResource(
			attemptID, event.evidence.stepID, event.evidence.checkID, event.stepOrdinal, event.checkOrdinal,
		)
		evidence := EvidenceJournalResource(event.evidence.evidence)
		reads = []JournalResource{AttemptJournalResource(attemptID), CommitProtocolJournalResource(attemptID), check, evidence}
		writes = append([]JournalResource(nil), reads...)
	case CommitProtocolRebasedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		reads = []JournalResource{AttemptJournalResource(attemptID)}
		if !event.protocolDigest.IsZero() {
			reads = append(reads, CommitProtocolJournalResource(attemptID))
		}
		for _, commit := range event.commits {
			reads = append(
				reads,
				CommitStepJournalResource(attemptID, commit.stepID, commit.ordinal),
				EvidenceJournalResource(commit.evidence),
			)
		}
		if len(event.reviewCommits) != 0 {
			reads = append(reads, ReviewFixJournalResource(attemptID))
		}
		for _, commit := range event.reviewCommits {
			reads = append(
				reads,
				ReviewFixStepJournalResource(attemptID, commit.stepID, commit.ordinal),
				EvidenceJournalResource(commit.evidence),
			)
		}
		writes = append([]JournalResource(nil), reads...)
		if len(event.reviewCommits) != 0 {
			reads = append(reads, ReviewFixBudgetJournalResource(attemptID))
		}
	default:
		return reviewFixJournalEventResources(event)
	}
	reads = append(reads, WorkspaceJournalResource(workspaceID), GenerationJournalResource(generation))
	return reads, writes, true
}

func cloneCommitJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case CommitProtocolStartedJournalEvent:
		value.protocol = *cloneCommitProtocol(&value.protocol)
		return value
	case CommitStepIntendedJournalEvent:
		value.inspection = cloneStagedCommitInspection(value.inspection)
		return value
	case CommitStepRecordedJournalEvent:
		value.evidence = cloneCommitObjectEvidence(value.evidence)
		return value
	case CommitCheckRecordedJournalEvent:
		value.evidence = cloneOneCommitCheckEvidence(value.evidence)
		return value
	case CommitProtocolRebasedJournalEvent:
		value.commits = cloneCommitObjects(value.commits)
		value.reviewCommits = cloneCommitObjects(value.reviewCommits)
		return value
	default:
		return cloneReviewFixJournalEvent(event)
	}
}

func cloneCommitObjects(values []CommitObjectEvidence) []CommitObjectEvidence {
	result := make([]CommitObjectEvidence, len(values))
	for index, value := range values {
		result[index] = cloneCommitObjectEvidence(value)
	}
	return result
}

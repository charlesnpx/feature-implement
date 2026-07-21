package workspace

import "fmt"

type ReviewFixPhase string

const (
	ReviewFixReady          ReviewFixPhase = "ready"
	ReviewFixReserved       ReviewFixPhase = "reserved"
	ReviewFixAwaitingCommit ReviewFixPhase = "awaiting_commit"
	ReviewFixAwaitingChecks ReviewFixPhase = "awaiting_checks"
	ReviewFixComplete       ReviewFixPhase = "complete"
)

func (phase ReviewFixPhase) valid() bool {
	switch phase {
	case ReviewFixReady, ReviewFixReserved, ReviewFixAwaitingCommit, ReviewFixAwaitingChecks, ReviewFixComplete:
		return true
	default:
		return false
	}
}

type ReviewFixStepState struct {
	ordinal        uint16
	parent         GitObjectID
	phase          ReviewFixPhase
	reservationKey Digest
	inspection     StagedCommitInspection
	body           string
	intentKey      Digest
	commit         CommitObjectEvidence
	checks         []CommitCheckEvidence
}

func (state ReviewFixStepState) Ordinal() uint16        { return state.ordinal }
func (state ReviewFixStepState) Parent() GitObjectID    { return state.parent }
func (state ReviewFixStepState) Phase() ReviewFixPhase  { return state.phase }
func (state ReviewFixStepState) ReservationKey() Digest { return state.reservationKey }
func (state ReviewFixStepState) IntentKey() Digest      { return state.intentKey }
func (state ReviewFixStepState) Body() string           { return state.body }
func (state ReviewFixStepState) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(state.inspection)
}
func (state ReviewFixStepState) Commit() (CommitObjectEvidence, bool) {
	if state.commit.evidence.IsZero() {
		return CommitObjectEvidence{}, false
	}
	return cloneCommitObjectEvidence(state.commit), true
}
func (state ReviewFixStepState) Checks() []CommitCheckEvidence {
	return cloneCommitCheckEvidence(state.checks)
}

type ReviewFixState struct {
	generation  Digest
	base        GitObjectID
	protocol    ReviewFixProtocol
	maximum     uint16
	fixes       []ReviewFixStepState
	checkingFix int
	rebaseEpoch uint64
}

func NewReviewFixState(
	generation Digest,
	base GitObjectID,
	protocol ReviewFixProtocol,
	maximum uint16,
) (ReviewFixState, error) {
	state := ReviewFixState{
		generation:  generation,
		base:        base,
		protocol:    *cloneReviewFixProtocol(&protocol),
		maximum:     maximum,
		checkingFix: -1,
	}
	if err := validateReviewFixState(state); err != nil {
		return ReviewFixState{}, err
	}
	return state, nil
}

func (state ReviewFixState) Generation() Digest { return state.generation }
func (state ReviewFixState) Base() GitObjectID  { return state.base }
func (state ReviewFixState) Protocol() ReviewFixProtocol {
	return *cloneReviewFixProtocol(&state.protocol)
}
func (state ReviewFixState) ProtocolDigest() Digest { return state.protocol.digest }
func (state ReviewFixState) Maximum() uint16        { return state.maximum }
func (state ReviewFixState) RebaseEpoch() uint64    { return state.rebaseEpoch }
func (state ReviewFixState) Used() uint16           { return uint16(len(state.fixes)) }
func (state ReviewFixState) Remaining() uint16      { return state.maximum - state.Used() }
func (state ReviewFixState) Fixes() []ReviewFixStepState {
	return cloneReviewFixStepStates(state.fixes)
}
func (state ReviewFixState) Phase() ReviewFixPhase {
	if state.checkingFix >= 0 {
		return ReviewFixAwaitingChecks
	}
	if len(state.fixes) == 0 {
		return ReviewFixReady
	}
	return state.fixes[len(state.fixes)-1].phase
}
func (state ReviewFixState) Quiescent() bool {
	phase := state.Phase()
	return phase == ReviewFixReady || phase == ReviewFixComplete
}
func (state ReviewFixState) Head() GitObjectID {
	head := state.base
	for _, fix := range state.fixes {
		if fix.commit.commit.IsZero() {
			break
		}
		head = fix.commit.commit
	}
	return head
}

type ReviewFixEvent interface {
	isReviewFixEvent()
}

type ReserveReviewFix struct {
	ordinal uint16
	parent  GitObjectID
}

func NewReserveReviewFix(ordinal uint16, parent GitObjectID) (ReserveReviewFix, error) {
	if ordinal == 0 || parent.IsZero() {
		return ReserveReviewFix{}, fmt.Errorf("review-fix reservation requires ordinal and parent")
	}
	return ReserveReviewFix{ordinal: ordinal, parent: parent}, nil
}

func (ReserveReviewFix) isReviewFixEvent()         {}
func (event ReserveReviewFix) Ordinal() uint16     { return event.ordinal }
func (event ReserveReviewFix) Parent() GitObjectID { return event.parent }

type StageReviewFix struct {
	ordinal    uint16
	inspection StagedCommitInspection
	body       string
}

func NewStageReviewFix(ordinal uint16, inspection StagedCommitInspection, body string) (StageReviewFix, error) {
	if ordinal == 0 || inspection.stateDigest.IsZero() {
		return StageReviewFix{}, fmt.Errorf("review-fix intent requires ordinal and staged inspection")
	}
	if err := validateCommitBody(body); err != nil {
		return StageReviewFix{}, err
	}
	return StageReviewFix{ordinal: ordinal, inspection: cloneStagedCommitInspection(inspection), body: body}, nil
}

func (StageReviewFix) isReviewFixEvent()     {}
func (event StageReviewFix) Ordinal() uint16 { return event.ordinal }
func (event StageReviewFix) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(event.inspection)
}
func (event StageReviewFix) Body() string { return event.body }

type RecordReviewFixCommit struct {
	ordinal  uint16
	evidence CommitObjectEvidence
}

func NewRecordReviewFixCommit(ordinal uint16, evidence CommitObjectEvidence) (RecordReviewFixCommit, error) {
	if ordinal == 0 || evidence.evidence.IsZero() {
		return RecordReviewFixCommit{}, fmt.Errorf("review-fix commit record requires ordinal and evidence")
	}
	if err := evidence.validate(); err != nil {
		return RecordReviewFixCommit{}, err
	}
	return RecordReviewFixCommit{ordinal: ordinal, evidence: cloneCommitObjectEvidence(evidence)}, nil
}

func (RecordReviewFixCommit) isReviewFixEvent()     {}
func (event RecordReviewFixCommit) Ordinal() uint16 { return event.ordinal }
func (event RecordReviewFixCommit) Evidence() CommitObjectEvidence {
	return cloneCommitObjectEvidence(event.evidence)
}

type RecordReviewFixCheck struct {
	ordinal  uint16
	evidence CommitCheckEvidence
}

func NewRecordReviewFixCheck(ordinal uint16, evidence CommitCheckEvidence) (RecordReviewFixCheck, error) {
	if ordinal == 0 || evidence.evidence.IsZero() {
		return RecordReviewFixCheck{}, fmt.Errorf("review-fix check record requires ordinal and evidence")
	}
	return RecordReviewFixCheck{ordinal: ordinal, evidence: cloneOneCommitCheckEvidence(evidence)}, nil
}

func (RecordReviewFixCheck) isReviewFixEvent()     {}
func (event RecordReviewFixCheck) Ordinal() uint16 { return event.ordinal }
func (event RecordReviewFixCheck) Evidence() CommitCheckEvidence {
	return cloneOneCommitCheckEvidence(event.evidence)
}

type RemapRebasedReviewFixes struct {
	base    GitObjectID
	commits []CommitObjectEvidence
}

func NewRemapRebasedReviewFixes(
	base GitObjectID,
	commits []CommitObjectEvidence,
) (RemapRebasedReviewFixes, error) {
	if base.IsZero() || len(commits) == 0 {
		return RemapRebasedReviewFixes{}, fmt.Errorf("review-fix rebase remapping requires a new base and commits")
	}
	copyCommits := make([]CommitObjectEvidence, len(commits))
	for index, commit := range commits {
		if err := commit.validate(); err != nil || commit.evidence.IsZero() {
			if err == nil {
				err = fmt.Errorf("commit evidence digest is required")
			}
			return RemapRebasedReviewFixes{}, fmt.Errorf("rebased review fix %d: %w", index+1, err)
		}
		copyCommits[index] = cloneCommitObjectEvidence(commit)
	}
	return RemapRebasedReviewFixes{base: base, commits: copyCommits}, nil
}

func (RemapRebasedReviewFixes) isReviewFixEvent()       {}
func (event RemapRebasedReviewFixes) Base() GitObjectID { return event.base }
func (event RemapRebasedReviewFixes) Commits() []CommitObjectEvidence {
	return cloneCommitObjects(event.commits)
}

type ReviewFixCompletedEffect struct {
	generation Digest
	protocol   Digest
	ordinal    uint16
	head       GitObjectID
	evidence   Digest
}

func (ReviewFixCompletedEffect) isCommitProtocolEffect()       {}
func (effect ReviewFixCompletedEffect) Generation() Digest     { return effect.generation }
func (effect ReviewFixCompletedEffect) ProtocolDigest() Digest { return effect.protocol }
func (effect ReviewFixCompletedEffect) Ordinal() uint16        { return effect.ordinal }
func (effect ReviewFixCompletedEffect) Head() GitObjectID      { return effect.head }
func (effect ReviewFixCompletedEffect) EvidenceDigest() Digest { return effect.evidence }

type ReviewFixReduction struct {
	state   ReviewFixState
	effects []CommitProtocolEffect
}

func (reduction ReviewFixReduction) State() ReviewFixState {
	return cloneReviewFixState(reduction.state)
}
func (reduction ReviewFixReduction) Effects() []CommitProtocolEffect {
	return append([]CommitProtocolEffect(nil), reduction.effects...)
}

func ReduceReviewFix(current ReviewFixState, event ReviewFixEvent) (ReviewFixReduction, error) {
	if err := validateReviewFixState(current); err != nil {
		return ReviewFixReduction{}, err
	}
	if event == nil {
		return ReviewFixReduction{}, fmt.Errorf("review-fix event is required")
	}
	next := cloneReviewFixState(current)
	var effects []CommitProtocolEffect
	switch value := event.(type) {
	case ReserveReviewFix:
		if !current.Quiescent() {
			return ReviewFixReduction{}, fmt.Errorf("review-fix reservation is blocked by an in-flight fix")
		}
		if current.Used() >= current.maximum {
			return ReviewFixReduction{}, fmt.Errorf("review-fix budget is exhausted")
		}
		expectedOrdinal := current.Used() + 1
		if value.ordinal != expectedOrdinal || value.parent != current.Head() {
			return ReviewFixReduction{}, fmt.Errorf("review-fix reservation does not match the next ordinal and head")
		}
		key, err := reviewFixReservationKey(
			current.generation, current.protocol.digest, current.maximum, value.ordinal, value.parent,
		)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		next.fixes = append(next.fixes, ReviewFixStepState{
			ordinal: value.ordinal, parent: value.parent, phase: ReviewFixReserved, reservationKey: key,
		})
	case StageReviewFix:
		if current.Phase() != ReviewFixReserved || value.ordinal != current.Used() {
			return ReviewFixReduction{}, fmt.Errorf("review-fix intent requires the current durable reservation")
		}
		index := len(current.fixes) - 1
		pending := current.fixes[index]
		step, err := current.protocol.Step(value.ordinal)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		if err := value.inspection.Validate(step, pending.parent); err != nil {
			return ReviewFixReduction{}, err
		}
		body, err := step.message.ResolveBody(value.body)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		key, err := commitEffectIdempotencyKey(
			current.generation, current.protocol.digest, step.id, value.ordinal,
			pending.parent, value.inspection.stateDigest, body, 0,
		)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		updated := &next.fixes[index]
		updated.phase, updated.inspection, updated.body, updated.intentKey =
			ReviewFixAwaitingCommit, cloneStagedCommitInspection(value.inspection), body, key
		effect, err := createReviewFixCommitEffect(next, index)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		effects = []CommitProtocolEffect{effect}
	case RecordReviewFixCommit:
		if current.Phase() != ReviewFixAwaitingCommit || value.ordinal != current.Used() {
			return ReviewFixReduction{}, fmt.Errorf("review-fix commit evidence requires a pending intent")
		}
		index := len(current.fixes) - 1
		pending := current.fixes[index]
		step, err := current.protocol.Step(value.ordinal)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		if err := value.evidence.ValidateStep(step, current.generation, value.ordinal, pending.parent); err != nil {
			return ReviewFixReduction{}, err
		}
		if value.evidence.tree != pending.inspection.indexTree ||
			value.evidence.diff.digest != pending.inspection.diff.digest || value.evidence.body != pending.body {
			return ReviewFixReduction{}, fmt.Errorf("review-fix commit does not match its staged intent")
		}
		updated := &next.fixes[index]
		updated.commit = cloneCommitObjectEvidence(value.evidence)
		if len(step.checks) == 0 {
			updated.phase = ReviewFixComplete
			effect, err := completedReviewFixEffect(next, index)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			effects = []CommitProtocolEffect{effect}
		} else {
			updated.phase = ReviewFixAwaitingChecks
			effect, err := createReviewFixCheckEffect(next, index, 0)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			effects = []CommitProtocolEffect{effect}
		}
	case RecordReviewFixCheck:
		if current.Phase() != ReviewFixAwaitingChecks {
			return ReviewFixReduction{}, fmt.Errorf("review-fix check evidence requires a pending check")
		}
		index := len(current.fixes) - 1
		if current.checkingFix >= 0 {
			index = current.checkingFix
		}
		if value.ordinal != uint16(index+1) {
			return ReviewFixReduction{}, fmt.Errorf("review-fix check evidence does not match the pending fix")
		}
		step, err := current.protocol.Step(value.ordinal)
		if err != nil {
			return ReviewFixReduction{}, err
		}
		checkIndex := len(current.fixes[index].checks)
		if checkIndex >= len(step.checks) {
			return ReviewFixReduction{}, fmt.Errorf("review-fix %d has no pending check", value.ordinal)
		}
		if err := value.evidence.Validate(step.checks[checkIndex], current.fixes[index].commit); err != nil {
			return ReviewFixReduction{}, err
		}
		next.fixes[index].checks = append(next.fixes[index].checks, cloneOneCommitCheckEvidence(value.evidence))
		if current.checkingFix >= 0 {
			if len(next.fixes[index].checks) == len(step.checks) {
				next.fixes[index].phase = ReviewFixComplete
				effects = scheduleNextRebasedReviewFixCheck(&next, index+1)
			} else {
				effect, err := createReviewFixCheckEffect(next, index, checkIndex+1)
				if err != nil {
					return ReviewFixReduction{}, err
				}
				effects = []CommitProtocolEffect{effect}
			}
		} else if len(next.fixes[index].checks) == len(step.checks) {
			next.fixes[index].phase = ReviewFixComplete
			effect, err := completedReviewFixEffect(next, index)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			effects = []CommitProtocolEffect{effect}
		} else {
			effect, err := createReviewFixCheckEffect(next, index, checkIndex+1)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			effects = []CommitProtocolEffect{effect}
		}
	case RemapRebasedReviewFixes:
		if !current.Quiescent() || len(current.fixes) == 0 {
			return ReviewFixReduction{}, fmt.Errorf("review-fix rebase remapping requires a completed, quiescent chain")
		}
		if len(value.commits) != len(current.fixes) {
			return ReviewFixReduction{}, fmt.Errorf(
				"review-fix rebase remapping requires exactly %d commits", len(current.fixes),
			)
		}
		if value.base.Algorithm() != current.base.Algorithm() {
			return ReviewFixReduction{}, fmt.Errorf("review-fix rebase changes the repository object format")
		}
		parent := value.base
		seen := make(map[string]struct{}, len(value.commits))
		changed := value.base != current.base
		for index, evidence := range value.commits {
			ordinal := uint16(index + 1)
			step, err := current.protocol.Step(ordinal)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			if err := evidence.ValidateStep(step, current.generation, ordinal, parent); err != nil {
				return ReviewFixReduction{}, fmt.Errorf("rebased review fix %d: %w", ordinal, err)
			}
			previous := current.fixes[index]
			if evidence.diff.digest != previous.commit.diff.digest {
				return ReviewFixReduction{}, fmt.Errorf("rebased review fix %d changed its canonical diff", ordinal)
			}
			if evidence.subject != previous.commit.subject || evidence.body != previous.commit.body {
				return ReviewFixReduction{}, fmt.Errorf("rebased review fix %d changed its commit message", ordinal)
			}
			if _, exists := seen[evidence.commit.String()]; exists {
				return ReviewFixReduction{}, fmt.Errorf("rebased review-fix chain repeats commit %s", evidence.commit)
			}
			seen[evidence.commit.String()] = struct{}{}
			inspection, err := NewStagedCommitInspection(parent, evidence.tree, evidence.diff, nil, nil, nil)
			if err != nil {
				return ReviewFixReduction{}, fmt.Errorf("rebuild rebased review-fix intent %d: %w", ordinal, err)
			}
			reservation, err := reviewFixReservationKey(
				current.generation, current.protocol.digest, current.maximum, ordinal, parent,
			)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			intent, err := commitEffectIdempotencyKey(
				current.generation, current.protocol.digest, step.id, ordinal, parent,
				inspection.stateDigest, previous.body, 0,
			)
			if err != nil {
				return ReviewFixReduction{}, err
			}
			next.fixes[index] = ReviewFixStepState{
				ordinal: ordinal, parent: parent, phase: ReviewFixComplete,
				reservationKey: reservation, inspection: inspection, body: previous.body,
				intentKey: intent, commit: cloneCommitObjectEvidence(evidence),
			}
			changed = changed || evidence.commit != previous.commit.commit || evidence.tree != previous.commit.tree
			parent = evidence.commit
		}
		if !changed {
			return ReviewFixReduction{}, fmt.Errorf("review-fix rebase remapping did not change the base, commit, or tree sequence")
		}
		next.base = value.base
		next.rebaseEpoch++
		next.checkingFix = -1
		effects = scheduleNextRebasedReviewFixCheck(&next, 0)
	default:
		return ReviewFixReduction{}, fmt.Errorf("unsupported review-fix event %T", event)
	}
	if err := validateReviewFixState(next); err != nil {
		return ReviewFixReduction{}, fmt.Errorf("invalid review-fix transition: %w", err)
	}
	return ReviewFixReduction{state: cloneReviewFixState(next), effects: effects}, nil
}

func PendingReviewFixEffects(state ReviewFixState) ([]CommitProtocolEffect, error) {
	if err := validateReviewFixState(state); err != nil {
		return nil, err
	}
	if len(state.fixes) == 0 {
		return nil, nil
	}
	if state.checkingFix >= 0 {
		effect, err := createReviewFixCheckEffect(
			state, state.checkingFix, len(state.fixes[state.checkingFix].checks),
		)
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	}
	index := len(state.fixes) - 1
	switch state.fixes[index].phase {
	case ReviewFixAwaitingCommit:
		effect, err := createReviewFixCommitEffect(state, index)
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	case ReviewFixAwaitingChecks:
		effect, err := createReviewFixCheckEffect(state, index, len(state.fixes[index].checks))
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	case ReviewFixComplete:
		effect, err := completedReviewFixEffect(state, index)
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	default:
		return nil, nil
	}
}

func scheduleNextRebasedReviewFixCheck(state *ReviewFixState, start int) []CommitProtocolEffect {
	for index := start; index < len(state.fixes); index++ {
		step, err := state.protocol.Step(uint16(index + 1))
		if err == nil && len(step.checks) != 0 {
			state.checkingFix = index
			state.fixes[index].phase = ReviewFixAwaitingChecks
			effect, _ := createReviewFixCheckEffect(*state, index, 0)
			return []CommitProtocolEffect{effect}
		}
	}
	state.checkingFix = -1
	effect, _ := completedReviewFixEffect(*state, len(state.fixes)-1)
	return []CommitProtocolEffect{effect}
}

func createReviewFixCommitEffect(state ReviewFixState, index int) (CreateConfiguredCommitEffect, error) {
	if index < 0 || index >= len(state.fixes) {
		return CreateConfiguredCommitEffect{}, fmt.Errorf("review-fix commit effect index is invalid")
	}
	fix := state.fixes[index]
	step, err := state.protocol.Step(fix.ordinal)
	if err != nil {
		return CreateConfiguredCommitEffect{}, err
	}
	return CreateConfiguredCommitEffect{
		generation: state.generation, protocol: state.protocol.digest, step: step,
		ordinal: fix.ordinal, parent: fix.parent, body: fix.body,
		inspection: cloneStagedCommitInspection(fix.inspection), idempotencyKey: fix.intentKey,
	}, nil
}

func createReviewFixCheckEffect(state ReviewFixState, stepIndex, checkIndex int) (RunConfiguredCheckEffect, error) {
	if stepIndex < 0 || stepIndex >= len(state.fixes) {
		return RunConfiguredCheckEffect{}, fmt.Errorf("review-fix check effect index is invalid")
	}
	fix := state.fixes[stepIndex]
	step, err := state.protocol.Step(fix.ordinal)
	if err != nil {
		return RunConfiguredCheckEffect{}, err
	}
	if checkIndex < 0 || checkIndex >= len(step.checks) || fix.commit.evidence.IsZero() {
		return RunConfiguredCheckEffect{}, fmt.Errorf("review-fix check effect has no matching commit and check")
	}
	check := step.checks[checkIndex]
	key := reviewFixCheckIdempotencyKey(state, fix, check, uint16(checkIndex+1))
	return RunConfiguredCheckEffect{
		generation: state.generation, protocol: state.protocol.digest, step: step,
		stepOrdinal: fix.ordinal, check: cloneCommitChecks([]CommitCheck{check})[0],
		checkOrdinal: uint16(checkIndex + 1), commit: cloneCommitObjectEvidence(fix.commit),
		idempotencyKey: key,
	}, nil
}

func reviewFixCheckIdempotencyKey(
	state ReviewFixState,
	fix ReviewFixStepState,
	check CommitCheck,
	checkOrdinal uint16,
) Digest {
	return DigestBytes([]byte(fmt.Sprintf(
		"review_fix_check_effect_v2\ngeneration=%s\nprotocol=%s\nordinal=%d\ncheck=%s\ncheck_ordinal=%d\ncommit=%s\ntree=%s\ndiff=%s\nrunner=%s\nparser=%s\nrebase_epoch=%d\n",
		state.generation, state.protocol.digest, fix.ordinal, check.id, checkOrdinal,
		fix.commit.commit, fix.commit.tree, fix.commit.diff.digest, check.runner, check.parser, state.rebaseEpoch,
	)))
}

func completedReviewFixEffect(state ReviewFixState, index int) (ReviewFixCompletedEffect, error) {
	if index < 0 || index >= len(state.fixes) || state.fixes[index].phase != ReviewFixComplete {
		return ReviewFixCompletedEffect{}, fmt.Errorf("review-fix completion requires a completed fix")
	}
	fix := state.fixes[index]
	bindings := []byte(fmt.Sprintf(
		"review_fix_complete_v2\ngeneration=%s\nprotocol=%s\nordinal=%d\nreservation=%s\nintent=%s\ncommit=%s\nrebase_epoch=%d\n",
		state.generation, state.protocol.digest, fix.ordinal, fix.reservationKey, fix.intentKey, fix.commit.evidence,
		state.rebaseEpoch,
	))
	for checkIndex, check := range fix.checks {
		bindings = append(bindings, []byte(fmt.Sprintf("check_%d=%s\n", checkIndex+1, check.evidence))...)
	}
	return ReviewFixCompletedEffect{
		generation: state.generation, protocol: state.protocol.digest, ordinal: fix.ordinal,
		head: fix.commit.commit, evidence: DigestBytes(bindings),
	}, nil
}

func reviewFixReservationKey(
	generation, protocol Digest,
	maximum, ordinal uint16,
	parent GitObjectID,
) (Digest, error) {
	if generation.IsZero() || protocol.IsZero() || maximum == 0 || ordinal == 0 || parent.IsZero() {
		return Digest{}, fmt.Errorf("review-fix reservation bindings are incomplete")
	}
	return DigestBytes([]byte(fmt.Sprintf(
		"review_fix_reservation_v2\ngeneration=%s\nprotocol=%s\nmaximum=%d\nordinal=%d\nparent=%s\n",
		generation, protocol, maximum, ordinal, parent,
	))), nil
}

func validateReviewFixState(state ReviewFixState) error {
	if state.generation.IsZero() || state.base.IsZero() || state.protocol.digest.IsZero() ||
		state.maximum == 0 || len(state.fixes) > int(state.maximum) || state.checkingFix < -1 ||
		state.checkingFix >= len(state.fixes) {
		return fmt.Errorf("review-fix state requires generation, base, protocol, and positive budget")
	}
	if state.checkingFix >= 0 && state.rebaseEpoch == 0 {
		return fmt.Errorf("review-fix check revalidation requires a rebase epoch")
	}
	parent := state.base
	for index, fix := range state.fixes {
		ordinal := uint16(index + 1)
		if fix.ordinal != ordinal || fix.parent != parent || !fix.phase.valid() || fix.phase == ReviewFixReady {
			return fmt.Errorf("review-fix %d has inconsistent ordinal, parent, or phase", ordinal)
		}
		reservation, err := reviewFixReservationKey(
			state.generation, state.protocol.digest, state.maximum, ordinal, parent,
		)
		if err != nil || reservation != fix.reservationKey {
			return fmt.Errorf("review-fix %d reservation key does not match", ordinal)
		}
		step, err := state.protocol.Step(ordinal)
		if err != nil {
			return err
		}
		if index < len(state.fixes)-1 && fix.phase != ReviewFixComplete && state.checkingFix != index {
			return fmt.Errorf("review-fix %d is incomplete before a later reservation", ordinal)
		}
		switch fix.phase {
		case ReviewFixReserved:
			if !fix.inspection.stateDigest.IsZero() || fix.body != "" || !fix.intentKey.IsZero() ||
				!fix.commit.evidence.IsZero() || len(fix.checks) != 0 {
				return fmt.Errorf("reserved review-fix %d carries premature intent or evidence", ordinal)
			}
		case ReviewFixAwaitingCommit, ReviewFixAwaitingChecks, ReviewFixComplete:
			if state.checkingFix >= 0 {
				expectedPhase := ReviewFixComplete
				if index == state.checkingFix {
					expectedPhase = ReviewFixAwaitingChecks
				}
				if fix.phase != expectedPhase {
					return fmt.Errorf("review-fix %d has an invalid phase during rebase check revalidation", ordinal)
				}
			}
			if fix.inspection.stateDigest.IsZero() || fix.intentKey.IsZero() {
				return fmt.Errorf("review-fix %d is missing its durable intent", ordinal)
			}
			if err := fix.inspection.Validate(step, parent); err != nil {
				return fmt.Errorf("review-fix %d intent: %w", ordinal, err)
			}
			resolvedBody, err := step.message.ResolveBody(fix.body)
			if err != nil || resolvedBody != fix.body {
				return fmt.Errorf("review-fix %d body does not match protocol", ordinal)
			}
			intent, err := commitEffectIdempotencyKey(
				state.generation, state.protocol.digest, step.id, ordinal, parent,
				fix.inspection.stateDigest, fix.body, 0,
			)
			if err != nil || intent != fix.intentKey {
				return fmt.Errorf("review-fix %d intent key does not match", ordinal)
			}
			if fix.phase == ReviewFixAwaitingCommit {
				if !fix.commit.evidence.IsZero() || len(fix.checks) != 0 {
					return fmt.Errorf("review-fix %d has premature commit or check evidence", ordinal)
				}
				continue
			}
			if err := fix.commit.ValidateStep(step, state.generation, ordinal, parent); err != nil {
				return fmt.Errorf("review-fix %d commit: %w", ordinal, err)
			}
			if fix.commit.tree != fix.inspection.indexTree || fix.commit.diff.digest != fix.inspection.diff.digest ||
				fix.commit.body != fix.body {
				return fmt.Errorf("review-fix %d commit differs from its intent", ordinal)
			}
			if len(fix.checks) > len(step.checks) {
				return fmt.Errorf("review-fix %d has too many check records", ordinal)
			}
			for checkIndex, evidence := range fix.checks {
				if err := evidence.Validate(step.checks[checkIndex], fix.commit); err != nil {
					return fmt.Errorf("review-fix %d check %d: %w", ordinal, checkIndex+1, err)
				}
			}
			if fix.phase == ReviewFixAwaitingChecks && len(fix.checks) >= len(step.checks) {
				return fmt.Errorf("review-fix %d awaiting-checks phase has no pending check", ordinal)
			}
			if fix.phase == ReviewFixComplete && len(fix.checks) != len(step.checks) {
				if state.checkingFix < 0 || index < state.checkingFix ||
					(index == state.checkingFix && len(fix.checks) >= len(step.checks)) ||
					(index > state.checkingFix && len(fix.checks) != 0) {
					return fmt.Errorf("completed review-fix %d has incomplete checks", ordinal)
				}
			}
		}
		if !fix.commit.commit.IsZero() {
			parent = fix.commit.commit
		}
	}
	return nil
}

func cloneReviewFixState(state ReviewFixState) ReviewFixState {
	result := state
	result.protocol = *cloneReviewFixProtocol(&state.protocol)
	result.fixes = cloneReviewFixStepStates(state.fixes)
	return result
}

func cloneReviewFixStepStates(values []ReviewFixStepState) []ReviewFixStepState {
	result := append([]ReviewFixStepState(nil), values...)
	for index := range result {
		result[index].inspection = cloneStagedCommitInspection(result[index].inspection)
		result[index].commit = cloneCommitObjectEvidence(result[index].commit)
		result[index].checks = cloneCommitCheckEvidence(result[index].checks)
	}
	return result
}

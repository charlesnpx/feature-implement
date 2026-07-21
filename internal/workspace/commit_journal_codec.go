package workspace

import (
	"encoding/json"
	"fmt"
)

type commitChangePayloadWire struct {
	Kind      CommitChangeKind `json:"kind"`
	OldPath   string           `json:"old_path"`
	NewPath   string           `json:"new_path"`
	OldMode   GitFileMode      `json:"old_mode"`
	NewMode   GitFileMode      `json:"new_mode"`
	OldObject string           `json:"old_object"`
	NewObject string           `json:"new_object"`
}

type stagedCommitPayloadWire struct {
	Head        string                    `json:"head"`
	IndexTree   string                    `json:"index_tree"`
	Changes     []commitChangePayloadWire `json:"changes"`
	Unstaged    []string                  `json:"unstaged"`
	Untracked   []string                  `json:"untracked"`
	Conflicted  []string                  `json:"conflicted"`
	StateDigest string                    `json:"state_digest"`
}

type commitObjectEvidencePayloadWire struct {
	Generation string                    `json:"generation"`
	StepID     string                    `json:"step_id"`
	Ordinal    uint16                    `json:"ordinal"`
	Commit     string                    `json:"commit"`
	Parent     string                    `json:"parent"`
	Tree       string                    `json:"tree"`
	Subject    string                    `json:"subject"`
	Body       string                    `json:"body"`
	Changes    []commitChangePayloadWire `json:"changes"`
	PathPolicy string                    `json:"path_policy"`
	Evidence   string                    `json:"evidence_digest"`
}

type commitCheckEvidencePayloadWire struct {
	Generation  string           `json:"generation"`
	StepID      string           `json:"step_id"`
	CheckID     string           `json:"check_id"`
	Commit      string           `json:"commit"`
	Tree        string           `json:"tree"`
	Diff        string           `json:"diff"`
	Runner      string           `json:"runner"`
	Parser      CheckParserKind  `json:"parser"`
	Command     string           `json:"command"`
	Output      string           `json:"output"`
	Isolation   string           `json:"isolation"`
	Outcome     CheckOutcomeKind `json:"outcome"`
	Identities  []string         `json:"identities"`
	OutcomeHash string           `json:"outcome_digest"`
	Evidence    string           `json:"evidence_digest"`
}

type commitProtocolStartedPayloadWire struct {
	WorkspaceID string                  `json:"workspace_id"`
	Generation  string                  `json:"generation"`
	AttemptID   string                  `json:"attempt_id"`
	Base        string                  `json:"base"`
	Protocol    canonicalCommitProtocol `json:"protocol"`
	Digest      string                  `json:"protocol_digest"`
}

type commitStepIntendedPayloadWire struct {
	WorkspaceID    string                  `json:"workspace_id"`
	Generation     string                  `json:"generation"`
	AttemptID      string                  `json:"attempt_id"`
	ProtocolDigest string                  `json:"protocol_digest"`
	StepID         string                  `json:"step_id"`
	Ordinal        uint16                  `json:"ordinal"`
	Parent         string                  `json:"parent"`
	Inspection     stagedCommitPayloadWire `json:"inspection"`
	Body           string                  `json:"body"`
	RebaseEpoch    uint64                  `json:"rebase_epoch"`
	IdempotencyKey string                  `json:"idempotency_key"`
}

type commitStepRecordedPayloadWire struct {
	WorkspaceID    string                          `json:"workspace_id"`
	Generation     string                          `json:"generation"`
	AttemptID      string                          `json:"attempt_id"`
	ProtocolDigest string                          `json:"protocol_digest"`
	IntentKey      string                          `json:"intent_key"`
	Evidence       commitObjectEvidencePayloadWire `json:"evidence"`
}

type commitCheckRecordedPayloadWire struct {
	WorkspaceID    string                         `json:"workspace_id"`
	Generation     string                         `json:"generation"`
	AttemptID      string                         `json:"attempt_id"`
	ProtocolDigest string                         `json:"protocol_digest"`
	StepOrdinal    uint16                         `json:"step_ordinal"`
	CheckOrdinal   uint16                         `json:"check_ordinal"`
	IdempotencyKey string                         `json:"idempotency_key"`
	Evidence       commitCheckEvidencePayloadWire `json:"evidence"`
}

type commitProtocolRebasedPayloadWire struct {
	WorkspaceID    string                            `json:"workspace_id"`
	Generation     string                            `json:"generation"`
	AttemptID      string                            `json:"attempt_id"`
	ProtocolDigest string                            `json:"protocol_digest"`
	Base           string                            `json:"base"`
	Commits        []commitObjectEvidencePayloadWire `json:"commits"`
	MappingDigest  string                            `json:"mapping_digest"`
}

func marshalCommitJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case CommitProtocolStartedJournalEvent:
		value = commitProtocolStartedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), Base: event.base.String(),
			Protocol: canonicalizeCommitProtocol(event.protocol), Digest: event.protocol.digest.String(),
		}
	case CommitStepIntendedJournalEvent:
		value = commitStepIntendedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			ProtocolDigest: event.protocolDigest.String(), StepID: event.stepID.String(), Ordinal: event.ordinal,
			Parent: event.parent.String(), Inspection: stagedCommitToPayload(event.inspection), Body: event.body,
			RebaseEpoch: event.rebaseEpoch, IdempotencyKey: event.idempotencyKey.String(),
		}
	case CommitStepRecordedJournalEvent:
		value = commitStepRecordedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			ProtocolDigest: event.protocolDigest.String(), IntentKey: event.intentKey.String(),
			Evidence: commitObjectEvidenceToPayload(event.evidence),
		}
	case CommitCheckRecordedJournalEvent:
		value = commitCheckRecordedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			ProtocolDigest: event.protocolDigest.String(), StepOrdinal: event.stepOrdinal, CheckOrdinal: event.checkOrdinal,
			IdempotencyKey: event.idempotencyKey.String(), Evidence: commitCheckEvidenceToPayload(event.evidence),
		}
	case CommitProtocolRebasedJournalEvent:
		commits := make([]commitObjectEvidencePayloadWire, 0, len(event.commits))
		for _, commit := range event.commits {
			commits = append(commits, commitObjectEvidenceToPayload(commit))
		}
		value = commitProtocolRebasedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			ProtocolDigest: event.protocolDigest.String(), Base: event.base.String(), Commits: commits,
			MappingDigest: event.mappingDigest.String(),
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeCommitJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventCommitProtocolStarted:
		var wire commitProtocolStartedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode commit protocol start: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		base, err := ParseGitObjectID(wire.Base)
		if err != nil {
			return nil, true, err
		}
		protocol, err := commitProtocolFromCanonical(wire.Protocol)
		if err != nil {
			return nil, true, err
		}
		if protocol.digest.String() != wire.Digest {
			return nil, true, fmt.Errorf("commit protocol payload digest does not match rules")
		}
		event, err := NewCommitProtocolStartedJournalEvent(workspaceID, generation, attemptID, base, protocol)
		return event, true, err
	case JournalEventCommitStepIntended:
		var wire commitStepIntendedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode commit step intent: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		stepID, err := NewID(wire.StepID)
		if err != nil {
			return nil, true, err
		}
		parent, err := ParseGitObjectID(wire.Parent)
		if err != nil {
			return nil, true, err
		}
		inspection, err := stagedCommitFromPayload(wire.Inspection)
		if err != nil {
			return nil, true, err
		}
		event, err := NewCommitStepIntendedJournalEvent(
			workspaceID, generation, attemptID, protocol, stepID, wire.Ordinal, parent,
			inspection, wire.Body, wire.RebaseEpoch,
		)
		if err != nil {
			return nil, true, err
		}
		if event.idempotencyKey.String() != wire.IdempotencyKey {
			return nil, true, fmt.Errorf("commit step intent payload idempotency key does not match")
		}
		return event, true, nil
	case JournalEventCommitStepRecorded:
		var wire commitStepRecordedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode commit step record: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		intent, err := ParseDigest(wire.IntentKey)
		if err != nil {
			return nil, true, err
		}
		evidence, err := commitObjectEvidenceFromPayload(wire.Evidence)
		if err != nil {
			return nil, true, err
		}
		event, err := NewCommitStepRecordedJournalEvent(workspaceID, generation, attemptID, protocol, intent, evidence)
		return event, true, err
	case JournalEventCommitCheckRecorded:
		var wire commitCheckRecordedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode commit check record: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		key, err := ParseDigest(wire.IdempotencyKey)
		if err != nil {
			return nil, true, err
		}
		evidence, err := commitCheckEvidenceFromPayload(wire.Evidence)
		if err != nil {
			return nil, true, err
		}
		event, err := NewCommitCheckRecordedJournalEvent(
			workspaceID, generation, attemptID, protocol, wire.StepOrdinal, wire.CheckOrdinal, key, evidence,
		)
		return event, true, err
	case JournalEventCommitProtocolRebased:
		var wire commitProtocolRebasedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode commit protocol rebase: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		base, err := ParseGitObjectID(wire.Base)
		if err != nil {
			return nil, true, err
		}
		commits := make([]CommitObjectEvidence, 0, len(wire.Commits))
		for _, item := range wire.Commits {
			evidence, err := commitObjectEvidenceFromPayload(item)
			if err != nil {
				return nil, true, err
			}
			commits = append(commits, evidence)
		}
		event, err := NewCommitProtocolRebasedJournalEvent(workspaceID, generation, attemptID, protocol, base, commits)
		if err != nil {
			return nil, true, err
		}
		if event.mappingDigest.String() != wire.MappingDigest {
			return nil, true, fmt.Errorf("commit protocol rebase mapping digest does not match payload")
		}
		return event, true, nil
	default:
		return nil, false, nil
	}
}

func stagedCommitToPayload(inspection StagedCommitInspection) stagedCommitPayloadWire {
	return stagedCommitPayloadWire{
		Head: inspection.head.String(), IndexTree: inspection.indexTree.String(),
		Changes:  commitChangesToPayload(inspection.diff.changes),
		Unstaged: append([]string(nil), inspection.unstaged...), Untracked: append([]string(nil), inspection.untracked...),
		Conflicted: append([]string(nil), inspection.conflicted...), StateDigest: inspection.stateDigest.String(),
	}
}

func stagedCommitFromPayload(wire stagedCommitPayloadWire) (StagedCommitInspection, error) {
	head, err := ParseGitObjectID(wire.Head)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	tree, err := ParseGitObjectID(wire.IndexTree)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	diff, err := commitDiffFromPayload(wire.Changes)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	inspection, err := NewStagedCommitInspection(head, tree, diff, wire.Unstaged, wire.Untracked, wire.Conflicted)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	if inspection.stateDigest.String() != wire.StateDigest {
		return StagedCommitInspection{}, fmt.Errorf("staged commit payload digest does not match")
	}
	return inspection, nil
}

func commitObjectEvidenceToPayload(evidence CommitObjectEvidence) commitObjectEvidencePayloadWire {
	return commitObjectEvidencePayloadWire{
		Generation: evidence.generation.String(), StepID: evidence.stepID.String(), Ordinal: evidence.ordinal,
		Commit: evidence.commit.String(), Parent: evidence.parent.String(), Tree: evidence.tree.String(),
		Subject: evidence.subject, Body: evidence.body, Changes: commitChangesToPayload(evidence.diff.changes),
		PathPolicy: evidence.pathPolicy.String(), Evidence: evidence.evidence.String(),
	}
}

func commitObjectEvidenceFromPayload(wire commitObjectEvidencePayloadWire) (CommitObjectEvidence, error) {
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	stepID, err := NewID(wire.StepID)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	commit, err := ParseGitObjectID(wire.Commit)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	parent, err := ParseGitObjectID(wire.Parent)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	tree, err := ParseGitObjectID(wire.Tree)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	diff, err := commitDiffFromPayload(wire.Changes)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	pathPolicy, err := ParseDigest(wire.PathPolicy)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	evidence, err := NewCommitObjectEvidence(
		generation, stepID, wire.Ordinal, commit, parent, tree,
		wire.Subject, wire.Body, diff, pathPolicy,
	)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	if evidence.evidence.String() != wire.Evidence {
		return CommitObjectEvidence{}, fmt.Errorf("commit evidence payload digest does not match")
	}
	return evidence, nil
}

func commitCheckEvidenceToPayload(evidence CommitCheckEvidence) commitCheckEvidencePayloadWire {
	return commitCheckEvidencePayloadWire{
		Generation: evidence.generation.String(), StepID: evidence.stepID.String(), CheckID: evidence.checkID.String(),
		Commit: evidence.commit.String(), Tree: evidence.tree.String(), Diff: evidence.diff.String(),
		Runner: evidence.runner.String(), Parser: evidence.parser, Command: evidence.command.String(),
		Output: evidence.output.String(), Isolation: evidence.isolation.String(), Outcome: evidence.outcome.kind,
		Identities: evidence.outcome.Identities(), OutcomeHash: evidence.outcome.digest.String(), Evidence: evidence.evidence.String(),
	}
}

func commitCheckEvidenceFromPayload(wire commitCheckEvidencePayloadWire) (CommitCheckEvidence, error) {
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	stepID, err := NewID(wire.StepID)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	checkID, err := NewID(wire.CheckID)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	commit, err := ParseGitObjectID(wire.Commit)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	tree, err := ParseGitObjectID(wire.Tree)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	diff, err := ParseDigest(wire.Diff)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	runner, err := NewID(wire.Runner)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	command, err := ParseDigest(wire.Command)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	output, err := ParseDigest(wire.Output)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	isolation, err := ParseDigest(wire.Isolation)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	outcome, err := NewParsedCheckOutcome(wire.Outcome, wire.Identities)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	if outcome.digest.String() != wire.OutcomeHash {
		return CommitCheckEvidence{}, fmt.Errorf("check outcome payload digest does not match")
	}
	evidenceDigest, err := ParseDigest(wire.Evidence)
	if err != nil {
		return CommitCheckEvidence{}, err
	}
	evidence := CommitCheckEvidence{
		generation: generation, stepID: stepID, checkID: checkID,
		commit: commit, tree: tree, diff: diff, runner: runner, parser: wire.Parser,
		command: command, output: output, isolation: isolation, outcome: outcome, evidence: evidenceDigest,
	}
	content, err := canonicalCommitCheckEvidence(evidence)
	if err != nil || DigestBytes(content) != evidenceDigest {
		return CommitCheckEvidence{}, fmt.Errorf("check evidence payload digest does not match")
	}
	return evidence, nil
}

func commitChangesToPayload(changes []CommitPathChange) []commitChangePayloadWire {
	result := make([]commitChangePayloadWire, 0, len(changes))
	for _, change := range changes {
		result = append(result, commitChangePayloadWire{
			Kind: change.kind, OldPath: change.oldPath, NewPath: change.newPath,
			OldMode: change.oldMode, NewMode: change.newMode,
			OldObject: change.oldObject.String(), NewObject: change.newObject.String(),
		})
	}
	return result
}

func commitDiffFromPayload(wires []commitChangePayloadWire) (CommitDiff, error) {
	changes := make([]CommitPathChange, 0, len(wires))
	for _, wire := range wires {
		oldObject, err := parseOptionalGitObjectID(wire.OldObject)
		if err != nil {
			return CommitDiff{}, err
		}
		newObject, err := parseOptionalGitObjectID(wire.NewObject)
		if err != nil {
			return CommitDiff{}, err
		}
		change, err := NewCommitPathChange(
			wire.Kind, wire.OldPath, wire.NewPath, wire.OldMode, wire.NewMode, oldObject, newObject,
		)
		if err != nil {
			return CommitDiff{}, err
		}
		changes = append(changes, change)
	}
	return NewCommitDiff(changes)
}

func parseOptionalGitObjectID(value string) (GitObjectID, error) {
	if value == "" {
		return GitObjectID{}, nil
	}
	return ParseGitObjectID(value)
}

func commitProtocolFromCanonical(wire canonicalCommitProtocol) (CommitProtocol, error) {
	steps := make([]CommitStep, 0, len(wire.Steps))
	for _, item := range wire.Steps {
		id, err := NewID(item.ID)
		if err != nil {
			return CommitProtocol{}, err
		}
		var exactBody *string
		if item.BodyPolicy == CommitBodyExact {
			value := item.ExactBody
			exactBody = &value
		} else if item.ExactBody != "" {
			return CommitProtocol{}, fmt.Errorf("non-exact commit step carries exact_body")
		}
		message, err := NewCommitMessagePolicy(item.Subject, item.BodyPolicy, exactBody)
		if err != nil {
			return CommitProtocol{}, err
		}
		paths, err := NewCommitPathPolicy(item.AllowedPaths, item.FrozenPaths)
		if err != nil {
			return CommitProtocol{}, err
		}
		checks := make([]CommitCheck, 0, len(item.Checks))
		for _, checkWire := range item.Checks {
			checkID, err := NewID(checkWire.ID)
			if err != nil {
				return CommitProtocol{}, err
			}
			runner, err := NewID(checkWire.Runner)
			if err != nil {
				return CommitProtocol{}, err
			}
			command, err := NewArgv(checkWire.Command...)
			if err != nil {
				return CommitProtocol{}, err
			}
			expectation, err := NewCheckExpectation(checkWire.Expectation, checkWire.FailureIDs)
			if err != nil {
				return CommitProtocol{}, err
			}
			check, err := NewCommitCheck(checkID, runner, checkWire.Parser, command, expectation)
			if err != nil {
				return CommitProtocol{}, err
			}
			checks = append(checks, check)
		}
		step, err := NewCommitStep(id, message, paths, checks)
		if err != nil {
			return CommitProtocol{}, err
		}
		steps = append(steps, step)
	}
	return NewCommitProtocol(steps)
}

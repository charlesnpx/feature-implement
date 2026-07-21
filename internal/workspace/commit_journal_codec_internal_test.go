package workspace

import (
	"fmt"
	"strings"
	"testing"
)

func TestCommitProtocolJournalCodecReplaysProgrammaticNilFrozenPaths(t *testing.T) {
	nilPolicy, err := NewCommitPathPolicy([]string{"src/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyPolicy, err := NewCommitPathPolicy([]string{"src/**"}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if nilPolicy.digest != emptyPolicy.digest {
		t.Fatalf("nil and empty frozen paths have different digests: %s != %s", nilPolicy.digest, emptyPolicy.digest)
	}
	message, err := NewCommitMessagePolicy("Implement protocol", CommitBodyForbidden, nil)
	if err != nil {
		t.Fatal(err)
	}
	step, err := NewCommitStep(MustID("implementation"), message, nilPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := NewCommitProtocol([]CommitStep{step})
	if err != nil {
		t.Fatal(err)
	}
	base, err := ParseGitObjectID("sha1:1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewCommitProtocolStartedJournalEvent(
		MustID("workspace"), DigestBytes([]byte("generation")), MustID("attempt"), base, protocol,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalCommitJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf("marshal programmatic protocol: supported=%v err=%v", supported, err)
	}
	decoded, supported, err := decodeCommitJournalEvent(JournalEventCommitProtocolStarted, payload)
	if err != nil || !supported {
		t.Fatalf("decode programmatic protocol: supported=%v err=%v", supported, err)
	}
	started, ok := decoded.(CommitProtocolStartedJournalEvent)
	if !ok {
		t.Fatalf("decoded event type = %T", decoded)
	}
	state, err := NewCommitProtocolState(started.generation, started.base, started.protocol)
	if err != nil {
		t.Fatalf("replay decoded protocol start: %v", err)
	}
	paths := state.Protocol().Steps()[0].Paths()
	if state.ProtocolDigest() != protocol.digest || paths.digest != nilPolicy.digest || len(paths.frozen) != 0 {
		t.Fatalf("replayed protocol=%s paths=%s frozen=%v", state.ProtocolDigest(), paths.digest, paths.frozen)
	}
}

func TestCommitProtocolValidationEnforcesJournalRecordBoundary(t *testing.T) {
	build := func(stepCount int) error {
		paths, err := NewCommitPathPolicy([]string{"src/**"}, nil)
		if err != nil {
			return err
		}
		steps := make([]CommitStep, 0, stepCount)
		for index := 0; index < stepCount; index++ {
			body := strings.Repeat(string(rune('a'+index)), maxCommitBodyBytes)
			message, err := NewCommitMessagePolicy(
				fmt.Sprintf("Implement protocol part %d", index+1), CommitBodyExact, &body,
			)
			if err != nil {
				return err
			}
			step, err := NewCommitStep(MustID(fmt.Sprintf("step-%d", index+1)), message, paths, nil)
			if err != nil {
				return err
			}
			steps = append(steps, step)
		}
		_, err = NewCommitProtocol(steps)
		return err
	}
	if err := build(3); err != nil {
		t.Fatalf("protocol below record boundary: %v", err)
	}
	if err := build(4); err == nil || !strings.Contains(err.Error(), "journal footprint") {
		t.Fatalf("protocol above record boundary error = %v", err)
	}
}

func TestCommitDiffValidationEnforcesJournalRecordBoundary(t *testing.T) {
	build := func(changeCount int) error {
		object, err := ParseGitObjectID("sha1:1111111111111111111111111111111111111111")
		if err != nil {
			return err
		}
		changes := make([]CommitPathChange, 0, changeCount)
		for index := 0; index < changeCount; index++ {
			pathValue := fmt.Sprintf("src/%04d-%s.go", index, strings.Repeat("a", 2048))
			change, err := NewCommitPathChange(
				CommitChangeAdded, "", pathValue, GitModeAbsent, GitModeRegular, GitObjectID{}, object,
			)
			if err != nil {
				return err
			}
			changes = append(changes, change)
		}
		_, err = NewCommitDiff(changes)
		return err
	}
	if err := build(180); err != nil {
		t.Fatalf("diff below record boundary: %v", err)
	}
	if err := build(260); err == nil || !strings.Contains(err.Error(), "durable journal footprint") {
		t.Fatalf("diff above record boundary error = %v", err)
	}
}

func TestCommitReducerReservesFutureRebaseJournalFootprint(t *testing.T) {
	paths, err := NewCommitPathPolicy([]string{"src/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]CommitStep, 0, 4)
	for index := 0; index < 4; index++ {
		message, err := NewCommitMessagePolicy(
			fmt.Sprintf("Implement protocol part %d", index+1), CommitBodyOptional, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		step, err := NewCommitStep(MustID(fmt.Sprintf("step-%d", index+1)), message, paths, nil)
		if err != nil {
			t.Fatal(err)
		}
		steps = append(steps, step)
	}
	protocol, err := NewCommitProtocol(steps)
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewCommitProtocolState(
		DigestBytes([]byte("generation")), journalBoundObject(t, 1), protocol,
	)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("b", maxCommitBodyBytes)
	for index, step := range steps {
		parent := state.Head()
		tree := journalBoundObject(t, 10+index*3)
		commit := journalBoundObject(t, 11+index*3)
		changed := journalBoundObject(t, 12+index*3)
		change, err := NewCommitPathChange(
			CommitChangeAdded, "", fmt.Sprintf("src/part-%d.go", index+1),
			GitModeAbsent, GitModeRegular, GitObjectID{}, changed,
		)
		if err != nil {
			t.Fatal(err)
		}
		diff, err := NewCommitDiff([]CommitPathChange{change})
		if err != nil {
			t.Fatal(err)
		}
		inspection, err := NewStagedCommitInspection(parent, tree, diff, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		stage, err := NewStageCommitStep(inspection, body)
		if err != nil {
			t.Fatal(err)
		}
		reduction, err := ReduceCommitProtocol(state, stage)
		if index == 3 {
			if err == nil || !strings.Contains(err.Error(), "durable rebase footprint") {
				t.Fatalf("fourth staged step footprint error = %v", err)
			}
			if state.Phase() != CommitProtocolReady || len(state.CompletedSteps()) != 3 {
				t.Fatalf("rejected footprint mutated state: %#v", state)
			}
			break
		}
		if err != nil {
			t.Fatalf("stage %d below cumulative boundary: %v", index+1, err)
		}
		evidence, err := NewCommitObjectEvidence(
			state.generation, step.id, uint16(index+1), commit, parent, tree,
			step.message.subject, body, diff, step.paths.digest,
		)
		if err != nil {
			t.Fatal(err)
		}
		record, err := NewRecordCommitStep(evidence)
		if err != nil {
			t.Fatal(err)
		}
		reduction, err = ReduceCommitProtocol(reduction.State(), record)
		if err != nil {
			t.Fatal(err)
		}
		state = reduction.State()
	}
}

func journalBoundObject(t *testing.T, value int) GitObjectID {
	t.Helper()
	object, err := ParseGitObjectID(fmt.Sprintf("sha1:%040x", value))
	if err != nil {
		t.Fatal(err)
	}
	return object
}

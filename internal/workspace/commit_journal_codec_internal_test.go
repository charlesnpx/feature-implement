package workspace

import "testing"

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

package workspace_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestImmutableValueConstructorsAndAccessorsDefendCopies(t *testing.T) {
	identifier, err := workspace.NewID("  stable-id  ")
	if err != nil || identifier.String() != "stable-id" {
		t.Fatalf("normalized id = %q, %v", identifier, err)
	}
	if _, err := workspace.NewID("Not Stable"); err == nil {
		t.Fatal("invalid identifier accepted")
	}

	zeroDigest, err := workspace.ParseDigest("sha256:" + strings.Repeat("0", 64))
	if err != nil || zeroDigest.IsZero() {
		t.Fatalf("algorithm-qualified all-zero digest must remain a valid value: %v", err)
	}
	digestBytes := zeroDigest.Bytes()
	digestBytes[0] = 1
	if zeroDigest.String() != "sha256:"+strings.Repeat("0", 64) {
		t.Fatal("digest bytes accessor leaked mutable storage")
	}
	object, err := workspace.ParseGitObjectID("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	objectBytes := object.Bytes()
	objectBytes[0] = 0
	if object.String() != "sha256:"+strings.Repeat("a", 64) {
		t.Fatal("Git object bytes accessor leaked mutable storage")
	}

	arguments := []string{"go", "test", "./..."}
	argv, err := workspace.NewArgv(arguments...)
	if err != nil {
		t.Fatal(err)
	}
	arguments[0] = "sh"
	returnedArguments := argv.Values()
	returnedArguments[1] = "env"
	if got := argv.Values(); !reflect.DeepEqual(got, []string{"go", "test", "./..."}) {
		t.Fatalf("argv alias escaped: %#v", got)
	}
	variable, err := workspace.NewEnvironmentVariable("GIT_CONFIG_NOSYSTEM", "1")
	if err != nil {
		t.Fatal(err)
	}
	environment := []workspace.EnvironmentVariable{variable}
	command, err := workspace.NewCommand(argv, "/repo", environment, workspace.ReplayAfterVerifiedNoEffect)
	if err != nil {
		t.Fatal(err)
	}
	environment[0] = workspace.EnvironmentVariable{}
	returnedEnvironment := command.Environment()
	returnedEnvironment[0] = workspace.EnvironmentVariable{}
	if got := command.Environment()[0].Name(); got != "GIT_CONFIG_NOSYSTEM" {
		t.Fatalf("command environment alias escaped: %q", got)
	}
	commandArgv := command.Argv().Values()
	commandArgv[0] = "sh"
	if got := command.Argv().Values()[0]; got != "go" {
		t.Fatalf("nested command argv alias escaped: %q", got)
	}

	item, err := workspace.NewEvidenceItem(workspace.MustID("head"), "sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	items := []workspace.EvidenceItem{item}
	evidence, err := workspace.NewEvidence(workspace.MustID("test"), workspace.DigestBytes([]byte("evidence")), items)
	if err != nil {
		t.Fatal(err)
	}
	items[0] = workspace.EvidenceItem{}
	returnedItems := evidence.Items()
	returnedItems[0] = workspace.EvidenceItem{}
	if got := evidence.Items()[0].Name().String(); got != "head" {
		t.Fatalf("evidence item alias escaped: %q", got)
	}

	repository, err := workspace.NewRepositoryIdentity("https://example.invalid/repository.git")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.NewControlPlaneBinding(workspace.ControlPlaneBindingOptions{
		Kind: workspace.ControlPlaneReceiptReconciliation, WorkspaceID: workspace.MustID("workspace-one"),
		Generation: workspace.DigestBytes([]byte("generation")), RequestDigest: workspace.DigestBytes([]byte("payload")),
		Repository: repository, Remote: "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := workspace.NewControlPlaneEnvelopeV2(
		binding, workspace.MustID("owner-key"), "nonce-1", time.Unix(2_000_000_000, 0), workspace.MustID("coordinator"),
	)
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	signature[0], signature[1], signature[2] = 1, 2, 3
	receipt, err := workspace.NewControlPlaneReceiptV2(envelope, signature)
	if err != nil {
		t.Fatal(err)
	}
	signature[0] = 9
	returnedSignature := receipt.Signature()
	returnedSignature[1] = 9
	wantSignature := make([]byte, 64)
	wantSignature[0], wantSignature[1], wantSignature[2] = 1, 2, 3
	if got := receipt.Signature(); !reflect.DeepEqual(got, wantSignature) {
		t.Fatalf("receipt signature alias escaped: %#v", got)
	}
}

func TestPureReducerIsDeterministicAndReturnsClosedEffects(t *testing.T) {
	generation := workspace.DigestBytes([]byte("generation"))
	evidence, err := workspace.NewEvidence(workspace.MustID("test"), workspace.DigestBytes([]byte("evidence")), nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func() workspace.ReducerState {
		state := workspace.InitialReducerState()
		activate, err := workspace.NewActivateDefinition(generation)
		if err != nil {
			t.Fatal(err)
		}
		reduction, err := workspace.Reduce(state, activate)
		if err != nil {
			t.Fatal(err)
		}
		if len(reduction.Effects()) != 1 || len(reduction.Directives()) != 0 {
			t.Fatalf("activation outputs = %#v %#v", reduction.Effects(), reduction.Directives())
		}
		if _, ok := reduction.Effects()[0].(workspace.PersistProjectionEffect); !ok {
			t.Fatalf("unexpected activation effect %T", reduction.Effects()[0])
		}
		state = reduction.State()

		pause, err := workspace.NewPauseDefinition(workspace.MustID("owner-gate"), []workspace.Evidence{evidence})
		if err != nil {
			t.Fatal(err)
		}
		reduction, err = workspace.Reduce(state, pause)
		if err != nil {
			t.Fatal(err)
		}
		if len(reduction.Directives()) != 1 {
			t.Fatalf("pause directives = %#v", reduction.Directives())
		}
		directive, ok := reduction.Directives()[0].(workspace.PausedDirective)
		if !ok || directive.Reason().String() != "owner-gate" || directive.Generation() != generation {
			t.Fatalf("pause directive = %#v", reduction.Directives()[0])
		}
		state = reduction.State()

		resume, err := workspace.NewResumeDefinition(generation)
		if err != nil {
			t.Fatal(err)
		}
		reduction, err = workspace.Reduce(state, resume)
		if err != nil {
			t.Fatal(err)
		}
		state = reduction.State()
		complete, err := workspace.NewCompleteDefinition([]workspace.Evidence{evidence})
		if err != nil {
			t.Fatal(err)
		}
		reduction, err = workspace.Reduce(state, complete)
		if err != nil {
			t.Fatal(err)
		}
		return reduction.State()
	}

	first := run()
	second := run()
	if first.Phase() != workspace.CoreCompleted || first.Revision() != 4 || first.Generation() != generation || len(first.Evidence()) != 2 {
		t.Fatalf("final state = %#v", first)
	}
	if first.Phase() != second.Phase() || first.Revision() != second.Revision() || first.Generation() != second.Generation() || !reflect.DeepEqual(first.Evidence(), second.Evidence()) {
		t.Fatalf("replay diverged: %#v != %#v", first, second)
	}
	evidenceCopy := first.Evidence()
	evidenceCopy[0] = workspace.Evidence{}
	if first.Evidence()[0].Kind().String() != "test" {
		t.Fatal("reducer state evidence accessor leaked mutable storage")
	}
}

func TestReducerRejectsInvalidTransitionsAndStaleGeneration(t *testing.T) {
	generation := workspace.DigestBytes([]byte("generation"))
	otherGeneration := workspace.DigestBytes([]byte("other"))
	pause, _ := workspace.NewPauseDefinition(workspace.MustID("gate"), nil)
	if _, err := workspace.Reduce(workspace.InitialReducerState(), pause); err == nil {
		t.Fatal("pause from empty state succeeded")
	}
	activate, _ := workspace.NewActivateDefinition(generation)
	active, err := workspace.Reduce(workspace.InitialReducerState(), activate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Reduce(active.State(), activate); err == nil {
		t.Fatal("second activation succeeded")
	}
	paused, err := workspace.Reduce(active.State(), pause)
	if err != nil {
		t.Fatal(err)
	}
	staleResume, _ := workspace.NewResumeDefinition(otherGeneration)
	if _, err := workspace.Reduce(paused.State(), staleResume); err == nil || !strings.Contains(err.Error(), "active generation") {
		t.Fatalf("stale resume error = %v", err)
	}
	complete, err := workspace.NewCompleteDefinition(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Reduce(paused.State(), complete); err == nil {
		t.Fatal("completion from paused state succeeded")
	}
}

func TestReducerEventsRejectMalformedEvidence(t *testing.T) {
	malformed := []workspace.Evidence{{}}
	if _, err := workspace.NewPauseDefinition(workspace.MustID("gate"), malformed); err == nil || !strings.Contains(err.Error(), "kind and digest") {
		t.Fatalf("malformed pause evidence error = %v", err)
	}
	if _, err := workspace.NewCompleteDefinition(malformed); err == nil || !strings.Contains(err.Error(), "kind and digest") {
		t.Fatalf("malformed completion evidence error = %v", err)
	}
}

func TestTypedPortsRejectUnrootedPathsAndShellLikeInvalidValues(t *testing.T) {
	if _, err := workspace.NewRootedPath("relative", "file.txt"); err == nil {
		t.Fatal("relative root accepted")
	}
	if _, err := workspace.NewRootedPath("/repo", "../escape"); err == nil {
		t.Fatal("escaping relative path accepted")
	}
	for _, relative := range []string{"C:/escape", "C:escape", "logs/output:stream", "CON", "aux.txt", "LPT1/output.txt", "dir./file.txt"} {
		if _, err := workspace.NewRootedPath("/repo", relative); err == nil {
			t.Fatalf("non-portable relative path %q accepted", relative)
		}
	}
	rooted, err := workspace.NewRootedPath("/repo/./root", "config/../config/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if rooted.Root() != "/repo/root" || rooted.Relative() != "config/policy.yaml" {
		t.Fatalf("rooted path = %s %s", rooted.Root(), rooted.Relative())
	}
	if _, err := workspace.NewArgv("bad\x00executable"); err == nil {
		t.Fatal("NUL argv accepted")
	}
	if _, err := workspace.NewCommand(workspace.Argv{}, "", nil, workspace.ReplayNever); err == nil {
		t.Fatal("empty typed command accepted")
	}
}

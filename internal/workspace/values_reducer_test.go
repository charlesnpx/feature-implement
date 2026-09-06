package workspace_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestImmutableValueConstructorsAndAccessorsDefendCopies(t *testing.T) {
	t.Parallel()

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

}

func TestTypedPortsRejectUnrootedPathsAndShellLikeInvalidValues(t *testing.T) {
	t.Parallel()

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

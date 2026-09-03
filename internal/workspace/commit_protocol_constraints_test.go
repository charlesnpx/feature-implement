package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestDeclarativeCommitProtocolSchemaKeepsOnlyFinalHistoryConstraints(t *testing.T) {
	t.Parallel()

	configured := declarativeCommitProtocolYAML(true)
	config, err := workspace.DecodeExecutionConfig([]byte(configured))
	if err != nil {
		t.Fatalf("DecodeExecutionConfig: %v", err)
	}
	units := config.MergeUnits()
	if len(units) != 1 {
		t.Fatalf("merge units = %d", len(units))
	}
	protocol, exists := units[0].CommitProtocol()
	if !exists || len(protocol.Steps()) != 2 || protocol.Digest().IsZero() {
		t.Fatalf("commit protocol = %#v configured=%t", protocol, exists)
	}
	first := protocol.Steps()[0]
	if first.ID().String() != "first" || first.Message().Subject() != "Add first checkpoint" ||
		first.Message().BodyPolicy() != workspace.CommitBodyForbidden || len(first.Checks()) != 1 ||
		first.Checks()[0].Command().Values()[0] != "go" {
		t.Fatalf("first declarative checkpoint = %#v", first)
	}

	unconfigured, err := workspace.DecodeExecutionConfig([]byte(declarativeCommitProtocolYAML(false)))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := unconfigured.MergeUnits()[0].CommitProtocol(); exists {
		t.Fatal("absent commit protocol became configured")
	}

	for _, test := range []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name:    "missing frozen paths",
			source:  strings.Replace(configured, "          frozen_paths: []\n", "", 1),
			wantErr: "must explicitly define allowed_paths, frozen_paths, and checks",
		},
		{
			name:    "runner mismatch",
			source:  strings.Replace(configured, "              runner: codex\n", "              runner: other\n", 1),
			wantErr: "does not match profile runner",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.DecodeExecutionConfig([]byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("DecodeExecutionConfig error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCommitPathPolicyValidatesBothPathsIncludingFrozenAndHiddenPaths(t *testing.T) {
	t.Parallel()

	policy, err := workspace.NewCommitPathPolicy(
		[]string{"src/**", "modules/**", ".github/**", ".gitignore"},
		[]string{"src/frozen.go", "modules/vendor/**"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		oldPath string
		newPath string
		wantErr string
	}{
		{"rename or copy allowed", "src/old.go", "src/new.go", ""},
		{"rename or copy into frozen path", "src/old.go", "src/frozen.go", "frozen"},
		{"rename or copy from frozen path", "src/frozen.go", "src/new.go", "frozen"},
		{"rename or copy from outside allowed paths", "private/old.go", "src/new.go", "outside"},
		{"old path only", "src/delete.go", "", ""},
		{"new path only", "", "src/add.go", ""},
		{"hidden subtree explicitly allowed", "", ".github/workflows/test.yml", ""},
		{"hidden file explicitly allowed", "", ".gitignore", ""},
		{"unlisted hidden path rejected", "", ".private", "outside"},
		{"frozen module path rejected", "", "modules/vendor/tool", "frozen"},
	} {
		t.Run(test.name, func(t *testing.T) {
			change, err := workspace.NewCommitPathChange(test.oldPath, test.newPath)
			if err != nil {
				t.Fatalf("NewCommitPathChange: %v", err)
			}
			err = policy.ValidateChange(change)
			if test.wantErr == "" && err != nil {
				t.Fatalf("ValidateChange: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("ValidateChange error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCommitDiffDetectsUnchangedSourceCopiesForPathPolicy(t *testing.T) {
	t.Parallel()

	repository, _, _ := newProtocolRepository(t)
	source := filepath.Join(repository, "src", "protocol.go")
	target := filepath.Join(repository, "src", "copied.go")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "--", "src/copied.go")
	runGitSetup(t, repository, "commit", "-m", "Copy protocol source")
	head := parseGitHead(t, repository)

	inspection, err := workspace.DefaultLocalCommitGitAdapter().InspectCommit(
		context.Background(), repository, head,
	)
	if err != nil {
		t.Fatal(err)
	}
	changes := inspection.Diff().Changes()
	if len(changes) != 1 || changes[0].OldPath() != "src/protocol.go" || changes[0].NewPath() != "src/copied.go" {
		t.Fatalf("copy path evidence = %#v", changes)
	}
	policy, err := workspace.NewCommitPathPolicy([]string{"src/**"}, []string{"src/protocol.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.ValidateChange(changes[0]); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("unchanged-source copy escaped frozen policy: %v", err)
	}
}

func TestFinalHistoryVerifierInspectsRealDetachedHistory(t *testing.T) {
	t.Parallel()

	repository, _, base := newProtocolRepository(t)
	for _, commit := range []struct {
		path    string
		subject string
		content []byte
	}{
		{path: "src/first.go", subject: "Add first checkpoint", content: []byte("redwood comet\n")},
		{path: "src/second.go", subject: "Add second checkpoint", content: []byte("quartz lantern\n")},
	} {
		if err := os.WriteFile(filepath.Join(repository, commit.path), commit.content, 0o644); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", "--", commit.path)
		runGitSetup(t, repository, "commit", "-m", commit.subject)
	}
	head := parseGitHead(t, repository)
	newStep := func(id, subject, allowed string) workspace.CommitStep {
		t.Helper()
		message, err := workspace.NewCommitMessagePolicy(subject, workspace.CommitBodyForbidden, nil)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := workspace.NewCommitPathPolicy([]string{allowed}, []string{})
		if err != nil {
			t.Fatal(err)
		}
		step, err := workspace.NewCommitStep(workspace.MustID(id), message, paths, nil)
		if err != nil {
			t.Fatal(err)
		}
		return step
	}
	protocol, err := workspace.NewCommitProtocol([]workspace.CommitStep{
		newStep("first", "Add first checkpoint", "src/first.go"),
		newStep("second", "Add second checkpoint", "src/second.go"),
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := workspace.NewFinalHistoryVerifier(workspace.DefaultLocalCommitGitAdapter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), protocol, repository, base, head); err != nil {
		t.Fatalf("Verify real final history: %v", err)
	}
}

func declarativeCommitProtocolYAML(withProtocol bool) string {
	protocol := ""
	if withProtocol {
		protocol = `
    commit_protocol:
      steps:
        - id: first
          subject: Add first checkpoint
          body_policy: forbidden
          allowed_paths:
            - src/first.go
          frozen_paths: []
          checks:
            - id: first-check
              runner: codex
              command:
                - go
                - test
                - ./...
        - id: second
          subject: Add second checkpoint
          body_policy: required
          allowed_paths:
            - src/second.go
          frozen_paths: []
          checks: []`
	}
	return `schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 3
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      checkpoint: none
      escalation: allowed
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3` + protocol + "\n"
}

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
			name: "legacy parser",
			source: strings.Replace(
				configured,
				"              command:\n",
				"              parser: go-test-json\n              command:\n",
				1,
			),
			wantErr: "field parser not found",
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

func TestCommitPathPolicyCoversAllFinalHistoryChangeKinds(t *testing.T) {
	t.Parallel()

	policy, err := workspace.NewCommitPathPolicy(
		[]string{"src/**", "modules/**", ".github/**", ".gitignore"},
		[]string{"src/frozen.go", "modules/vendor/**"},
	)
	if err != nil {
		t.Fatal(err)
	}
	oldObject, newObject := mustGitObject(t, '1'), mustGitObject(t, '2')
	for _, test := range []struct {
		name    string
		kind    workspace.CommitChangeKind
		oldPath string
		newPath string
		oldMode workspace.GitFileMode
		newMode workspace.GitFileMode
		old     workspace.GitObjectID
		new     workspace.GitObjectID
		wantErr string
	}{
		{"rename allowed", workspace.CommitChangeRenamed, "src/old.go", "src/new.go", workspace.GitModeRegular, workspace.GitModeRegular, oldObject, newObject, ""},
		{"rename into frozen", workspace.CommitChangeRenamed, "src/old.go", "src/frozen.go", workspace.GitModeRegular, workspace.GitModeRegular, oldObject, newObject, "frozen"},
		{"rename from outside", workspace.CommitChangeRenamed, "private/old.go", "src/new.go", workspace.GitModeRegular, workspace.GitModeRegular, oldObject, newObject, "outside"},
		{"delete allowed", workspace.CommitChangeDeleted, "src/delete.go", "", workspace.GitModeRegular, workspace.GitModeAbsent, oldObject, workspace.GitObjectID{}, ""},
		{"mode allowed", workspace.CommitChangeTypeChanged, "src/tool", "src/tool", workspace.GitModeRegular, workspace.GitModeExecutable, oldObject, newObject, ""},
		{"symlink allowed", workspace.CommitChangeAdded, "", "src/link", workspace.GitModeAbsent, workspace.GitModeSymlink, workspace.GitObjectID{}, newObject, ""},
		{"submodule allowed", workspace.CommitChangeAdded, "", "modules/tool", workspace.GitModeAbsent, workspace.GitModeSubmodule, workspace.GitObjectID{}, newObject, ""},
		{"submodule frozen", workspace.CommitChangeAdded, "", "modules/vendor/tool", workspace.GitModeAbsent, workspace.GitModeSubmodule, workspace.GitObjectID{}, newObject, "frozen"},
		{"hidden subtree allowed", workspace.CommitChangeAdded, "", ".github/workflows/test.yml", workspace.GitModeAbsent, workspace.GitModeRegular, workspace.GitObjectID{}, newObject, ""},
		{"hidden file allowed", workspace.CommitChangeAdded, "", ".gitignore", workspace.GitModeAbsent, workspace.GitModeRegular, workspace.GitObjectID{}, newObject, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			change, err := workspace.NewCommitPathChange(
				test.kind, test.oldPath, test.newPath, test.oldMode, test.newMode, test.old, test.new,
			)
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

func TestCommitDiffRejectsMixedObjectFormats(t *testing.T) {
	t.Parallel()

	sha1 := mustGitObject(t, '1')
	sha256, err := workspace.ParseGitObjectID("sha256:" + strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	first, err := workspace.NewCommitPathChange(
		workspace.CommitChangeAdded, "", "src/one.go",
		workspace.GitModeAbsent, workspace.GitModeRegular, workspace.GitObjectID{}, sha1,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.NewCommitPathChange(
		workspace.CommitChangeAdded, "", "src/two.go",
		workspace.GitModeAbsent, workspace.GitModeRegular, workspace.GitObjectID{}, sha256,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewCommitDiff([]workspace.CommitPathChange{first, second}); err == nil ||
		!strings.Contains(err.Error(), "mixes Git object algorithms") {
		t.Fatalf("mixed diff error = %v", err)
	}
}

func TestFinalHistoryVerifierInspectsRealDetachedHistory(t *testing.T) {
	t.Parallel()

	repository, _, base := newProtocolRepository(t)
	for _, commit := range []struct {
		path    string
		subject string
	}{
		{path: "src/first.go", subject: "Add first checkpoint"},
		{path: "src/second.go", subject: "Add second checkpoint"},
	} {
		if err := os.WriteFile(filepath.Join(repository, commit.path), []byte("package protocol\n"), 0o644); err != nil {
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
  max_review_rounds: 3
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
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
      max_attempts: 3
      max_review_rounds: 3` + protocol + "\n"
}

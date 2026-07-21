package workspace_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestOptionalCommitProtocolSchemaIsStrictAndAbsentMeansUnconstrained(t *testing.T) {
	configured := protocolExecutionYAML(true)
	config, err := workspace.DecodeExecutionConfig([]byte(configured))
	if err != nil {
		t.Fatalf("DecodeExecutionConfig: %v", err)
	}
	units := config.MergeUnits()
	if len(units) != 1 {
		t.Fatalf("merge units = %d", len(units))
	}
	protocol, ok := units[0].CommitProtocol()
	if !ok || len(protocol.Steps()) != 2 || protocol.Digest().IsZero() {
		t.Fatalf("commit protocol = %#v, configured=%v", protocol, ok)
	}
	steps := protocol.Steps()
	if steps[0].ID().String() != "red-test" || steps[0].Message().Subject() != "Add failing protocol test" ||
		steps[0].Message().BodyPolicy() != workspace.CommitBodyForbidden {
		t.Fatalf("first step = %#v", steps[0])
	}
	checks := steps[0].Checks()
	if len(checks) != 1 || checks[0].Parser() != workspace.CheckParserGoTestJSON ||
		checks[0].Expectation().Kind() != workspace.CheckExpectationExpectedTestFailure {
		t.Fatalf("first checks = %#v", checks)
	}
	reviewFix, ok := units[0].ReviewFixProtocol()
	if !ok || reviewFix.SubjectPrefix() != "Review fix" || reviewFix.BodyPolicy() != workspace.CommitBodyRequired {
		t.Fatalf("review fix protocol = %#v, configured=%v", reviewFix, ok)
	}
	if units[0].Policy().MaxReviewFixes() != 2 {
		t.Fatalf("review-fix policy maximum = %d", units[0].Policy().MaxReviewFixes())
	}
	state, constrained, err := workspace.CommitProtocolStateForUnit(
		units[0], workspace.DigestBytes([]byte("generation")), mustGitObject(t, '1'),
	)
	if err != nil || !constrained || state.ProtocolDigest() != protocol.Digest() {
		t.Fatalf("configured state = %#v constrained=%v err=%v", state, constrained, err)
	}

	unconfiguredConfig, err := workspace.DecodeExecutionConfig([]byte(protocolExecutionYAML(false)))
	if err != nil {
		t.Fatal(err)
	}
	unconfigured := unconfiguredConfig.MergeUnits()[0]
	if _, ok := unconfigured.CommitProtocol(); ok {
		t.Fatal("absent protocol became configured")
	}
	if _, constrained, err := workspace.CommitProtocolStateForUnit(
		unconfigured, workspace.DigestBytes([]byte("generation")), mustGitObject(t, '1'),
	); err != nil || constrained {
		t.Fatalf("unconfigured protocol constrained=%v err=%v", constrained, err)
	}

	tests := []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name:    "implicit frozen paths",
			source:  strings.Replace(configured, "          frozen_paths: []\n", "", 1),
			wantErr: "must explicitly define allowed_paths, frozen_paths, and checks",
		},
		{
			name:    "runner bypass",
			source:  strings.Replace(configured, "              runner: codex\n", "              runner: other\n", 1),
			wantErr: "does not match profile runner",
		},
		{
			name:    "generic red checkpoint",
			source:  strings.Replace(configured, "                failure_ids:\n                  - example/pkg::TestProtocol\n", "                failure_ids: []\n", 1),
			wantErr: "requires structured failure_ids",
		},
		{
			name:    "ambiguous rebase subject",
			source:  strings.Replace(configured, "          subject: Implement protocol\n", "          subject: Add failing protocol test\n", 1),
			wantErr: "makes rebase mapping ambiguous",
		},
		{
			name:    "unknown protocol field",
			source:  strings.Replace(configured, "      steps:\n", "      shell_command: forbidden\n      steps:\n", 1),
			wantErr: "field shell_command not found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.DecodeExecutionConfig([]byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCommitPathPolicyCoversRenamesDeletesModesSymlinksAndSubmodules(t *testing.T) {
	policy, err := workspace.NewCommitPathPolicy(
		[]string{"src/**", "modules/**", ".github/**", ".gitignore"},
		[]string{"src/frozen.go", "modules/vendor/**"},
	)
	if err != nil {
		t.Fatal(err)
	}
	oldObject, newObject := mustGitObject(t, '1'), mustGitObject(t, '2')
	tests := []struct {
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
	}
	for _, test := range tests {
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
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCommitDiffRejectsMixedOrRepositoryMismatchedObjectFormats(t *testing.T) {
	sha1 := mustGitObject(t, '1')
	sha256, err := workspace.ParseGitObjectID("sha256:" + strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := workspace.NewCommitPathChange(
		workspace.CommitChangeAdded, "", "src/one.go",
		workspace.GitModeAbsent, workspace.GitModeRegular, workspace.GitObjectID{}, sha1,
	)
	second, _ := workspace.NewCommitPathChange(
		workspace.CommitChangeAdded, "", "src/two.go",
		workspace.GitModeAbsent, workspace.GitModeRegular, workspace.GitObjectID{}, sha256,
	)
	if _, err := workspace.NewCommitDiff([]workspace.CommitPathChange{first, second}); err == nil ||
		!strings.Contains(err.Error(), "mixes Git object algorithms") {
		t.Fatalf("mixed diff error = %v", err)
	}
	diff, err := workspace.NewCommitDiff([]workspace.CommitPathChange{second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewStagedCommitInspection(
		mustGitObject(t, '3'), mustGitObject(t, '4'), diff, nil, nil, nil,
	); err == nil || !strings.Contains(err.Error(), "diff object format differs") {
		t.Fatalf("repository-mismatched diff error = %v", err)
	}
}

func TestStructuredCheckParsersRejectGenericAndWrongFailures(t *testing.T) {
	isolation := workspace.StrictCheckIsolationProof()
	goPass := []byte("{\"Action\":\"pass\",\"Package\":\"example/pkg\"}\n")
	passResult := mustCheckResult(t, workspace.CheckExited, 0, "", goPass, nil, isolation)
	pass, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, passResult)
	if err != nil || pass.Kind() != workspace.CheckOutcomePassed {
		t.Fatalf("pass outcome = %#v err=%v", pass, err)
	}

	goFailure := []byte(strings.Join([]string{
		`{"Action":"fail","Package":"example/pkg","Test":"TestProtocol"}`,
		`{"Action":"fail","Package":"example/pkg"}`,
		"",
	}, "\n"))
	failureResult := mustCheckResult(t, workspace.CheckExited, 1, "", goFailure, nil, isolation)
	failure, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, failureResult)
	if err != nil || failure.Kind() != workspace.CheckOutcomeAssertionFailed ||
		len(failure.Identities()) != 1 || failure.Identities()[0] != "example/pkg::TestProtocol" {
		t.Fatalf("failure outcome = %#v err=%v", failure, err)
	}
	expected, _ := workspace.NewCheckExpectation(
		workspace.CheckExpectationExpectedTestFailure, []string{"example/pkg::TestProtocol"},
	)
	wrong, _ := workspace.NewCheckExpectation(
		workspace.CheckExpectationExpectedTestFailure, []string{"example/pkg::TestOther"},
	)
	if !expected.SatisfiedBy(failure) || wrong.SatisfiedBy(failure) {
		t.Fatal("structured failure identity matching is not exact")
	}

	compileOutput := []byte(strings.Join([]string{
		`{"ImportPath":"example/pkg","Action":"build-output","Output":"./x.go:2: undefined: missing\n"}`,
		`{"ImportPath":"example/pkg","Action":"build-fail"}`,
		`{"Action":"start","Package":"example/pkg"}`,
		`{"Action":"output","Package":"example/pkg","Output":"FAIL\texample/pkg [build failed]\n"}`,
		`{"Action":"fail","Package":"example/pkg","FailedBuild":"example/pkg"}`,
		"",
	}, "\n"))
	compileResult := mustCheckResult(t, workspace.CheckExited, 1, "", compileOutput, nil, isolation)
	compileOutcome, _ := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, compileResult)
	if compileOutcome.Kind() != workspace.CheckOutcomeCompilationFailed || expected.SatisfiedBy(compileOutcome) {
		t.Fatalf("compile outcome = %#v", compileOutcome)
	}

	generic := mustCheckResult(t, workspace.CheckExited, 1, "", []byte("generic failure\n"), nil, isolation)
	genericOutcome, _ := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, generic)
	if genericOutcome.Kind() != workspace.CheckOutcomeMalformedOutput || expected.SatisfiedBy(genericOutcome) {
		t.Fatalf("generic outcome = %#v", genericOutcome)
	}
	setupOutput := []byte("{\"Action\":\"fail\",\"Package\":\"example/pkg\"}\n")
	setupResult := mustCheckResult(t, workspace.CheckExited, 1, "", setupOutput, nil, isolation)
	setupOutcome, _ := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, setupResult)
	if setupOutcome.Kind() != workspace.CheckOutcomeSetupFailed || expected.SatisfiedBy(setupOutcome) {
		t.Fatalf("setup outcome = %#v", setupOutcome)
	}

	for _, test := range []struct {
		termination workspace.CheckTerminationKind
		signal      string
		kind        workspace.CheckOutcomeKind
	}{
		{workspace.CheckTimedOut, "", workspace.CheckOutcomeTimedOut},
		{workspace.CheckSignaled, "SIGKILL", workspace.CheckOutcomeSignaled},
		{workspace.CheckCrashed, "", workspace.CheckOutcomeCrashed},
		{workspace.CheckMissingExecutable, "", workspace.CheckOutcomeMissingExecutable},
		{workspace.CheckInfrastructure, "", workspace.CheckOutcomeInfrastructureFailed},
	} {
		result := mustCheckResult(t, test.termination, -1, test.signal, nil, nil, isolation)
		outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
		if err != nil || outcome.Kind() != test.kind || expected.SatisfiedBy(outcome) {
			t.Fatalf("termination %s outcome=%#v err=%v", test.termination, outcome, err)
		}
	}
}

func TestGoTestParserRejectsUncorrelatedBuildsAndAbnormalTermination(t *testing.T) {
	namedFailure := []string{
		`{"Action":"fail","Package":"example/pkg","Test":"TestExpected"}`,
		`{"Action":"fail","Package":"example/pkg"}`,
	}
	tests := []struct {
		name     string
		exitCode int
		lines    []string
		stderr   string
		want     workspace.CheckOutcomeKind
	}{
		{
			name:     "build event uses Package instead of ImportPath",
			exitCode: 1,
			lines: []string{
				`{"Action":"build-fail","Package":"example/pkg"}`,
				`{"Action":"fail","Package":"example/pkg","FailedBuild":"example/pkg"}`,
			},
			want: workspace.CheckOutcomeMalformedOutput,
		},
		{
			name:     "failed build is not correlated",
			exitCode: 1,
			lines: []string{
				`{"ImportPath":"example/dependency","Action":"build-fail"}`,
				`{"Action":"fail","Package":"example/pkg"}`,
			},
			want: workspace.CheckOutcomeMalformedOutput,
		},
		{
			name:     "FailedBuild has no build stream",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/pkg","FailedBuild":"example/dependency"}`,
			},
			want: workspace.CheckOutcomeMalformedOutput,
		},
		{
			name:     "package process was signaled",
			exitCode: 1,
			lines: []string{
				namedFailure[0],
				`{"Action":"output","Package":"example/pkg","Output":"signal: killed\n"}`,
				namedFailure[1],
			},
			want: workspace.CheckOutcomeSignaled,
		},
		{
			name:     "package process exited abnormally",
			exitCode: 1,
			lines: []string{
				namedFailure[0],
				`{"Action":"output","Package":"example/pkg","Output":"exit status 2\n"}`,
				namedFailure[1],
			},
			want: workspace.CheckOutcomeCrashed,
		},
		{
			name:     "go command wrote stderr",
			exitCode: 1,
			lines:    namedFailure,
			stderr:   "go: infrastructure failure\n",
			want:     workspace.CheckOutcomeInfrastructureFailed,
		},
		{
			name:     "go command used unexpected exit code",
			exitCode: 2,
			lines:    namedFailure,
			want:     workspace.CheckOutcomeInfrastructureFailed,
		},
		{
			name:     "duplicate event key",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Action":"pass","Package":"example/pkg","Test":"TestExpected"}`,
				`{"Action":"fail","Package":"example/pkg"}`,
			},
			want: workspace.CheckOutcomeMalformedOutput,
		},
		{
			name:     "failed test later passes",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/pkg","Test":"TestExpected"}`,
				`{"Action":"pass","Package":"example/pkg","Test":"TestExpected"}`,
				`{"Action":"fail","Package":"example/pkg"}`,
			},
			want: workspace.CheckOutcomeMalformedOutput,
		},
		{
			name:     "failed test later skips",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/pkg","Test":"TestExpected"}`,
				`{"Action":"skip","Package":"example/pkg","Test":"TestExpected"}`,
				`{"Action":"fail","Package":"example/pkg"}`,
			},
			want: workspace.CheckOutcomeMalformedOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := []byte(strings.Join(append(append([]string{}, test.lines...), ""), "\n"))
			result := mustCheckResult(
				t, workspace.CheckExited, test.exitCode, "", output, []byte(test.stderr),
				workspace.StrictCheckIsolationProof(),
			)
			outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
			if err != nil || outcome.Kind() != test.want {
				t.Fatalf("outcome = %#v err=%v, want %s", outcome, err, test.want)
			}
		})
	}
}

func TestGoTestParserRecognizesRealGo126CompilationFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/broken\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "broken.go"), []byte("package broken\nfunc Broken() { missing() }\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "-json", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil || command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("broken go test err=%v exit=%v stdout=%s stderr=%s", err, command.ProcessState, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"ImportPath":"example.com/broken"`)) {
		t.Fatalf("Go 1.26 build stream did not contain ImportPath: %s", stdout.String())
	}
	result := mustCheckResult(
		t, workspace.CheckExited, command.ProcessState.ExitCode(), "", stdout.Bytes(), stderr.Bytes(),
		workspace.StrictCheckIsolationProof(),
	)
	outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
	if err != nil || outcome.Kind() != workspace.CheckOutcomeCompilationFailed {
		t.Fatalf("real compilation outcome = %#v err=%v stdout=%s stderr=%s", outcome, err, stdout.String(), stderr.String())
	}
}

func TestGoTestParserRecognizesRealTimeoutAndIgnoresLoggedMarkersOnPass(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		timeout  string
		exitCode int
		want     workspace.CheckOutcomeKind
	}{
		{
			name: "timeout",
			source: `package fixture

import (
	"testing"
	"time"
)

func TestTimeout(t *testing.T) {
	time.Sleep(time.Second)
}
`,
			timeout: "50ms", exitCode: 1, want: workspace.CheckOutcomeTimedOut,
		},
		{
			name: "passing marker log",
			source: `package fixture

import (
	"fmt"
	"testing"
)

func TestMarkerLog(t *testing.T) {
	fmt.Println("panic: test timed out after 50ms")
	fmt.Println("signal: killed")
	fmt.Println("exit status 2")
}
`,
			timeout: "5s", exitCode: 0, want: workspace.CheckOutcomePassed,
		},
		{
			name: "failing marker log",
			source: `package fixture

import (
	"fmt"
	"testing"
)

func TestMarkerLog(t *testing.T) {
	fmt.Println("panic: test timed out after 50ms")
	fmt.Println("fatal error: synthetic")
	fmt.Println("signal: killed")
	fmt.Println("exit status 2")
	t.Error("expected assertion failure")
}
`,
			timeout: "5s", exitCode: 1, want: workspace.CheckOutcomeAssertionFailed,
		},
		{
			name: "real panic",
			source: `package fixture

import "testing"

func TestPanic(t *testing.T) {
	panic("boom")
}
`,
			timeout: "5s", exitCode: 1, want: workspace.CheckOutcomeCrashed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(root, "go.mod"), []byte("module example.com/parserfixture\n\ngo 1.26\n"), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "fixture_test.go"), []byte(test.source), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "test", "-json", "-timeout="+test.timeout, "./...")
			command.Dir = root
			command.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if command.ProcessState == nil || command.ProcessState.ExitCode() != test.exitCode ||
				test.exitCode == 0 && err != nil || test.exitCode != 0 && err == nil {
				t.Fatalf(
					"go test err=%v exit=%v stdout=%s stderr=%s",
					err, command.ProcessState, stdout.String(), stderr.String(),
				)
			}
			result := mustCheckResult(
				t, workspace.CheckExited, test.exitCode, "", stdout.Bytes(), stderr.Bytes(),
				workspace.StrictCheckIsolationProof(),
			)
			outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
			if err != nil || outcome.Kind() != test.want {
				t.Fatalf(
					"outcome=%#v err=%v want=%s stdout=%s stderr=%s",
					outcome, err, test.want, stdout.String(), stderr.String(),
				)
			}
		})
	}
}

func TestGoTestParserAllowsRepeatedTestRunsWithDistinctTerminals(t *testing.T) {
	output := []byte(strings.Join([]string{
		`{"Action":"run","Package":"example/pkg","Test":"TestRepeated"}`,
		`{"Action":"pass","Package":"example/pkg","Test":"TestRepeated"}`,
		`{"Action":"run","Package":"example/pkg","Test":"TestRepeated"}`,
		`{"Action":"pass","Package":"example/pkg","Test":"TestRepeated"}`,
		`{"Action":"pass","Package":"example/pkg"}`,
		"",
	}, "\n"))
	result := mustCheckResult(
		t, workspace.CheckExited, 0, "", output, nil, workspace.StrictCheckIsolationProof(),
	)
	outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
	if err != nil || outcome.Kind() != workspace.CheckOutcomePassed {
		t.Fatalf("repeated test-run outcome = %#v err=%v", outcome, err)
	}
}

func TestGoTestParserDoesNotHideSetupFailureBehindExpectedTestFailure(t *testing.T) {
	output := []byte(strings.Join([]string{
		`{"Action":"fail","Package":"example/a","Test":"TestExpected"}`,
		`{"Action":"fail","Package":"example/a"}`,
		`{"Action":"fail","Package":"example/b"}`,
		"",
	}, "\n"))
	result := mustCheckResult(
		t, workspace.CheckExited, 1, "", output, nil, workspace.StrictCheckIsolationProof(),
	)
	outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
	if err != nil || outcome.Kind() != workspace.CheckOutcomeSetupFailed || len(outcome.Identities()) != 0 {
		t.Fatalf("mixed-package outcome = %#v err=%v", outcome, err)
	}
	expected, err := workspace.NewCheckExpectation(
		workspace.CheckExpectationExpectedTestFailure, []string{"example/a::TestExpected"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if expected.SatisfiedBy(outcome) {
		t.Fatal("package setup failure satisfied an expected test-failure check")
	}
}

func TestGoTestParserRejectsPartialMultiPackageStreams(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		lines    []string
	}{
		{
			name: "pass",
			lines: []string{
				`{"Action":"pass","Package":"example/a"}`,
				`{"Action":"start","Package":"example/b"}`,
			},
		},
		{
			name:     "expected failure",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/a","Test":"TestExpected"}`,
				`{"Action":"fail","Package":"example/a"}`,
				`{"Action":"run","Package":"example/b","Test":"TestIncomplete"}`,
			},
		},
		{
			name:     "package failure while a named test remains active",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/a","Test":"TestExpected"}`,
				`{"Action":"run","Package":"example/a","Test":"TestNeverTerminates"}`,
				`{"Action":"fail","Package":"example/a"}`,
			},
		},
		{
			name: "package pass while a named test remains active",
			lines: []string{
				`{"Action":"run","Package":"example/a","Test":"TestNeverTerminates"}`,
				`{"Action":"pass","Package":"example/a"}`,
			},
		},
		{
			name: "repeated run before a named test terminates",
			lines: []string{
				`{"Action":"run","Package":"example/a","Test":"TestRepeated"}`,
				`{"Action":"run","Package":"example/a","Test":"TestRepeated"}`,
				`{"Action":"pass","Package":"example/a","Test":"TestRepeated"}`,
				`{"Action":"pass","Package":"example/a"}`,
			},
		},
		{
			name:     "late event after terminal failure",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/a","Test":"TestExpected"}`,
				`{"Action":"fail","Package":"example/a"}`,
				`{"Action":"run","Package":"example/a","Test":"TestLate"}`,
			},
		},
		{
			name: "late event after terminal pass",
			lines: []string{
				`{"Action":"pass","Package":"example/a"}`,
				`{"Action":"start","Package":"example/a"}`,
			},
		},
		{
			name:     "named failure contradicted by package pass",
			exitCode: 1,
			lines: []string{
				`{"Action":"fail","Package":"example/a","Test":"TestExpected"}`,
				`{"Action":"pass","Package":"example/a"}`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines := append(append([]string{}, test.lines...), "")
			output := []byte(strings.Join(lines, "\n"))
			result := mustCheckResult(
				t, workspace.CheckExited, test.exitCode, "", output, nil, workspace.StrictCheckIsolationProof(),
			)
			outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, result)
			if err != nil || outcome.Kind() != workspace.CheckOutcomeMalformedOutput {
				t.Fatalf("partial outcome = %#v err=%v", outcome, err)
			}
		})
	}
}

func TestAssertionAndDiagnosticParsersRequireExactStructuredIdentities(t *testing.T) {
	for _, test := range []struct {
		name       string
		parser     workspace.CheckParserKind
		collection string
		kind       workspace.CheckOutcomeKind
	}{
		{"assertion", workspace.CheckParserAssertionJSON, "assertions", workspace.CheckOutcomeAssertionFailed},
		{"diagnostic", workspace.CheckParserDiagnosticJSON, "diagnostics", workspace.CheckOutcomeDiagnosticFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := []byte(`{"schema_version":1,"status":"failed","` + test.collection + `":[{"id":"checkpoint","status":"failed"}]}`)
			result := mustCheckResult(
				t, workspace.CheckExited, 1, "", output, nil, workspace.StrictCheckIsolationProof(),
			)
			outcome, err := workspace.ParseCheckOutcome(test.parser, result)
			if err != nil || outcome.Kind() != test.kind || len(outcome.Identities()) != 1 || outcome.Identities()[0] != "checkpoint" {
				t.Fatalf("outcome = %#v err=%v", outcome, err)
			}
			expected, _ := workspace.NewCheckExpectation(
				workspace.CheckExpectationExpectedTestFailure, []string{"checkpoint"},
			)
			wrong, _ := workspace.NewCheckExpectation(
				workspace.CheckExpectationExpectedTestFailure, []string{"other"},
			)
			if !expected.SatisfiedBy(outcome) || wrong.SatisfiedBy(outcome) {
				t.Fatal("structured identity matching is not exact")
			}

			malformed := mustCheckResult(
				t, workspace.CheckExited, 1, "",
				append(output[:len(output)-1], []byte(`,"unknown":true}`)...), nil,
				workspace.StrictCheckIsolationProof(),
			)
			malformedOutcome, err := workspace.ParseCheckOutcome(test.parser, malformed)
			if err != nil || malformedOutcome.Kind() != workspace.CheckOutcomeMalformedOutput || expected.SatisfiedBy(malformedOutcome) {
				t.Fatalf("malformed outcome = %#v err=%v", malformedOutcome, err)
			}

			for _, duplicate := range [][]byte{
				[]byte(`{"schema_version":1,"status":"failed","status":"passed","` + test.collection + `":[{"id":"checkpoint","status":"failed"}]}`),
				[]byte(`{"schema_version":1,"status":"failed","` + test.collection + `":[{"id":"checkpoint","id":"other","status":"failed"}]}`),
			} {
				duplicateResult := mustCheckResult(
					t, workspace.CheckExited, 1, "", duplicate, nil, workspace.StrictCheckIsolationProof(),
				)
				duplicateOutcome, err := workspace.ParseCheckOutcome(test.parser, duplicateResult)
				if err != nil || duplicateOutcome.Kind() != workspace.CheckOutcomeMalformedOutput {
					t.Fatalf("duplicate-key outcome = %#v err=%v", duplicateOutcome, err)
				}
			}
		})
	}
}

func TestCommitProtocolReducerOrdersCommitAndChecksAndInvalidatesChecksOnRebase(t *testing.T) {
	generation := workspace.DigestBytes([]byte("generation"))
	base, tree, commit := mustGitObject(t, '1'), mustGitObject(t, '2'), mustGitObject(t, '3')
	step, check := protocolTestStep(t, "protocol-step", "Implement protocol")
	protocol, err := workspace.NewCommitProtocol([]workspace.CommitStep{step})
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewCommitProtocolState(generation, base, protocol)
	if err != nil {
		t.Fatal(err)
	}
	diff := addedDiff(t, "src/protocol.go", mustGitObject(t, '4'))
	staged, err := workspace.NewStagedCommitInspection(base, tree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stageEvent, _ := workspace.NewStageCommitStep(staged, "")
	reduction, err := workspace.ReduceCommitProtocol(state, stageEvent)
	if err != nil || reduction.State().Phase() != workspace.CommitProtocolAwaitingCommit {
		t.Fatalf("stage reduction = %#v err=%v", reduction, err)
	}
	if len(reduction.Effects()) != 1 {
		t.Fatalf("stage effects = %#v", reduction.Effects())
	}
	if _, ok := reduction.Effects()[0].(workspace.CreateConfiguredCommitEffect); !ok {
		t.Fatalf("stage effect type = %T", reduction.Effects()[0])
	}
	state = reduction.State()
	commitEvidence, err := workspace.NewCommitObjectEvidence(
		generation, step.ID(), 1, commit, base, tree, step.Message().Subject(), "", diff, step.Paths().Digest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	commitEvent, _ := workspace.NewRecordCommitStep(commitEvidence)
	reduction, err = workspace.ReduceCommitProtocol(state, commitEvent)
	if err != nil || reduction.State().Phase() != workspace.CommitProtocolAwaitingChecks {
		t.Fatalf("commit reduction = %#v err=%v", reduction, err)
	}
	if _, ok := reduction.Effects()[0].(workspace.RunConfiguredCheckEffect); !ok {
		t.Fatalf("commit effect type = %T", reduction.Effects()[0])
	}
	state = reduction.State()
	passProcess := mustCheckResult(
		t, workspace.CheckExited, 0, "",
		[]byte("{\"Action\":\"pass\",\"Package\":\"example/pkg\"}\n"), nil,
		workspace.StrictCheckIsolationProof(),
	)
	passOutcome, _ := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, passProcess)
	checkEvidence, err := workspace.NewCommitCheckEvidence(generation, step, check, commitEvidence, passProcess, passOutcome)
	if err != nil {
		t.Fatal(err)
	}
	checkEvent, _ := workspace.NewRecordCommitCheck(checkEvidence)
	reduction, err = workspace.ReduceCommitProtocol(state, checkEvent)
	if err != nil || reduction.State().Phase() != workspace.CommitProtocolComplete {
		t.Fatalf("check reduction = %#v err=%v", reduction, err)
	}
	if _, ok := reduction.Effects()[0].(workspace.CommitProtocolCompletedEffect); !ok {
		t.Fatalf("completion effect type = %T", reduction.Effects()[0])
	}
	state = reduction.State()

	newBase, newTree, newCommit := mustGitObject(t, '5'), mustGitObject(t, '6'), mustGitObject(t, '7')
	rebasedEvidence, err := workspace.NewCommitObjectEvidence(
		generation, step.ID(), 1, newCommit, newBase, newTree,
		step.Message().Subject(), "", diff, step.Paths().Digest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	rebaseEvent, _ := workspace.NewRemapRebasedCommits(newBase, []workspace.CommitObjectEvidence{rebasedEvidence})
	reduction, err = workspace.ReduceCommitProtocol(state, rebaseEvent)
	if err != nil || reduction.State().Phase() != workspace.CommitProtocolAwaitingChecks || reduction.State().RebaseEpoch() != 1 {
		t.Fatalf("rebase reduction = %#v err=%v", reduction, err)
	}
	completed := reduction.State().CompletedSteps()
	if len(completed) != 1 || completed[0].Commit().Commit() != newCommit || len(completed[0].Checks()) != 0 {
		t.Fatalf("rebased state = %#v", completed)
	}
}

func TestCommitCheckEvidenceRejectsOutcomeUnrelatedToProcessResult(t *testing.T) {
	generation := workspace.DigestBytes([]byte("generation"))
	base, tree, commit := mustGitObject(t, '1'), mustGitObject(t, '2'), mustGitObject(t, '3')
	step, check := protocolTestStep(t, "protocol-step", "Implement protocol")
	diff := addedDiff(t, "src/protocol.go", mustGitObject(t, '4'))
	commitEvidence, err := workspace.NewCommitObjectEvidence(
		generation, step.ID(), 1, commit, base, tree,
		step.Message().Subject(), "", diff, step.Paths().Digest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	passOutcome, err := workspace.ParseCheckOutcome(
		workspace.CheckParserGoTestJSON,
		passingCheckResult(t, workspace.StrictCheckIsolationProof()),
	)
	if err != nil {
		t.Fatal(err)
	}
	failingResult := mustCheckResult(
		t, workspace.CheckExited, 1, "",
		[]byte("{\"Action\":\"fail\",\"Package\":\"example/pkg\",\"Test\":\"TestBroken\"}\n{\"Action\":\"fail\",\"Package\":\"example/pkg\"}\n"),
		nil, workspace.StrictCheckIsolationProof(),
	)
	if _, err := workspace.NewCommitCheckEvidence(
		generation, step, check, commitEvidence, failingResult, passOutcome,
	); err == nil || !strings.Contains(err.Error(), "does not match its process result") {
		t.Fatalf("unrelated outcome error = %v", err)
	}
}

func TestCommitProtocolRebaseAcceptsACompletedPrefix(t *testing.T) {
	generation := workspace.DigestBytes([]byte("generation"))
	base := mustGitObject(t, '1')
	first, firstCheck := protocolTestStep(t, "first-step", "First step")
	optionalMessage, _ := workspace.NewCommitMessagePolicy("First step", workspace.CommitBodyOptional, nil)
	first, _ = workspace.NewCommitStep(first.ID(), optionalMessage, first.Paths(), first.Checks())
	second, _ := protocolTestStep(t, "second-step", "Second step")
	protocol, err := workspace.NewCommitProtocol([]workspace.CommitStep{first, second})
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewCommitProtocolState(generation, base, protocol)
	if err != nil {
		t.Fatal(err)
	}
	diff := addedDiff(t, "src/protocol.go", mustGitObject(t, '4'))
	tree, commit := mustGitObject(t, '2'), mustGitObject(t, '3')
	staged, _ := workspace.NewStagedCommitInspection(base, tree, diff, nil, nil, nil)
	stage, _ := workspace.NewStageCommitStep(staged, "original body")
	reduction, err := workspace.ReduceCommitProtocol(state, stage)
	if err != nil {
		t.Fatal(err)
	}
	commitEvidence, _ := workspace.NewCommitObjectEvidence(
		generation, first.ID(), 1, commit, base, tree,
		first.Message().Subject(), "original body", diff, first.Paths().Digest(),
	)
	recordCommit, _ := workspace.NewRecordCommitStep(commitEvidence)
	reduction, err = workspace.ReduceCommitProtocol(reduction.State(), recordCommit)
	if err != nil {
		t.Fatal(err)
	}
	process := passingCheckResult(t, workspace.StrictCheckIsolationProof())
	outcome, _ := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, process)
	checkEvidence, _ := workspace.NewCommitCheckEvidence(
		generation, first, firstCheck, commitEvidence, process, outcome,
	)
	recordCheck, _ := workspace.NewRecordCommitCheck(checkEvidence)
	reduction, err = workspace.ReduceCommitProtocol(reduction.State(), recordCheck)
	if err != nil || reduction.State().Phase() != workspace.CommitProtocolReady ||
		len(reduction.State().CompletedSteps()) != 1 {
		t.Fatalf("completed prefix = %#v err=%v", reduction.State(), err)
	}

	newBase, newTree, newCommit := mustGitObject(t, '5'), mustGitObject(t, '6'), mustGitObject(t, '7')
	rewrittenEvidence, _ := workspace.NewCommitObjectEvidence(
		generation, first.ID(), 1, newCommit, newBase, newTree,
		first.Message().Subject(), "rewritten body", diff, first.Paths().Digest(),
	)
	rewrittenRemap, _ := workspace.NewRemapRebasedCommits(newBase, []workspace.CommitObjectEvidence{rewrittenEvidence})
	if _, err := workspace.ReduceCommitProtocol(reduction.State(), rewrittenRemap); err == nil ||
		!strings.Contains(err.Error(), "changed its commit message") {
		t.Fatalf("rewritten rebase error = %v", err)
	}
	rebasedEvidence, _ := workspace.NewCommitObjectEvidence(
		generation, first.ID(), 1, newCommit, newBase, newTree,
		first.Message().Subject(), "original body", diff, first.Paths().Digest(),
	)
	remap, _ := workspace.NewRemapRebasedCommits(newBase, []workspace.CommitObjectEvidence{rebasedEvidence})
	reduction, err = workspace.ReduceCommitProtocol(reduction.State(), remap)
	if err != nil || reduction.State().Phase() != workspace.CommitProtocolAwaitingChecks ||
		len(reduction.State().CompletedSteps()) != 1 || reduction.State().CompletedSteps()[0].Commit().Commit() != newCommit {
		t.Fatalf("rebased prefix = %#v err=%v", reduction.State(), err)
	}
}

func TestCommitEvidenceRejectsMergeCommitsAndReviewFixBudgetIsDurableState(t *testing.T) {
	step, _ := protocolTestStep(t, "one-step", "One step")
	diff := addedDiff(t, "src/value.go", mustGitObject(t, '4'))
	inspection, err := workspace.NewGitCommitInspection(
		mustGitObject(t, '5'), []workspace.GitObjectID{mustGitObject(t, '1'), mustGitObject(t, '2')},
		mustGitObject(t, '3'), "One step", "", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspection.Evidence(workspace.DigestBytes([]byte("generation")), step, 1); err == nil || !strings.Contains(err.Error(), "merge commits are forbidden") {
		t.Fatalf("merge commit evidence error = %v", err)
	}
	paths, _ := workspace.NewCommitPathPolicy([]string{"src/**"}, []string{})
	review, _ := workspace.NewReviewFixProtocol("Review fix", workspace.CommitBodyRequired, paths, nil)
	state, err := workspace.NewReviewFixState(
		workspace.DigestBytes([]byte("generation")), mustGitObject(t, '1'), review, 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	reserve, _ := workspace.NewReserveReviewFix(1, state.Head())
	reduction, err := workspace.ReduceReviewFix(state, reserve)
	if err != nil || reduction.State().Used() != 1 || reduction.State().Remaining() != 1 {
		t.Fatalf("first reservation state = %#v err=%v", reduction.State(), err)
	}
	state = reduction.State()
	if _, err := workspace.ReduceReviewFix(state, reserve); err == nil || !strings.Contains(err.Error(), "in-flight") {
		t.Fatalf("duplicate/in-flight reservation error = %v", err)
	}
	reviewStep, _ := review.Step(1)
	fixDiff := addedDiff(t, "src/review.go", mustGitObject(t, '4'))
	staged, _ := workspace.NewStagedCommitInspection(mustGitObject(t, '1'), mustGitObject(t, '2'), fixDiff, nil, nil, nil)
	stage, _ := workspace.NewStageReviewFix(1, staged, "accepted feedback")
	reduction, err = workspace.ReduceReviewFix(state, stage)
	if err != nil || reduction.State().Phase() != workspace.ReviewFixAwaitingCommit {
		t.Fatalf("staged review fix = %#v err=%v", reduction.State(), err)
	}
	evidence, _ := workspace.NewCommitObjectEvidence(
		state.Generation(), reviewStep.ID(), 1, mustGitObject(t, '3'), mustGitObject(t, '1'), mustGitObject(t, '2'),
		reviewStep.Message().Subject(), "accepted feedback", fixDiff, reviewStep.Paths().Digest(),
	)
	record, _ := workspace.NewRecordReviewFixCommit(1, evidence)
	reduction, err = workspace.ReduceReviewFix(reduction.State(), record)
	if err != nil || reduction.State().Phase() != workspace.ReviewFixComplete || reduction.State().Head() != mustGitObject(t, '3') {
		t.Fatalf("completed review fix = %#v err=%v", reduction.State(), err)
	}
}

func TestReviewFixRebaseRerunsChecksForEveryRewrittenFix(t *testing.T) {
	generation := workspace.DigestBytes([]byte("generation"))
	_, check := protocolTestStep(t, "unused", "Unused")
	paths, _ := workspace.NewCommitPathPolicy([]string{"src/**"}, nil)
	protocol, err := workspace.NewReviewFixProtocol(
		"Review fix", workspace.CommitBodyRequired, paths, []workspace.CommitCheck{check},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewReviewFixState(generation, mustGitObject(t, '1'), protocol, 2)
	if err != nil {
		t.Fatal(err)
	}
	process := mustCheckResult(
		t, workspace.CheckExited, 0, "", []byte("{\"Action\":\"pass\",\"Package\":\"example/pkg\"}\n"), nil,
		workspace.StrictCheckIsolationProof(),
	)
	outcome, err := workspace.ParseCheckOutcome(workspace.CheckParserGoTestJSON, process)
	if err != nil {
		t.Fatal(err)
	}

	var original []workspace.CommitObjectEvidence
	for index := 0; index < 2; index++ {
		ordinal := uint16(index + 1)
		step, _ := protocol.Step(ordinal)
		parent := state.Head()
		diff := addedDiff(t, fmt.Sprintf("src/review-%d.go", ordinal), mustGitObject(t, byte('2'+index*3)))
		tree := mustGitObject(t, byte('3'+index*3))
		commit := mustGitObject(t, byte('4'+index*3))
		reserve, _ := workspace.NewReserveReviewFix(ordinal, parent)
		reduction, err := workspace.ReduceReviewFix(state, reserve)
		if err != nil {
			t.Fatal(err)
		}
		staged, _ := workspace.NewStagedCommitInspection(parent, tree, diff, nil, nil, nil)
		stage, _ := workspace.NewStageReviewFix(ordinal, staged, fmt.Sprintf("accepted feedback %d", ordinal))
		reduction, err = workspace.ReduceReviewFix(reduction.State(), stage)
		if err != nil {
			t.Fatal(err)
		}
		evidence, _ := workspace.NewCommitObjectEvidence(
			generation, step.ID(), ordinal, commit, parent, tree, step.Message().Subject(),
			fmt.Sprintf("accepted feedback %d", ordinal), diff, step.Paths().Digest(),
		)
		recordCommit, _ := workspace.NewRecordReviewFixCommit(ordinal, evidence)
		reduction, err = workspace.ReduceReviewFix(reduction.State(), recordCommit)
		if err != nil {
			t.Fatal(err)
		}
		checkEvidence, _ := workspace.NewCommitCheckEvidence(generation, step, check, evidence, process, outcome)
		recordCheck, _ := workspace.NewRecordReviewFixCheck(ordinal, checkEvidence)
		reduction, err = workspace.ReduceReviewFix(reduction.State(), recordCheck)
		if err != nil {
			t.Fatal(err)
		}
		state = reduction.State()
		original = append(original, evidence)
	}

	newBase := mustGitObject(t, '8')
	firstStep, _ := protocol.Step(1)
	secondStep, _ := protocol.Step(2)
	first := original[0]
	second := original[1]
	firstRebased, _ := workspace.NewCommitObjectEvidence(
		generation, firstStep.ID(), 1, mustGitObject(t, '9'), newBase, mustGitObject(t, 'a'),
		first.Subject(), first.Body(), first.Diff(), first.PathPolicyDigest(),
	)
	secondRebased, _ := workspace.NewCommitObjectEvidence(
		generation, secondStep.ID(), 2, mustGitObject(t, 'b'), firstRebased.Commit(), mustGitObject(t, 'c'),
		second.Subject(), second.Body(), second.Diff(), second.PathPolicyDigest(),
	)
	remap, _ := workspace.NewRemapRebasedReviewFixes(
		newBase, []workspace.CommitObjectEvidence{firstRebased, secondRebased},
	)
	reduction, err := workspace.ReduceReviewFix(state, remap)
	if err != nil || reduction.State().Phase() != workspace.ReviewFixAwaitingChecks ||
		reduction.State().RebaseEpoch() != 1 {
		t.Fatalf("remapped review fixes state=%#v err=%v", reduction.State(), err)
	}
	state = reduction.State()
	for index, commit := range []workspace.CommitObjectEvidence{firstRebased, secondRebased} {
		effects, err := workspace.PendingReviewFixEffects(state)
		if err != nil || len(effects) != 1 {
			t.Fatalf("pending rebased check %d effects=%#v err=%v", index+1, effects, err)
		}
		effect, ok := effects[0].(workspace.RunConfiguredCheckEffect)
		if !ok || effect.StepOrdinal() != uint16(index+1) || effect.Commit().Commit() != commit.Commit() {
			t.Fatalf("pending rebased check %d = %#v", index+1, effects[0])
		}
		step, _ := protocol.Step(uint16(index + 1))
		evidence, _ := workspace.NewCommitCheckEvidence(generation, step, check, commit, process, outcome)
		record, _ := workspace.NewRecordReviewFixCheck(uint16(index+1), evidence)
		reduction, err = workspace.ReduceReviewFix(state, record)
		if err != nil {
			t.Fatalf("record rebased check %d: %v", index+1, err)
		}
		state = reduction.State()
	}
	fixes := state.Fixes()
	if state.Phase() != workspace.ReviewFixComplete || state.Head() != secondRebased.Commit() ||
		len(fixes) != 2 || fixes[0].Checks()[0].Commit() != firstRebased.Commit() ||
		fixes[1].Checks()[0].Commit() != secondRebased.Commit() {
		t.Fatalf("revalidated review-fix chain = %#v", state)
	}
}

func protocolTestStep(t *testing.T, id, subject string) (workspace.CommitStep, workspace.CommitCheck) {
	t.Helper()
	message, err := workspace.NewCommitMessagePolicy(subject, workspace.CommitBodyForbidden, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := workspace.NewCommitPathPolicy([]string{"src/**"}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	expectation, _ := workspace.NewCheckExpectation(workspace.CheckExpectationPass, nil)
	command, _ := workspace.NewArgv("go", "test", "./...")
	check, err := workspace.NewCommitCheck(
		workspace.MustID("unit-tests"), workspace.MustID("codex"), workspace.CheckParserGoTestJSON, command, expectation,
	)
	if err != nil {
		t.Fatal(err)
	}
	stepID, err := workspace.NewID(id)
	if err != nil {
		t.Fatal(err)
	}
	step, err := workspace.NewCommitStep(stepID, message, paths, []workspace.CommitCheck{check})
	if err != nil {
		t.Fatal(err)
	}
	return step, check
}

func addedDiff(t *testing.T, path string, object workspace.GitObjectID) workspace.CommitDiff {
	t.Helper()
	change, err := workspace.NewCommitPathChange(
		workspace.CommitChangeAdded, "", path,
		workspace.GitModeAbsent, workspace.GitModeRegular,
		workspace.GitObjectID{}, object,
	)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := workspace.NewCommitDiff([]workspace.CommitPathChange{change})
	if err != nil {
		t.Fatal(err)
	}
	return diff
}

func mustGitObject(t *testing.T, digit byte) workspace.GitObjectID {
	t.Helper()
	object, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat(string(digit), 40))
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func mustCheckResult(
	t *testing.T,
	termination workspace.CheckTerminationKind,
	exitCode int,
	signal string,
	stdout, stderr []byte,
	isolation workspace.CheckIsolationProof,
) workspace.CheckProcessResult {
	t.Helper()
	result, err := workspace.NewCheckProcessResult(termination, exitCode, signal, stdout, stderr, isolation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func protocolExecutionYAML(configured bool) string {
	protocol := ""
	if configured {
		protocol = `
    commit_protocol:
      steps:
        - id: red-test
          subject: Add failing protocol test
          body_policy: forbidden
          allowed_paths:
            - src/**
          frozen_paths: []
          checks:
            - id: red-check
              runner: codex
              parser: go-test-json
              command:
                - go
                - test
                - ./...
              expectation:
                kind: expected_test_failure
                failure_ids:
                  - example/pkg::TestProtocol
        - id: implementation
          subject: Implement protocol
          body_policy: required
          allowed_paths:
            - src/**
          frozen_paths:
            - src/frozen.go
          checks:
            - id: green-check
              runner: codex
              parser: go-test-json
              command:
                - go
                - test
                - ./...
              expectation:
                kind: pass
                failure_ids: []
    review_fix_protocol:
      subject_prefix: Review fix
      body_policy: required
      allowed_paths:
        - src/**
      frozen_paths:
        - src/frozen.go
      checks:
        - id: review-check
          runner: codex
          parser: go-test-json
          command:
            - go
            - test
            - ./...
          expectation:
            kind: pass
            failure_ids: []`
	}
	return `schema_version: 2
policy:
  require_passing_checks: true
  require_signed_receipts: true
  allow_write_network: false
  max_attempts: 3
  max_review_rounds: 3
  max_review_fixes: 2
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      mode: pause_only
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2` + protocol + "\n"
}

package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalCommandDecodersRequireExactReceiptFreeFields(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target func() any
		want   string
	}{
		{
			name: "init requires worktree root",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`,
			target: func() any { return &initializeRequest{} },
			want:   "worktree_root",
		},
		{
			name: "reserve rejects caller base",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "plan_id": "plan-one",
  "merge_unit_id": "unit-one",
  "attempt_number": 1,
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "base": "sha1:1111111111111111111111111111111111111111"
}`,
			target: func() any { return &reserveAttemptInput{} },
			want:   "unknown field",
		},
		{
			name: "acknowledgement requires directive",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "kind": "goal_completed",
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "idempotency_key": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}`,
			target: func() any { return &acknowledgeInput{} },
			want:   "directive_digest",
		},
		{
			name: "acknowledgement rejects receipt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "kind": "goal_completed",
  "directive_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "idempotency_key": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "receipt": {}
}`,
			target: func() any { return &acknowledgeInput{} },
			want:   "unknown field",
		},
		{
			name: "owner response requires exact head",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "boundary_id": "boundary-one",
  "directive_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "response": "continue"
}`,
			target: func() any { return &ownerResponseInput{} },
			want:   "expected_head",
		},
		{
			name: "review result rejects receipt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "reservation_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "request_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "reviewer_instance": "reviewer-one",
  "status": "completed",
  "findings": [],
  "isolation": {
    "repository_read_only": true,
    "scratch_ephemeral": true,
    "repository_hooks": false,
    "write_network": false,
    "external_write": false
  },
  "receipt": {}
}`,
			target: func() any { return &recordReviewInput{} },
			want:   "unknown field",
		},
		{
			name: "non-applying review fix rejects body",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "ordinal": 1,
  "accepted_finding_ids": [],
  "body": "ignored"
}`,
			target: func() any { return &reviewFixInput{} },
			want:   "unknown field",
		},
		{
			name: "integration requires attempt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`,
			target: func() any { return &integrateMergeUnitInput{} },
			want:   "attempt_id",
		},
		{
			name: "completion rejects attempt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one"
}`,
			target: func() any { return &completeVerifyInput{} },
			want:   "unknown field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := decodeRequest([]byte(test.source), test.target())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}

	var completion completeVerifyInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`), &completion); err != nil {
		t.Fatalf("valid completion envelope: %v", err)
	}
}

func TestRequestSchemasExposeOnlySupportedLocalMutations(t *testing.T) {
	encoded, err := json.Marshal(RequestSchemas())
	if err != nil {
		t.Fatal(err)
	}
	source := string(encoded)
	for _, forbidden := range []string{
		`"receipt"`, `"reconcile.`, `"control.`, `"provider.`,
		`"provider_broker"`, `"commit.rebase"`, `"base"`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf(
				"request schemas expose removed field %q: %s",
				forbidden, source,
			)
		}
	}
	schemas := RequestSchemas()["requests"].(map[string]any)
	for _, required := range []string{
		"init",
		"recover",
		"attempt.reserve",
		"attempt.materialize",
		"attempt.adopt-head",
		"attempt.boundary",
		"attempt.next-goal",
		"attempt.acknowledge",
		"attempt.owner-response",
		"attempt.resume",
		"commit.next",
		"review.start",
		"review.reserve",
		"review.record",
		"review.reserve-fix",
		"review.apply-fix",
		"review.record-fix",
		"review.ready",
		"integrate.merge-unit",
		"complete.verify",
	} {
		if _, exists := schemas[required]; !exists {
			t.Fatalf("request schemas omit %s", required)
		}
	}
	if len(schemas) != 20 {
		t.Fatalf("request schema count = %d: %+v", len(schemas), schemas)
	}
	for _, action := range []string{
		"review.reserve-fix",
		"review.record-fix",
	} {
		properties := schemas[action].(map[string]any)["properties"].(map[string]any)
		if _, exists := properties["body"]; exists {
			t.Fatalf("%s schema accepts ignored body: %+v", action, properties)
		}
	}
	applyProperties := schemas["review.apply-fix"].(map[string]any)["properties"].(map[string]any)
	if _, exists := applyProperties["body"]; !exists {
		t.Fatalf("review.apply-fix schema omits body: %+v", applyProperties)
	}
}

func TestDecodeRequestKeepsSchemaOptionalFieldsOptional(t *testing.T) {
	var input commitNextInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one"
}`), &input); err != nil {
		t.Fatalf("optional commit body was required: %v", err)
	}
	var reviewFix applyReviewFixInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "ordinal": 1,
  "accepted_finding_ids": [],
  "body": "apply the accepted fixes"
}`), &reviewFix); err != nil {
		t.Fatalf("valid review fix body: %v", err)
	}
}

func TestReportDirectiveSchemaKeepsChoicesOptional(t *testing.T) {
	reports := ReportSchemas()["reports"].(map[string]any)
	scheduler := reports["scheduler"].(map[string]any)
	properties := scheduler["properties"].(map[string]any)
	units := properties["units"].(map[string]any)
	unit := units["items"].(map[string]any)
	unitProperties := unit["properties"].(map[string]any)
	directives := unitProperties["pending_directives"].(map[string]any)
	directive := directives["items"].(map[string]any)
	required := directive["required"].([]string)
	for _, name := range required {
		if name == "choices" {
			t.Fatalf("directive schema still requires omitted empty choices: %+v", required)
		}
	}
	directiveProperties := directive["properties"].(map[string]any)
	if _, exists := directiveProperties["choices"]; !exists {
		t.Fatalf("directive schema no longer describes choices: %+v", directiveProperties)
	}
}

func TestDeferredLocalCommandsStrictlyDecodeTheirFinalEnvelopes(
	t *testing.T,
) {
	_, err := executeIntegration(
		context.Background(),
		workspace.WorkspaceBundle{},
		Options{
			Subaction: "merge-unit",
			Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one"
}`),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "workspace directory is required") {
		t.Fatalf("valid integration envelope error = %v", err)
	}
	_, err = executeCompletion(
		workspace.WorkspaceBundle{},
		Options{
			Subaction: "verify",
			Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("valid completion envelope error = %v", err)
	}
}

func TestExecuteIntegrationSucceedsAndRetriesIdempotently(t *testing.T) {
	repositoryRoot := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repositoryRoot, "init", "-b", "main")
	runGitTest(t, repositoryRoot, "config", "user.name", "Feature Test")
	runGitTest(
		t, repositoryRoot, "config", "user.email",
		"feature@example.test",
	)
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "tracked.txt"),
		[]byte("base\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repositoryRoot, "add", "tracked.txt")
	runGitTest(t, repositoryRoot, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(
		t, strings.TrimSpace(runGitTest(
			t, repositoryRoot, "rev-parse", "HEAD",
		)),
	)

	bundleRoot := canonicalWorkspaceCommandTempDir(t)
	if err := os.MkdirAll(
		filepath.Join(bundleRoot, "plans"), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(bundleRoot, "config"), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	write := func(relative, content string) {
		t.Helper()
		if err := os.WriteFile(
			filepath.Join(bundleRoot, relative),
			[]byte(content), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	write(workspace.WorkspaceBundleFileName, `{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/alpha.yaml"],
  "execution_config": "config/execution.yaml"
}`)
	write("feature.workspace.yaml", fmt.Sprintf(`schema_version: 2
id: command-workspace
mode: local
repository:
  root: %q
base_ref: refs/heads/main
base_commit: %s
feature_branch: feature/command-workspace
execution_config: config/execution.yaml
plans:
  - id: alpha-plan
    source: plans/alpha.yaml
dependencies: []
`, repositoryRoot, base))
	write("plans/alpha.yaml", `schema_version: 2
id: alpha-plan
title: Alpha Plan
stories:
  - id: story-one
    summary: Implement the command integration path.
    acceptance:
      - Command integration succeeds.
    implementation:
      - Integrate the accepted attempt.
    testing:
      - Retry integration idempotently.
    dependencies: []
merge_units:
  - id: unit-one
    name: Unit One
    story_ids:
      - story-one
`)
	write("config/execution.yaml", `schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 2
  max_review_rounds: 2
  max_review_fixes: 1
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 1
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      mode: pause_only
      serial_segment: command-segment
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 1
`)
	checkpointInput := func(occurredAt string) []byte {
		return []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": %q
}`, occurredAt))
	}
	if _, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: bundleRoot, Kind: workspace.PlanCheckpointInitial,
			Input: checkpointInput("2026-07-25T17:59:58Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: bundleRoot, Kind: workspace.PlanCheckpointLock,
			Input: checkpointInput("2026-07-25T17:59:59Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	bundle, err := workspace.LoadWorkspaceBundle(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspaceDir := canonicalWorkspaceCommandTempDir(t)
	worktreeRoot := canonicalWorkspaceCommandTempDir(t)
	if _, err := initializeWorkspace(
		context.Background(), bundle,
		Options{
			WorkspaceDir: workspaceDir,
			Input: []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:00Z",
  "worktree_root": %q
}`, worktreeRoot)),
		},
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	attemptGit := workspace.DefaultLocalAttemptGitAdapter()
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("command-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	mergeUnit, err := workspace.NewMergeUnitReference(
		workspace.MustID("alpha-plan"),
		workspace.MustID("unit-one"),
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := workspace.ReserveAttempt(
		context.Background(), journal, bundle.Definition(), attemptGit,
		workspace.ReserveAttemptRequest{
			MergeUnit: mergeUnit, AttemptNumber: 1, Goal: goal,
			OccurredAt: time.Date(
				2026, time.July, 25, 18, 0, 1, 0, time.UTC,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = workspace.MaterializeAttempt(
		context.Background(), journal, bundle.Definition(), attemptGit,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(),
			OccurredAt: time.Date(
				2026, time.July, 25, 18, 0, 2, 0, time.UTC,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(attempt.Worktree(), "integration.txt"),
		[]byte("accepted\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, attempt.Worktree(), "add", "integration.txt")
	runGitTest(
		t, attempt.Worktree(), "commit", "-m",
		"Accepted command implementation",
	)
	repository := localReviewRepository{
		git: workspace.DefaultLocalCommitGitAdapter(),
	}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(), journal, bundle.Definition(), repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID: attempt.AttemptID(),
			OccurredAt: time.Date(
				2026, time.July, 25, 18, 0, 3, 0, time.UTC,
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	options := Options{
		Subaction:    "merge-unit",
		WorkspaceDir: workspaceDir,
		Input: []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:04Z",
  "attempt_id": %q
}`, attempt.AttemptID().String())),
	}
	first, err := executeIntegration(
		context.Background(), bundle, options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "recorded" ||
		first.Action != "integrate.merge-unit" ||
		first.Report.Target.FeatureHead == base.String() {
		t.Fatalf("successful integration result = %#v", first)
	}
	integrated := false
	for _, unit := range first.Report.Integration.Units {
		if unit.AttemptID == attempt.AttemptID().String() &&
			unit.Status == "integrated" {
			integrated = true
		}
	}
	if !integrated {
		t.Fatalf(
			"successful integration report = %#v",
			first.Report.Integration,
		)
	}
	journal, err = workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	firstRecords := len(firstSnapshot.Records())
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	options.Input = []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:05Z",
  "attempt_id": %q
}`, attempt.AttemptID().String()))
	retried, err := executeIntegration(
		context.Background(), bundle, options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Report.Target.FeatureHead !=
		first.Report.Target.FeatureHead {
		t.Fatalf(
			"idempotent integration retry moved feature head: first=%s retry=%s",
			first.Report.Target.FeatureHead,
			retried.Report.Target.FeatureHead,
		)
	}
	journal, err = workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	retriedSnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(retriedSnapshot.Records()) != firstRecords {
		t.Fatalf(
			"idempotent integration retry appended records: first=%d retry=%d",
			firstRecords, len(retriedSnapshot.Records()),
		)
	}
}

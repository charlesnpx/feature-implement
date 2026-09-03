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
			name: "review gate record rejects unknown fields",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
	  "dispatch_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	  "verdict": "satisfied",
	  "evidence_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	  "receipt": {}
}`,
			target: func() any { return &recordReviewGateInput{} },
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
		"attempt.start",
		"attempt.adopt-head",
		"attempt.pause",
		"attempt.resume",
		"attempt.abandon",
		"review.dispatch",
		"review.record",
		"review.record-document",
		"review.ready",
		"integrate.merge-unit",
		"complete.verify",
	} {
		if _, exists := schemas[required]; !exists {
			t.Fatalf("request schemas omit %s", required)
		}
	}
	if len(schemas) != 13 {
		t.Fatalf("request schema count = %d: %+v", len(schemas), schemas)
	}
}

func TestDecodeRequestKeepsSchemaOptionalFieldsOptional(t *testing.T) {
	var input recordReviewGateInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
	  "dispatch_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	  "verdict": "failed_to_run",
	  "evidence_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}`), &input); err != nil {
		t.Fatalf("valid review gate record envelope: %v", err)
	}
}

func TestWorkspaceViewSchemaOmitsRetiredDirectiveFields(t *testing.T) {
	view := WorkspaceViewSchema()
	if view["additionalProperties"] != false {
		t.Fatalf("workspace view schema permits unknown fields: %+v", view)
	}
	properties := view["properties"].(map[string]any)
	scheduler := properties["scheduler"].(map[string]any)
	schedulerProperties := scheduler["properties"].(map[string]any)
	units := schedulerProperties["units"].(map[string]any)
	unit := units["items"].(map[string]any)
	unitProperties := unit["properties"].(map[string]any)
	if _, exists := unitProperties["branch"]; exists {
		t.Fatalf("workspace unit schema still exposes retired branch: %+v", unitProperties)
	}
	directives := unitProperties["pending_directives"].(map[string]any)
	directive := directives["items"].(map[string]any)
	required := directive["required"].([]string)
	boundaryKindRequired := false
	for _, name := range required {
		if name == "boundary_kind" {
			boundaryKindRequired = true
		}
	}
	directiveProperties := directive["properties"].(map[string]any)
	expectedDirectiveFields := map[string]bool{
		"kind": true, "boundary_kind": true, "workspace_id": true,
		"generation": true, "attempt_id": true, "boundary_id": true,
		"goal_id": true, "goal_scope": true, "head": true,
	}
	if len(directiveProperties) != len(expectedDirectiveFields) {
		t.Fatalf("directive schema fields = %+v", directiveProperties)
	}
	for field := range expectedDirectiveFields {
		if _, exists := directiveProperties[field]; !exists {
			t.Fatalf("directive schema omits %q: %+v", field, directiveProperties)
		}
	}
	if _, exists := directiveProperties["boundary_kind"]; !exists || !boundaryKindRequired {
		t.Fatalf("directive schema does not require boundary kind: %+v / %+v", required, directiveProperties)
	}
	kinds := directiveProperties["kind"].(map[string]any)["enum"].([]string)
	if len(kinds) != 1 || kinds[0] != "boundary_pending" {
		t.Fatalf("directive schema kinds = %+v", kinds)
	}
	blockers := unitProperties["blockers"].(map[string]any)
	blocker := blockers["items"].(map[string]any)
	if blocker["type"] != "string" {
		t.Fatalf("workspace view blocker is not a string: %+v", blocker)
	}
	if _, exists := blocker["properties"]; exists {
		t.Fatalf("workspace view blocker retains object properties: %+v", blocker)
	}
	attempts := properties["attempts"].(map[string]any)
	attempt := attempts["items"].(map[string]any)
	attemptProperties := attempt["properties"].(map[string]any)
	if _, exists := attemptProperties["branch"]; exists {
		t.Fatalf("workspace attempt schema still exposes retired branch: %+v", attemptProperties)
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
		context.Background(),
		workspace.WorkspaceBundle{},
		Options{
			Subaction: "verify",
			Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "workspace directory is required") {
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
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 2
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      checkpoint: pause_only
      escalation: allowed
      serial_segment: command-segment
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 2
`)
	if _, err := Execute(context.Background(), Options{
		Action:           "validate",
		BundleDir:        bundleRoot,
		WriteLocks:       true,
		GeneratorVersion: "test",
	}); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, bundleRoot, "init", "-b", "main")
	runGitTest(t, bundleRoot, "config", "user.name", "Feature Test")
	runGitTest(t, bundleRoot, "config", "user.email", "feature@example.test")
	runGitTest(t, bundleRoot, "add", ".")
	runGitTest(t, bundleRoot, "commit", "-m", "Committed plan locks")
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
	attempt, err := workspace.StartAttempt(
		context.Background(), journal, bundle.Definition(), attemptGit,
		workspace.StartAttemptRequest{
			MergeUnit: mergeUnit, AttemptNumber: 1, Goal: goal,
			OccurredAt: time.Date(
				2026, time.July, 25, 18, 0, 1, 0, time.UTC,
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
		t, attempt.Worktree(),
		"-c", "user.name=Command Test",
		"-c", "user.email=command@example.test",
		"commit", "-m",
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
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	completionOptions := Options{
		Subaction:    "verify",
		WorkspaceDir: workspaceDir,
		Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:06Z"
}`),
	}
	completed, err := executeCompletion(
		context.Background(), bundle, completionOptions,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Action != "complete.verify" ||
		!completed.Report.Completion.Complete ||
		len(completed.Report.Completion.Blockers) != 0 ||
		completed.Report.Completion.ReportDigest == "" ||
		completed.Report.Gates.Completion.Status !=
			workspace.GatePassed {
		t.Fatalf(
			"successful completion result = %#v",
			completed,
		)
	}
	journal, err = workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	completedSnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	completedRecords := len(completedSnapshot.Records())
	if completedRecords != firstRecords+1 {
		t.Fatalf(
			"completion records = %d, want %d",
			completedRecords, firstRecords+1,
		)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	completionOptions.Input = []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:07Z"
}`)
	completionRetry, err := executeCompletion(
		context.Background(), bundle, completionOptions,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !completionRetry.Report.Completion.Complete ||
		completionRetry.Report.Completion.ReportDigest !=
			completed.Report.Completion.ReportDigest {
		t.Fatalf(
			"idempotent completion result = %#v",
			completionRetry,
		)
	}
	journal, err = workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	completionRetrySnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(completionRetrySnapshot.Records()) != completedRecords {
		t.Fatalf(
			"completion retry appended records: first=%d retry=%d",
			completedRecords,
			len(completionRetrySnapshot.Records()),
		)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverWorkspace(
		context.Background(),
		bundle,
		Options{
			WorkspaceDir: workspaceDir,
			Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:07.5Z"
}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Action != "recover" ||
		!recovered.Report.Completion.Complete ||
		recovered.Report.Completion.ReportDigest !=
			completed.Report.Completion.ReportDigest {
		t.Fatalf(
			"idempotent CLI recovery = %#v", recovered,
		)
	}
	journal, err = workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	recoverySnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(recoverySnapshot.Records()) != completedRecords {
		t.Fatalf(
			"idempotent recovery appended records: completion=%d recovery=%d",
			completedRecords, len(recoverySnapshot.Records()),
		)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	runGitTest(
		t, repositoryRoot,
		"update-ref",
		bundle.Definition().Workspace().FeatureRef(),
		strings.TrimPrefix(base.String(), "sha1:"),
	)
	completionOptions.Input = []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:08Z"
}`)
	if _, err := executeCompletion(
		context.Background(), bundle, completionOptions,
	); err == nil {
		t.Fatal(
			"post-completion feature-ref drift was not rejected",
		)
	}
	drifted, err := readWorkspaceView(
		context.Background(), bundle, workspaceDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !drifted.Drift.Detected ||
		len(drifted.Drift.Reasons) == 0 ||
		drifted.Completion.Complete ||
		drifted.Gates.Completion.Status != workspace.GateFailed {
		t.Fatalf(
			"post-completion drift report = drift %#v completion %#v gate %#v",
			drifted.Drift,
			drifted.Completion,
			drifted.Gates.Completion,
		)
	}
}

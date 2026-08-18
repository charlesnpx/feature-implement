package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestWorkspaceAbandonReleasesReadyRuntimeIsTerminalAndIdempotent(
	t *testing.T,
) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, exists := runtime.LocalTarget()
	if !exists || !target.Created() {
		t.Fatalf("ready local target = %#v exists=%t", target, exists)
	}
	root := harness.definition.Workspace().RepositoryRoot()
	featureRef := target.Binding().FeatureRef()
	beforeHead := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", featureRef,
	))
	beforeReflog := runTargetGitTest(
		t,
		root,
		"reflog",
		"show",
		"--format=%H%x00%gs",
		"-n",
		"100",
		featureRef,
		"--",
	)
	beforeRecords := len(snapshot.Records())
	reason := "superseded by a newer generation"

	result, err := workspace.AbandonWorkspace(
		context.Background(),
		harness.journal,
		harness.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:04:05Z"),
			Reason:     reason,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Record().EventType() != workspace.JournalEventWorkspaceAbandoned ||
		result.Record().Sequence() != uint64(beforeRecords+1) ||
		result.Abandonment().Reason() != reason ||
		!result.Abandonment().Released() {
		t.Fatalf("abandonment result = %#v", result)
	}
	if afterHead := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", featureRef,
	)); afterHead != beforeHead {
		t.Fatalf("released feature ref head = %s, want %s", afterHead, beforeHead)
	}
	if afterReflog := runTargetGitTest(
		t,
		root,
		"reflog",
		"show",
		"--format=%H%x00%gs",
		"-n",
		"100",
		featureRef,
		"--",
	); afterReflog != beforeReflog {
		t.Fatalf("released feature ref reflog changed = %q, want %q", afterReflog, beforeReflog)
	}
	releaseMarkerRef := "refs/feature-implement/released/" +
		target.Binding().FeatureBranch()
	if markerHead := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", releaseMarkerRef,
	)); markerHead != beforeHead {
		t.Fatalf("release marker ref head = %s, want %s", markerHead, beforeHead)
	}
	wantReleaseMarker := "feature-implement feature-ref released " +
		target.IntentDigest().String()
	if marker := strings.TrimSpace(runTargetGitTest(
		t, root, "reflog", "show", "--format=%gs", "-n", "1", releaseMarkerRef, "--",
	)); marker != wantReleaseMarker {
		t.Fatalf("release marker reflog = %q, want %q", marker, wantReleaseMarker)
	}

	after, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	report, err := workspace.RebuildWorkspaceReport(after, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	if report.Abandonment == nil || report.Abandonment.Reason != reason ||
		report.Abandonment.FeatureRef != featureRef ||
		!report.Abandonment.Released ||
		!containsCompletionBlocker(report.Completion.Blockers, "workspace:abandoned") ||
		report.Gates.Completion.Status != workspace.GateFailed {
		t.Fatalf("abandonment report = %#v", report)
	}
	firstDigest, err := workspace.VerifyWorkspaceRuntimeConformance(
		after, harness.definition.Generation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := workspace.VerifyWorkspaceRuntimeConformance(
		after, harness.definition.Generation(),
	)
	if err != nil || firstDigest != secondDigest {
		t.Fatalf("abandonment replay conformance = %s / %s, err=%v", firstDigest, secondDigest, err)
	}

	_, err = workspace.ReserveAttempt(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.git,
		workspace.ReserveAttemptRequest{
			MergeUnit:     harness.unit,
			AttemptNumber: 1,
			Goal:          harness.goal,
			OccurredAt:    mustTime(t, "2026-08-18T15:04:06Z"),
		},
	)
	if err == nil || !strings.Contains(
		err.Error(), "workspace abandonment is final for local workflow mutations",
	) {
		t.Fatalf("post-abandon reservation error = %v", err)
	}
	if records := journalRecordCount(t, harness.journal); records != beforeRecords+1 {
		t.Fatalf("post-abandon reservation records = %d, want %d", records, beforeRecords+1)
	}

	retry, err := workspace.AbandonWorkspace(
		context.Background(),
		harness.journal,
		harness.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:04:07Z"),
			Reason:     reason,
		},
	)
	if err != nil || retry.Record().Sequence() != 0 ||
		retry.Abandonment().EventDigest() != result.Abandonment().EventDigest() ||
		journalRecordCount(t, harness.journal) != beforeRecords+1 {
		t.Fatalf("idempotent abandonment = %#v err=%v", retry, err)
	}
	_, err = workspace.AbandonWorkspace(
		context.Background(),
		harness.journal,
		harness.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:04:08Z"),
			Reason:     "different supersession reason",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match recorded reason") ||
		journalRecordCount(t, harness.journal) != beforeRecords+1 {
		t.Fatalf("different abandonment reason error = %v", err)
	}

	_, err = workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		t.TempDir(),
		harness.definition,
		mustTime(t, "2026-08-18T15:04:09Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: t.TempDir()},
	)
	if err == nil || !strings.Contains(
		err.Error(), "was released by an abandoned workspace",
	) {
		t.Fatalf("released feature ref admission error = %v", err)
	}
	if afterHead := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", featureRef,
	)); afterHead != beforeHead {
		t.Fatalf("released feature ref head after admission = %s, want %s", afterHead, beforeHead)
	}
	if afterReflog := runTargetGitTest(
		t,
		root,
		"reflog",
		"show",
		"--format=%H%x00%gs",
		"-n",
		"100",
		featureRef,
		"--",
	); afterReflog != beforeReflog {
		t.Fatalf("released feature ref reflog after admission changed = %q, want %q", afterReflog, beforeReflog)
	}
	if markerHead := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", releaseMarkerRef,
	)); markerHead != beforeHead {
		t.Fatalf("release marker ref head after admission = %s, want %s", markerHead, beforeHead)
	}
}

func TestWorkspaceAbandonBeforeFeatureRefCreationDoesNotTouchGit(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	runtimeRoot := t.TempDir()
	fired := false
	_, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-08-18T15:10:00Z"),
		workspace.WorkspaceInitializationOptions{
			WorktreeRoot: t.TempDir(),
			TargetFault: func(point workspace.LocalTargetInitializationFaultPoint) error {
				if point == workspace.LocalTargetFaultAfterIntentSynced && !fired {
					fired = true
					return context.DeadlineExceeded
				}
				return nil
			},
		},
	)
	if err == nil || !fired {
		t.Fatalf("creation-intent interruption = %v fired=%t", err, fired)
	}
	journal, err := workspace.OpenWorkspaceJournal(runtimeRoot, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	before := journalRecordCount(t, journal)
	result, err := workspace.AbandonWorkspace(
		context.Background(),
		journal,
		definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:10:01Z"),
			Reason:     "superseded before feature-ref creation",
		},
	)
	if err != nil || result.Abandonment().Released() ||
		result.Abandonment().FeatureHead().IsZero() == false {
		t.Fatalf("pre-creation abandonment = %#v err=%v", result, err)
	}
	if records := journalRecordCount(t, journal); records != before+1 {
		t.Fatalf("pre-creation abandonment records = %d, want %d", records, before+1)
	}
	if branch := strings.TrimSpace(runTargetGitTest(
		t,
		definition.Workspace().RepositoryRoot(),
		"branch",
		"--list",
		definition.Workspace().FeatureBranch(),
	)); branch != "" {
		t.Fatalf("pre-creation abandonment created feature branch %q", branch)
	}
}

func TestWorkspaceAbandonRejectsFeatureRefDriftWithoutMutation(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	root := harness.definition.Workspace().RepositoryRoot()
	featureRef := harness.definition.Workspace().FeatureRef()
	treeRaw := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", baseObjectHex(harness.base)+"^{tree}",
	))
	tree, err := workspace.ParseGitObjectID(
		string(harness.base.Algorithm()) + ":" + treeRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	drift := createIntegrationTestCommit(
		t, root, tree, []workspace.GitObjectID{harness.base}, "abandonment drift",
	)
	runTargetGitTest(
		t, root, "update-ref", featureRef, baseObjectHex(drift), baseObjectHex(harness.base),
	)
	beforeHead := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", featureRef,
	))
	beforeMarker := strings.TrimSpace(runTargetGitTest(
		t, root, "reflog", "show", "--format=%gs", "-n", "1", featureRef, "--",
	))
	beforeRecords := journalRecordCount(t, harness.journal)

	_, err = workspace.AbandonWorkspace(
		context.Background(),
		harness.journal,
		harness.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:20:00Z"),
			Reason:     "superseded after ref drift",
		},
	)
	if err == nil || !strings.Contains(
		err.Error(), "feature ref changed from its exact owned head and marker before release",
	) {
		t.Fatalf("drifted feature ref abandonment error = %v", err)
	}
	if records := journalRecordCount(t, harness.journal); records != beforeRecords {
		t.Fatalf("drifted feature ref abandonment records = %d, want %d", records, beforeRecords)
	}
	if head := strings.TrimSpace(runTargetGitTest(
		t, root, "rev-parse", featureRef,
	)); head != beforeHead {
		t.Fatalf("drifted feature ref head changed = %s, want %s", head, beforeHead)
	}
	if marker := strings.TrimSpace(runTargetGitTest(
		t, root, "reflog", "show", "--format=%gs", "-n", "1", featureRef, "--",
	)); marker != beforeMarker {
		t.Fatalf("drifted feature ref marker changed = %q, want %q", marker, beforeMarker)
	}
}

func TestWorkspaceAbandonRejectsCompletedRuntimeWithoutMutation(t *testing.T) {
	t.Parallel()

	harness := newCompletedWorkspaceHarness(t)
	if _, err := workspace.CompleteWorkspace(
		context.Background(),
		harness.core.journal,
		harness.core.definition,
		harness.git,
		workspace.CompleteWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:30:00Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	before := journalRecordCount(t, harness.core.journal)
	_, err := workspace.AbandonWorkspace(
		context.Background(),
		harness.core.journal,
		harness.core.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-08-18T15:30:01Z"),
			Reason:     "superseded after completion",
		},
	)
	if err == nil || !strings.Contains(
		err.Error(), "workspace abandonment is not allowed after workspace completion",
	) {
		t.Fatalf("completed runtime abandonment error = %v", err)
	}
	if records := journalRecordCount(t, harness.core.journal); records != before {
		t.Fatalf("completed runtime abandonment records = %d, want %d", records, before)
	}
}

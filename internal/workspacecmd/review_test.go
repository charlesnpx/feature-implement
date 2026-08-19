package workspacecmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalReviewRepositoryAdoptsActualCleanDescendantHead(t *testing.T) {
	repository := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "Feature Test")
	runGitTest(t, repository, "config", "user.email", "feature@example.test")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))

	if err := os.WriteFile(tracked, []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Implementation")
	head := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	tree := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD^{tree}")))
	request, err := workspace.NewReviewRepositoryRequest(repository, "main", base)
	if err != nil {
		t.Fatal(err)
	}
	adapter := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	snapshot, err := adapter.InspectReviewSnapshot(context.Background(), request)
	if err != nil || !snapshot.Clean() || snapshot.Head() != head || snapshot.Tree() != tree {
		t.Fatalf("actual review snapshot = %#v error=%v", snapshot, err)
	}

	runGitTest(t, repository, "reset", "--hard", gitObjectHex(base))
	staleRequest, err := workspace.NewReviewRepositoryRequest(repository, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), staleRequest); err == nil ||
		!strings.Contains(err.Error(), "descend from durable head") {
		t.Fatalf("rewound ordinary head error = %v", err)
	}
}

func TestExecuteReviewRecordFailureRecordsAndRetriesTheSameProfile(t *testing.T) {
	fixture := newReviewRecordFailureCommandFixture(t)
	options := Options{
		Action:       "review",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
	}
	options.Subaction = "start"
	options.Input = []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-08-18T22:00:00.000000000Z",
  "attempt_id": %q
}`, fixture.attemptID.String()))
	started, err := Execute(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	startResult, ok := started.(ReviewCommandResult)
	if !ok || startResult.Status != "recorded" || startResult.Action != "review.start" {
		t.Fatalf("review start result = %#v", started)
	}

	options.Subaction = "reserve"
	options.Input = []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-08-18T22:00:01.000000000Z",
  "attempt_id": %q,
  "reviewer_instance": "isolation-reviewer-one",
  "idempotency_key": %q
}`, fixture.attemptID.String(), workspace.DigestBytes([]byte("review-record-failure")).String()))
	reserved, err := Execute(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	reservedResult, ok := reserved.(ReviewCommandResult)
	if !ok || reservedResult.Status != "recorded" || reservedResult.Action != "review.reserve" {
		t.Fatalf("review reserve result = %#v", reserved)
	}
	reservation, ok := reservedResult.Detail.(ReviewReservationView)
	if !ok {
		t.Fatalf("review reservation detail = %#v", reservedResult.Detail)
	}

	failureDigest := workspace.DigestBytes([]byte("reviewer broke isolation")).String()
	options.Subaction = "record-failure"
	options.Input = reviewRecordFailureInput(fixture.attemptID, reservation.ReservationDigest, failureDigest)
	first, err := Execute(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, ok := first.(ReviewCommandResult)
	if !ok || firstResult.Status != "recorded" || firstResult.Action != "review.record-failure" {
		t.Fatalf("review record-failure result = %#v", first)
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(fixture.workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	recordedEvents := len(snapshot.Records())

	replayed, err := Execute(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	replayedResult, ok := replayed.(ReviewCommandResult)
	if !ok || replayedResult.Status != "recorded" || replayedResult.Action != "review.record-failure" {
		t.Fatalf("replayed review record-failure result = %#v", replayed)
	}
	replayedSnapshot, err := workspace.ReadWorkspaceJournalSnapshot(fixture.workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayedSnapshot.Records()) != recordedEvents {
		t.Fatalf("record-failure replay appended event: first=%d replay=%d", recordedEvents, len(replayedSnapshot.Records()))
	}

	options.Input = reviewRecordFailureInput(
		fixture.attemptID,
		reservation.ReservationDigest,
		workspace.DigestBytes([]byte("different review failure")).String(),
	)
	if _, err := Execute(context.Background(), options); err == nil {
		t.Fatal("record-failure accepted a different failure digest")
	}

	valid := reviewRecordFailureInput(fixture.attemptID, reservation.ReservationDigest, failureDigest)
	for _, test := range []struct {
		name  string
		input []byte
	}{
		{
			name:  "unknown field",
			input: []byte(strings.Replace(string(valid), "\n}", ",\n  \"unexpected\": true\n}", 1)),
		},
		{
			name: "duplicate key",
			input: []byte(strings.Replace(
				string(valid), "\n}", fmt.Sprintf(",\n  \"failure_digest\": %q\n}", failureDigest), 1,
			)),
		},
		{
			name:  "wrong schema version",
			input: []byte(strings.Replace(string(valid), "\"schema_version\": 2", "\"schema_version\": 1", 1)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			options.Input = test.input
			if _, err := Execute(context.Background(), options); err == nil {
				t.Fatal("record-failure accepted invalid input")
			}
		})
	}

	afterInvalid, err := workspace.ReadWorkspaceJournalSnapshot(fixture.workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterInvalid.Records()) != recordedEvents {
		t.Fatalf("rejected record-failure input appended event: before=%d after=%d", recordedEvents, len(afterInvalid.Records()))
	}
	bundle, err := workspace.LoadWorkspaceBundle(fixture.bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := workspace.RebuildReviewRuntime(afterInvalid, bundle.Definition())
	if err != nil {
		t.Fatal(err)
	}
	state, exists := reviews.State(fixture.attemptID)
	if !exists || state.MergeReady() {
		t.Fatalf("review state after invocation failure = %#v exists=%v", state, exists)
	}
	next, available, err := state.NextRequest()
	if err != nil || !available || next.Profile().ID().String() != reservation.Request.ProfileID ||
		next.Invocation() != reservation.Request.Invocation+1 {
		t.Fatalf("review retry after invocation failure = %#v available=%v err=%v", next, available, err)
	}
	rounds := state.Rounds()
	if len(rounds) != 1 || len(rounds[0].Failures()) != 1 || len(rounds[0].Results()) != 0 {
		t.Fatalf("incomplete review round after invocation failure = %#v", state)
	}
}

func reviewRecordFailureInput(
	attemptID workspace.ID,
	reservationDigest string,
	failureDigest string,
) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-08-18T22:00:02.000000000Z",
  "attempt_id": %q,
  "reservation_digest": %q,
  "failure_digest": %q
}`, attemptID.String(), reservationDigest, failureDigest))
}

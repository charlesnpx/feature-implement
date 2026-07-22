package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestGitHubAdapterReconcilesPushFromExactImmutableIntent(t *testing.T) {
	adapter := newFakeGitHubAdapter(t)
	base := testGitObject(t, "1")
	head := testGitObject(t, "2")
	intent := testProviderIntent(t, workspace.ProviderIntentPush, base, head)
	query, err := workspace.NewProviderIntentQuery(intent)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GIT_LS_REMOTE", gitObjectHex(head)+"\trefs/heads/mu-test\n")
	observation, err := adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent applied push: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectApplied || observation.Digest().IsZero() {
		t.Fatalf("applied push observation = %#v", observation)
	}

	t.Setenv("FAKE_GIT_LS_REMOTE", "")
	observation, err = adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent absent push: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectNotApplied {
		t.Fatalf("absent push disposition = %s", observation.Disposition())
	}

	drift := testGitObject(t, "3")
	t.Setenv("FAKE_GIT_LS_REMOTE", gitObjectHex(drift)+"\trefs/heads/mu-test\n")
	observation, err = adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent drifted push: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectUnknown {
		t.Fatalf("drifted push disposition = %s", observation.Disposition())
	}

	t.Setenv("FAKE_GIT_REMOTE_URL", "https://github.com/example/other.git")
	if _, err := adapter.QueryIntent(context.Background(), query); err == nil || !strings.Contains(err.Error(), "not github provider repository") {
		t.Fatalf("mismatched configured remote error = %v", err)
	}
}

func TestGitHubRepositoryIdentityRequiresExactGitHubRepository(t *testing.T) {
	tests := map[string]string{
		"https": "https://github.com/Example/Project.git",
		"ssh":   "git@github.com:Example/Project.git",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			repository, err := githubRepositoryFromRemoteURL(value)
			if err != nil || repository != "Example/Project" {
				t.Fatalf("repository = %q, %v", repository, err)
			}
		})
	}
	for _, value := range []string{"https://git.example.com/example/project.git", "https://github.com/example/project/extra.git", "/tmp/project"} {
		if repository, err := githubRepositoryFromRemoteURL(value); err == nil {
			t.Fatalf("invalid repository identity %q normalized to %q", value, repository)
		}
	}
}

func TestGitHubCommandsPinCanonicalHostAndRepository(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "fake-gh")
	script := `#!/bin/sh
printf '%s\n%s\n' "$GH_HOST" "$GH_REPO"
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_HOST", "example.invalid")
	t.Setenv("GH_REPO", "example.invalid/other/project")
	adapter := &githubProviderAdapter{
		repositoryRoot: directory, providerRepository: "example/project", ghExecutable: executable,
	}
	output, err := adapter.runGH(context.Background(), "api", "user")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "github.com\ngithub.com/example/project\n" {
		t.Fatalf("pinned GitHub environment = %q", output)
	}
	environment := githubProcessEnvironment([]string{
		"PATH=/usr/bin", "GH_HOST=one.invalid", "gh_host=two.invalid", "GH_REPO=wrong/repository",
	}, "example/project")
	joined := "\x00" + strings.Join(environment, "\x00") + "\x00"
	if strings.Count(joined, "\x00GH_HOST=") != 1 || strings.Count(joined, "\x00GH_REPO=") != 1 ||
		!strings.Contains(joined, "\x00GH_HOST=github.com\x00") ||
		!strings.Contains(joined, "\x00GH_REPO=github.com/example/project\x00") {
		t.Fatalf("deduplicated GitHub environment = %#v", environment)
	}
}

func TestProviderMergeCommitUsesExactTopologyAndBaseLease(t *testing.T) {
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
	if err := os.WriteFile(tracked, []byte("head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Head")
	head := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	tree := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD^{tree}")))
	adapter := &githubProviderAdapter{repositoryRoot: repository, remote: "origin", gitExecutable: "git"}
	mergeCommit, err := adapter.createProviderMergeCommit(context.Background(), base, head, tree, 42)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := workspace.DefaultLocalCommitGitAdapter().InspectCommit(context.Background(), repository, mergeCommit)
	if err != nil {
		t.Fatal(err)
	}
	parents := inspection.Parents()
	if inspection.Tree() != tree || len(parents) != 2 || parents[0] != base || parents[1] != head {
		t.Fatalf("provider merge topology = tree:%s parents:%v", inspection.Tree(), parents)
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitTest(t, repository, "init", "--bare", remote)
	runGitTest(t, repository, "push", remote, gitObjectHex(base)+":refs/heads/main")
	runGitTest(t, repository, "checkout", "-b", "drift", gitObjectHex(base))
	if err := os.WriteFile(tracked, []byte("drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Competing base update")
	drift := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	runGitTest(t, repository, "push", remote, gitObjectHex(drift)+":refs/heads/main")

	arguments := providerMergePushArguments("refs/heads/main", base, mergeCommit, remote)
	command := exec.Command("git", arguments...)
	command.Dir = repository
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("drifted base lease unexpectedly updated remote: %s", output)
	}
	remoteLine := strings.TrimSpace(runGitTest(t, repository, "ls-remote", remote, "refs/heads/main"))
	if !strings.HasPrefix(remoteLine, gitObjectHex(drift)+"\t") {
		t.Fatalf("drifted base changed despite rejected lease: %q", remoteLine)
	}
}

func TestGitHubRequiredChecksBindBranchProtectionAndLegacyStatuses(t *testing.T) {
	requirements, err := parseGitHubBranchRequirements([]byte(`{
  "required_status_checks": {
    "contexts": ["legacy/status", "app/check", "missing/check"],
    "checks": [{"context":"app/check","app_id":17}]
  },
  "required_pull_request_reviews": {"required_approving_review_count":1}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements.checks) != 3 || !requirements.reviewsRequired || requirements.evidence.IsZero() {
		t.Fatalf("branch requirements = %#v", requirements)
	}
	checks, err := requiredGitHubCheckStates(
		requirements,
		[]byte(`{"check_runs":[
  {"name":"app/check","status":"completed","conclusion":"success","app":{"id":17}},
  {"name":"optional/check","status":"completed","conclusion":"failure","app":{"id":22}}
]}`),
		[]byte(`[
  {"context":"legacy/status","state":"success"},
  {"context":"optional/status","state":"failure"}
]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 3 {
		t.Fatalf("required checks = %#v", checks)
	}
	conclusions := []workspace.ProviderCheckConclusion{
		checks[0].Conclusion(), checks[1].Conclusion(), checks[2].Conclusion(),
	}
	if fmt.Sprint(conclusions) != "[passed passed pending]" {
		t.Fatalf("required check conclusions = %v", conclusions)
	}
	for _, check := range checks {
		if !check.Required() || check.EvidenceDigest().IsZero() {
			t.Fatalf("required check lacks bound evidence: %#v", check)
		}
	}
	reviews, err := requiredGitHubReviewStates(
		requirements,
		[]byte(`{"data":{"repository":{"pullRequest":{"reviewDecision":"APPROVED"}}}}`),
	)
	if err != nil || len(reviews) != 1 || !reviews[0].Required() ||
		reviews[0].Conclusion() != workspace.ProviderReviewApproved || reviews[0].EvidenceDigest().IsZero() {
		t.Fatalf("required review state = %#v, %v", reviews, err)
	}
}

func TestGitHubOptionalChecksAndReviewsDoNotBecomeRequired(t *testing.T) {
	requirements, err := parseGitHubBranchRequirements([]byte(`{
  "required_status_checks": null,
  "required_pull_request_reviews": null
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements.checks) != 0 || requirements.reviewsRequired {
		t.Fatalf("optional provider observations became required: %#v", requirements)
	}
	checks, err := requiredGitHubCheckStates(requirements, []byte(`{"check_runs":[]}`), []byte(`[]`))
	if err != nil || len(checks) != 0 {
		t.Fatalf("optional checks = %#v, %v", checks, err)
	}
	reviews, err := requiredGitHubReviewStates(requirements, nil)
	if err != nil || len(reviews) != 0 {
		t.Fatalf("optional reviews = %#v, %v", reviews, err)
	}
}

func TestGitHubAdapterReconcilesOpenPullRequestWithoutCommandOutput(t *testing.T) {
	adapter := newFakeGitHubAdapter(t)
	base := testGitObject(t, "1")
	head := testGitObject(t, "2")
	intent := testProviderIntent(t, workspace.ProviderIntentOpenPullRequest, base, head)
	query, err := workspace.NewProviderIntentQuery(intent)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GH_PULLS", fmt.Sprintf(`[{"number":42,"state":"open","merged":false,"head":{"sha":%q,"ref":"mu-test"},"base":{"sha":%q,"ref":"main"}}]`, gitObjectHex(head), gitObjectHex(base)))
	observation, err := adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent applied open PR: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectApplied {
		t.Fatalf("open PR disposition = %s", observation.Disposition())
	}

	t.Setenv("FAKE_GH_PULLS", `[]`)
	observation, err = adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent absent open PR: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectNotApplied {
		t.Fatalf("absent PR disposition = %s", observation.Disposition())
	}
}

func TestProviderJSONRequiresExactlyOneValue(t *testing.T) {
	var value map[string]any
	if err := decodeProviderJSON([]byte("{\"ok\":true}\n"), &value); err != nil {
		t.Fatalf("valid provider JSON: %v", err)
	}
	for _, source := range []string{
		`{"ok":true}{"second":true}`,
		`{"ok":true} trailing`,
	} {
		if err := decodeProviderJSON([]byte(source), &value); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("provider JSON %q error = %v", source, err)
		}
	}
}

func TestProviderCommandResultExposesTypedEvidenceWithoutExecutableOutput(t *testing.T) {
	encoded, err := json.Marshal(ProviderCommandResult{
		SchemaVersion: 2,
		Status:        "recorded",
		Action:        "provider.dispatch",
		Detail: map[string]any{
			"intent_id": "intent-one", "status": "succeeded", "request_marker": "provider-marker",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"command", "provider_command", "argv", "executable", "remote_delete", "no_merge"} {
		if _, exists := document[forbidden]; exists || strings.Contains(string(encoded), `"`+forbidden+`"`) {
			t.Fatalf("provider result exposes forbidden executable surface %q: %s", forbidden, encoded)
		}
	}
}

func newFakeGitHubAdapter(t *testing.T) *githubProviderAdapter {
	t.Helper()
	directory := t.TempDir()
	gitExecutable := filepath.Join(directory, "fake-git")
	ghExecutable := filepath.Join(directory, "fake-gh")
	gitScript := `#!/bin/sh
case "$1" in
  remote) printf '%s\n' "${FAKE_GIT_REMOTE_URL:-https://github.com/example/project.git}" ;;
  ls-remote) printf '%s' "$FAKE_GIT_LS_REMOTE" ;;
  rev-parse)
    if [ "$2" = "--show-object-format" ]; then printf 'sha1\n'; else printf '%s\n' "$FAKE_GIT_TREE"; fi ;;
  *) printf 'unsupported fake git invocation: %s\n' "$*" >&2; exit 97 ;;
esac
`
	ghScript := `#!/bin/sh
case "$*" in
  *"repos/example/project/pulls"*) printf '%s' "$FAKE_GH_PULLS" ;;
  *) printf 'unsupported fake gh invocation: %s\n' "$*" >&2; exit 98 ;;
esac
`
	for path, content := range map[string]string{gitExecutable: gitScript, ghExecutable: ghScript} {
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := workspace.NewRepositoryIdentity("https://github.com/example/project.git")
	if err != nil {
		t.Fatal(err)
	}
	return &githubProviderAdapter{
		repositoryRoot: directory, repositoryIdentity: repository, providerRepository: "example/project", remote: "origin",
		gitExecutable: gitExecutable, ghExecutable: ghExecutable,
	}
}

func testProviderIntent(
	t *testing.T,
	kind workspace.ProviderIntentKind,
	base, head workspace.GitObjectID,
) workspace.ProviderIntent {
	t.Helper()
	repository, err := workspace.NewRepositoryIdentity("https://github.com/example/project.git")
	if err != nil {
		t.Fatal(err)
	}
	mergeUnit, err := workspace.NewMergeUnitReference(workspace.MustID("alpha-plan"), workspace.MustID("unit-one"))
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := workspace.NewAuthorizationFrontier(base, head)
	if err != nil {
		t.Fatal(err)
	}
	scope := workspace.ProviderIntentScopeOptions{
		WorkspaceID: workspace.MustID("workspace-one"), Generation: workspace.DigestBytes([]byte("generation")),
		AttemptID: workspace.MustID("attempt-one"), MergeUnit: mergeUnit, Repository: repository, Remote: "origin",
		SerialSegment: workspace.MustID("serial-one"), Frontier: frontier, Epoch: 1,
	}
	switch kind {
	case workspace.ProviderIntentPush:
		intent, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
			Scope: scope, Branch: "mu-test", ExpectRemoteAbsent: true, Head: head,
		})
		if err != nil {
			t.Fatal(err)
		}
		return intent
	case workspace.ProviderIntentOpenPullRequest:
		intent, err := workspace.NewProviderOpenPullRequestIntent(workspace.ProviderOpenPullRequestIntentOptions{
			Scope: scope, Branch: "mu-test", BaseRef: "main", Head: head, Tree: testGitObject(t, "4"),
			Title: "Test PR", Body: "Typed provider adapter test.",
		})
		if err != nil {
			t.Fatal(err)
		}
		return intent
	default:
		t.Fatalf("unsupported test intent kind %s", kind)
		return workspace.ProviderIntent{}
	}
}

func testGitObject(t *testing.T, digit string) workspace.GitObjectID {
	t.Helper()
	object, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat(digit, 40))
	if err != nil {
		t.Fatal(err)
	}
	return object
}

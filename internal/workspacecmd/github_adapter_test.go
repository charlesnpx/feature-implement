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
	setFakeGitState(t, adapter, "ls-remote", gitObjectHex(head)+"\trefs/heads/mu-test\n")
	observation, err := adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent applied push: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectApplied || observation.Digest().IsZero() {
		t.Fatalf("applied push observation = %#v", observation)
	}

	setFakeGitState(t, adapter, "ls-remote", "")
	observation, err = adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent absent push: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectNotApplied {
		t.Fatalf("absent push disposition = %s", observation.Disposition())
	}

	drift := testGitObject(t, "3")
	setFakeGitState(t, adapter, "ls-remote", gitObjectHex(drift)+"\trefs/heads/mu-test\n")
	observation, err = adapter.QueryIntent(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryIntent drifted push: %v", err)
	}
	if observation.Disposition() != workspace.ProviderEffectUnknown {
		t.Fatalf("drifted push disposition = %s", observation.Disposition())
	}

	setFakeGitState(t, adapter, "remote-url", "https://github.com/example/other.git\n")
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

func TestProviderGitEnvironmentPinsTransportAndCredentialBoundary(t *testing.T) {
	remote := canonicalGitHubRemoteURL("example/project")
	environment := providerGitProcessEnvironment([]string{
		"PATH=/usr/bin", "HOME=/ambient/home", "GH_TOKEN=ambient-token",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url.https://example.invalid/.insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/example/project.git",
		"GIT_SSH_COMMAND=redirect-ssh", "HTTPS_PROXY=https://proxy.invalid",
		"GIT_SSL_NO_VERIFY=1", "CURL_CA_BUNDLE=/tmp/attacker.pem",
	}, remote, "Authorization: Basic test-credential")
	joined := "\x00" + strings.Join(environment, "\x00") + "\x00"
	for _, forbidden := range []string{
		"ambient-token", "example.invalid", "redirect-ssh", "proxy.invalid", "attacker.pem",
		"GIT_SSL_NO_VERIFY=", "HTTPS_PROXY=", "GH_TOKEN=",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("provider Git environment retained %q: %#v", forbidden, environment)
		}
	}
	for _, required := range []string{
		"\x00HOME=" + os.DevNull + "\x00",
		"\x00GIT_CONFIG_GLOBAL=" + os.DevNull + "\x00",
		"\x00GIT_CONFIG_SYSTEM=" + os.DevNull + "\x00",
		"\x00GIT_ALLOW_PROTOCOL=https\x00",
		"\x00GIT_SSH_COMMAND=" + os.DevNull + "\x00",
		"url." + remote + ".insteadOf",
		"http." + remote + ".sslVerify",
		"Authorization: Basic test-credential",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("provider Git environment omitted %q: %#v", required, environment)
		}
	}
}

func TestProviderGitAuthorizationUsesPinnedGitHubCredential(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "fake-gh")
	script := `#!/bin/sh
if [ "$*" != "auth token --hostname github.com" ]; then exit 97; fi
printf '%s\n' 'test-token'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := &githubProviderAdapter{
		repositoryRoot: directory, providerRepository: "example/project", ghExecutable: executable,
	}
	authorization, err := adapter.githubGitAuthorization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authorization, "Authorization: Basic ") || strings.Contains(authorization, "test-token") {
		t.Fatalf("GitHub authorization was not encoded: %q", authorization)
	}
}

func TestProviderNetworkGitContextExcludesSourceLocalConfig(t *testing.T) {
	repository := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "Feature Test")
	runGitTest(t, repository, "config", "user.email", "feature@example.test")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Provider object")
	head := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	canonical := canonicalGitHubRemoteURL("example/project")
	maliciousKey := "url.https://example.invalid/.insteadOf"
	runGitTest(t, repository, "config", maliciousKey, canonical)

	adapter := &githubProviderAdapter{
		repositoryRoot: repository, providerRepository: "example/project", gitExecutable: "git",
	}
	execution, err := adapter.prepareProviderGitNetworkContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(execution.root) })
	environment := providerGitNetworkProcessEnvironment(os.Environ(), canonical, "", execution)

	localConfig := exec.Command("git", "config", "--local", "--get", maliciousKey)
	localConfig.Dir, localConfig.Env = execution.root, environment
	if output, err := localConfig.CombinedOutput(); err == nil {
		t.Fatalf("isolated provider Git loaded source local config: %s", output)
	}
	object := exec.Command("git", "cat-file", "-e", head+"^{commit}")
	object.Dir, object.Env = execution.root, environment
	if output, err := object.CombinedOutput(); err != nil {
		t.Fatalf("isolated provider Git cannot read approved objects: %v: %s", err, output)
	}
	expanded := exec.Command("git", "ls-remote", "--get-url", canonical)
	expanded.Dir, expanded.Env = execution.root, environment
	output, err := expanded.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != canonical {
		t.Fatalf("isolated provider Git URL = %q, %v", output, err)
	}
}

func TestGitHubPullRequestLifecycleIsExplicit(t *testing.T) {
	for input, expected := range map[string]workspace.ProviderPullRequestLifecycle{
		"open": workspace.ProviderPullRequestOpen, "CLOSED": workspace.ProviderPullRequestClosed,
	} {
		actual, err := parseGitHubPullRequestLifecycle(input)
		if err != nil || actual != expected {
			t.Fatalf("lifecycle %q = %q, %v", input, actual, err)
		}
	}
	if _, err := parseGitHubPullRequestLifecycle(""); err == nil {
		t.Fatal("missing GitHub pull request lifecycle was accepted")
	}
}

func TestProviderMergeCommitUsesExactTopologyAndAtomicRefLeases(t *testing.T) {
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

	runGitTest(t, repository, "checkout", "-b", "drift", gitObjectHex(base))
	if err := os.WriteFile(tracked, []byte("drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Competing base update")
	drift := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))

	newRemote := func(t *testing.T) string {
		t.Helper()
		remote := filepath.Join(t.TempDir(), "remote.git")
		runGitTest(t, repository, "init", "--bare", remote)
		runGitTest(t, repository, "push", remote,
			gitObjectHex(base)+":refs/heads/main", gitObjectHex(head)+":refs/heads/feature")
		return remote
	}
	push := func(remote string) ([]byte, error) {
		arguments := providerMergePushArguments(
			"refs/heads/main", base, mergeCommit,
			"refs/heads/feature", head, remote,
		)
		command := exec.Command("git", arguments...)
		command.Dir = repository
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		return command.CombinedOutput()
	}

	t.Run("success", func(t *testing.T) {
		remote := newRemote(t)
		if output, err := push(remote); err != nil {
			t.Fatalf("atomic merge push: %v: %s", err, output)
		}
		assertRemoteRef(t, repository, remote, "refs/heads/main", mergeCommit)
		assertRemoteRef(t, repository, remote, "refs/heads/feature", head)
	})
	t.Run("base drift", func(t *testing.T) {
		remote := newRemote(t)
		runGitTest(t, repository, "push", remote, gitObjectHex(drift)+":refs/heads/main")
		if output, err := push(remote); err == nil {
			t.Fatalf("drifted base lease unexpectedly updated remote: %s", output)
		}
		assertRemoteRef(t, repository, remote, "refs/heads/main", drift)
		assertRemoteRef(t, repository, remote, "refs/heads/feature", head)
	})
	t.Run("head drift", func(t *testing.T) {
		remote := newRemote(t)
		runGitTest(t, repository, "--git-dir", remote, "update-ref", "refs/heads/feature", gitObjectHex(base), gitObjectHex(head))
		if output, err := push(remote); err == nil {
			t.Fatalf("drifted head lease unexpectedly updated remote: %s", output)
		}
		assertRemoteRef(t, repository, remote, "refs/heads/main", base)
		assertRemoteRef(t, repository, remote, "refs/heads/feature", base)
	})
}

func assertRemoteRef(
	t *testing.T,
	repository, remote, ref string,
	expected workspace.GitObjectID,
) {
	t.Helper()
	line := strings.TrimSpace(runGitTest(t, repository, "ls-remote", remote, ref))
	if !strings.HasPrefix(line, gitObjectHex(expected)+"\t") {
		t.Fatalf("remote %s = %q, want %s", ref, line, expected)
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
script_dir=$(dirname "$0")
script_dir=$(cd "$script_dir" && pwd)
case "$1" in
  remote)
    if [ -f "$script_dir/fake-git-remote-url" ]; then cat "$script_dir/fake-git-remote-url"; else printf '%s\n' 'https://github.com/example/project.git'; fi ;;
  ls-remote)
    if [ -f "$script_dir/fake-git-ls-remote" ]; then cat "$script_dir/fake-git-ls-remote"; fi ;;
  rev-parse)
    if [ "$2" = "--show-object-format" ]; then
      printf 'sha1\n'
    elif [ "$2" = "--path-format=absolute" ]; then
      printf '%s\n' "$script_dir/fake-objects"
    elif [ -f "$script_dir/fake-git-tree" ]; then
      cat "$script_dir/fake-git-tree"
    fi ;;
  init)
    for argument in "$@"; do target=$argument; done
    mkdir -p "$target" ;;
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
	if err := os.Mkdir(filepath.Join(directory, "fake-objects"), 0o700); err != nil {
		t.Fatal(err)
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

func setFakeGitState(t *testing.T, adapter *githubProviderAdapter, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(adapter.repositoryRoot, "fake-git-"+name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
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

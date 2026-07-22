package workspacecmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type githubProviderAdapter struct {
	repositoryRoot     string
	repositoryIdentity workspace.RepositoryIdentity
	providerRepository string
	remote             string
	gitExecutable      string
	ghExecutable       string
}

func newGitHubProviderAdapter(manifest workspace.WorkspaceManifest) (*githubProviderAdapter, error) {
	if manifest.Provider().Kind().String() != "github" {
		return nil, fmt.Errorf("unsupported provider kind %s; workspace CLI supports github", manifest.Provider().Kind())
	}
	parts := strings.Split(manifest.Provider().Repository(), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("github provider repository must be owner/repository")
	}
	repository, err := githubRepositoryFromRemoteURL(manifest.Repository().String())
	if err != nil {
		return nil, fmt.Errorf("github repository identity: %w", err)
	}
	if !strings.EqualFold(repository, manifest.Provider().Repository()) {
		return nil, fmt.Errorf("repository identity %s does not match github provider repository %s", manifest.Repository(), manifest.Provider().Repository())
	}
	return &githubProviderAdapter{
		repositoryRoot: manifest.RepositoryRoot(), repositoryIdentity: manifest.Repository(),
		providerRepository: manifest.Provider().Repository(), remote: manifest.Remote(),
		gitExecutable: "git", ghExecutable: "gh",
	}, nil
}

func (adapter *githubProviderAdapter) Push(ctx context.Context, request workspace.ProviderPushRequest) (workspace.ProviderPushAdapterResult, error) {
	marker := "git-push:" + request.IdempotencyKey().String()
	if err := adapter.validateRequestRepository(request.Repository(), request.Remote()); err != nil {
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	remoteURL, err := adapter.configuredRemoteURL(ctx)
	if err != nil {
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	ref := "refs/heads/" + request.Branch()
	observed, exists, err := adapter.remoteRefURL(ctx, remoteURL, ref, request.Head().Algorithm())
	if err != nil {
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	expected, hasExpected := request.ExpectedRemoteHead()
	switch {
	case request.ExpectsRemoteAbsent() && exists:
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, fmt.Errorf("remote branch %s already exists at %s", request.Branch(), observed))
	case hasExpected && (!exists || observed != expected):
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, fmt.Errorf("remote branch %s does not match expected lease %s", request.Branch(), expected))
	case !request.ExpectsRemoteAbsent() && !hasExpected:
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, fmt.Errorf("push request has no atomic remote lease"))
	}
	lease := "--force-with-lease=" + ref + ":"
	if hasExpected {
		lease += gitObjectHex(expected)
	}
	_, err = adapter.runGitProviderWrite(ctx, remoteURL, "push", "--porcelain", lease, remoteURL, gitObjectHex(request.Head())+":"+ref)
	if err != nil {
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	remoteHead, exists, err := adapter.remoteRefURL(ctx, remoteURL, ref, request.Head().Algorithm())
	if err != nil || !exists || remoteHead != request.Head() {
		if err == nil {
			err = fmt.Errorf("remote branch %s did not converge to %s", request.Branch(), request.Head())
		}
		return workspace.ProviderPushAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	return workspace.NewProviderPushAdapterResult(marker, remoteHead)
}

func (adapter *githubProviderAdapter) OpenPullRequest(ctx context.Context, request workspace.ProviderOpenPullRequestRequest) (workspace.ProviderOpenPullRequestAdapterResult, error) {
	marker := "github-open-pr:" + request.IdempotencyKey().String()
	if err := adapter.validateRequestRepository(request.Repository(), request.Remote()); err != nil {
		return workspace.ProviderOpenPullRequestAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	existing, err := adapter.findPullRequests(ctx, request.Branch(), request.BaseRef(), "open")
	if err != nil {
		return workspace.ProviderOpenPullRequestAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	for _, pullRequest := range existing {
		head, err := parseGitHubObject(pullRequest.Head.SHA, request.Head().Algorithm())
		if err == nil && head == request.Head() {
			return workspace.NewProviderOpenPullRequestAdapterResult(marker, pullRequest.Number, head)
		}
	}
	if len(existing) != 0 {
		return workspace.ProviderOpenPullRequestAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, fmt.Errorf("existing pull request for branch %s has a different head", request.Branch()))
	}
	output, err := adapter.runGH(ctx,
		"api", "--method", "POST", "repos/"+adapter.providerRepository+"/pulls",
		"--raw-field", "title="+request.Title(), "--raw-field", "head="+request.Branch(),
		"--raw-field", "base="+request.BaseRef(), "--raw-field", "body="+request.Body(),
	)
	if err != nil {
		return workspace.ProviderOpenPullRequestAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	var created githubPullRequest
	if err := decodeProviderJSON(output, &created); err != nil {
		return workspace.ProviderOpenPullRequestAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	head, err := parseGitHubObject(created.Head.SHA, request.Head().Algorithm())
	if err != nil || created.Number == 0 || head != request.Head() {
		if err == nil {
			err = fmt.Errorf("created pull request does not match exact requested head")
		}
		return workspace.ProviderOpenPullRequestAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	return workspace.NewProviderOpenPullRequestAdapterResult(marker, created.Number, head)
}

func (adapter *githubProviderAdapter) Merge(ctx context.Context, request workspace.ProviderMergeRequest) (workspace.ProviderMergeAdapterResult, error) {
	marker := "github-merge:" + request.IdempotencyKey().String()
	if err := adapter.validateRequestRepository(request.Repository(), request.Remote()); err != nil {
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	if request.Strategy() != workspace.ProviderMergeCommit {
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, fmt.Errorf("only merge_commit is supported"))
	}
	if request.Branch() == request.BaseRef() {
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, fmt.Errorf("merge head and base refs must be distinct"))
	}
	baseHead, exists, err := adapter.remoteRef(ctx, "refs/heads/"+request.BaseRef(), request.Head().Algorithm())
	if err != nil || !exists || baseHead != request.ExpectedBaseHead() {
		if err == nil {
			err = fmt.Errorf("integration base %s does not match expected head %s", request.BaseRef(), request.ExpectedBaseHead())
		}
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	remoteURL, err := adapter.configuredRemoteURL(ctx)
	if err != nil {
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	mergeCommit, err := adapter.createProviderMergeCommit(
		ctx, request.ExpectedBaseHead(), request.Head(), request.Tree(), request.PullRequest().Number(),
	)
	if err != nil {
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterFailedBeforeEffect, marker, err)
	}
	baseRef := "refs/heads/" + request.BaseRef()
	headRef := "refs/heads/" + request.Branch()
	if _, err := adapter.runGitProviderWrite(ctx, remoteURL, providerMergePushArguments(
		baseRef, request.ExpectedBaseHead(), mergeCommit,
		headRef, request.Head(), remoteURL,
	)...); err != nil {
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	finalBase, exists, err := adapter.remoteRef(ctx, "refs/heads/"+request.BaseRef(), request.Head().Algorithm())
	if err != nil || !exists || finalBase != mergeCommit {
		if err == nil {
			err = fmt.Errorf("integration base did not advance to provider merge commit %s", mergeCommit)
		}
		return workspace.ProviderMergeAdapterResult{}, providerAdapterFailure(workspace.ProviderAdapterAmbiguous, marker, err)
	}
	return workspace.NewProviderMergeAdapterResult(marker, mergeCommit, finalBase)
}

func (adapter *githubProviderAdapter) createProviderMergeCommit(
	ctx context.Context,
	base, head, tree workspace.GitObjectID,
	pullRequest uint64,
) (workspace.GitObjectID, error) {
	if base.IsZero() || head.IsZero() || tree.IsZero() || pullRequest == 0 ||
		base.Algorithm() != head.Algorithm() || head.Algorithm() != tree.Algorithm() {
		return workspace.GitObjectID{}, fmt.Errorf("provider merge commit requires exact algorithm-matched topology")
	}
	observedTree, err := adapter.commitTree(ctx, head)
	if err != nil || observedTree != tree {
		if err == nil {
			err = fmt.Errorf("provider merge head tree %s does not match expected tree %s", observedTree, tree)
		}
		return workspace.GitObjectID{}, err
	}
	if _, err := adapter.runGit(
		ctx, "merge-base", "--is-ancestor", gitObjectHex(base), gitObjectHex(head),
	); err != nil {
		return workspace.GitObjectID{}, fmt.Errorf("provider merge head does not descend from approved base: %w", err)
	}
	output, err := adapter.runGitLocalWrite(
		ctx,
		"-c", "user.name=feature-implement",
		"-c", "user.email=feature-implement@users.noreply.github.com",
		"commit-tree", gitObjectHex(tree),
		"-p", gitObjectHex(base), "-p", gitObjectHex(head),
		"-m", fmt.Sprintf("Merge pull request #%d", pullRequest),
	)
	if err != nil {
		return workspace.GitObjectID{}, err
	}
	mergeCommit, err := parseGitHubObject(strings.TrimSpace(string(output)), head.Algorithm())
	if err != nil {
		return workspace.GitObjectID{}, fmt.Errorf("parse provider merge commit: %w", err)
	}
	inspection, err := workspace.DefaultLocalCommitGitAdapter().InspectCommit(ctx, adapter.repositoryRoot, mergeCommit)
	if err != nil {
		return workspace.GitObjectID{}, fmt.Errorf("inspect provider merge commit: %w", err)
	}
	parents := inspection.Parents()
	if inspection.Tree() != tree || len(parents) != 2 || parents[0] != base || parents[1] != head {
		return workspace.GitObjectID{}, fmt.Errorf("provider merge commit does not match approved two-parent topology")
	}
	return mergeCommit, nil
}

func providerMergePushArguments(
	baseRef string,
	expectedBase, mergeCommit workspace.GitObjectID,
	headRef string,
	expectedHead workspace.GitObjectID,
	remoteURL string,
) []string {
	baseLease := "--force-with-lease=" + baseRef + ":" + gitObjectHex(expectedBase)
	headLease := "--force-with-lease=" + headRef + ":" + gitObjectHex(expectedHead)
	return []string{
		"push", "--porcelain", "--atomic", baseLease, headLease, remoteURL,
		gitObjectHex(mergeCommit) + ":" + baseRef,
		gitObjectHex(expectedHead) + ":" + headRef,
	}
}

func (adapter *githubProviderAdapter) QueryIntent(ctx context.Context, query workspace.ProviderIntentQuery) (workspace.ProviderReconciliationObservation, error) {
	if err := adapter.validateRequestRepository(query.Repository(), query.Remote()); err != nil {
		return workspace.ProviderReconciliationObservation{}, err
	}
	marker := "github-query:" + query.IdempotencyKey().String()
	options := workspace.ProviderReconciliationObservationOptions{RequestMarker: marker}
	switch query.Kind() {
	case workspace.ProviderIntentPush:
		remoteHead, exists, err := adapter.remoteRef(ctx, "refs/heads/"+query.Branch(), query.Head().Algorithm())
		if err != nil {
			return workspace.ProviderReconciliationObservation{}, err
		}
		expected, hasExpected := query.ExpectedRemoteHead()
		switch {
		case exists && remoteHead == query.Head():
			options.Disposition, options.RemoteHead = workspace.ProviderEffectApplied, remoteHead
		case query.ExpectsRemoteAbsent() && !exists:
			options.Disposition = workspace.ProviderEffectNotApplied
		case hasExpected && exists && remoteHead == expected:
			options.Disposition = workspace.ProviderEffectNotApplied
		default:
			options.Disposition = workspace.ProviderEffectUnknown
		}
	case workspace.ProviderIntentOpenPullRequest:
		pullRequests, err := adapter.findPullRequests(ctx, query.Branch(), query.BaseRef(), "all")
		if err != nil {
			return workspace.ProviderReconciliationObservation{}, err
		}
		matches := make([]githubPullRequest, 0, 1)
		for _, pullRequest := range pullRequests {
			head, parseErr := parseGitHubObject(pullRequest.Head.SHA, query.Head().Algorithm())
			if parseErr == nil && head == query.Head() {
				matches = append(matches, pullRequest)
			}
		}
		switch {
		case len(matches) == 1:
			options.Disposition = workspace.ProviderEffectApplied
			options.PullRequestNumber = matches[0].Number
			options.PullRequestHead = query.Head()
		case len(matches) == 0 && len(pullRequests) == 0:
			options.Disposition = workspace.ProviderEffectNotApplied
		default:
			options.Disposition = workspace.ProviderEffectUnknown
		}
	case workspace.ProviderIntentMerge:
		pullRequest, exists := query.PullRequest()
		if !exists {
			return workspace.ProviderReconciliationObservation{}, fmt.Errorf("merge query has no provider-derived pull request")
		}
		prQuery, err := workspace.NewProviderPullRequestQuery(query.Repository(), pullRequest)
		if err != nil {
			return workspace.ProviderReconciliationObservation{}, err
		}
		state, err := adapter.QueryPullRequest(ctx, prQuery)
		if err != nil {
			return workspace.ProviderReconciliationObservation{}, err
		}
		exact := state.BaseRef() == query.BaseRef() && state.Branch() == query.Branch() &&
			state.Head() == query.Head() && state.HeadTree() == query.Tree() &&
			state.RemoteBranchHead() == query.Head() && state.BaseHeadBeforeMerge() == query.IntegrationBaseHead()
		switch {
		case !exact:
			options.Disposition = workspace.ProviderEffectUnknown
		case !state.Merged():
			options.Disposition = workspace.ProviderEffectNotApplied
		case state.MergeStrategy() == query.MergeStrategy() && state.FinalBaseHead() == state.MergeCommit():
			options.Disposition = workspace.ProviderEffectApplied
			options.MergeCommit = state.MergeCommit()
			options.FinalBaseHead = state.FinalBaseHead()
		default:
			options.Disposition = workspace.ProviderEffectUnknown
		}
	default:
		return workspace.ProviderReconciliationObservation{}, fmt.Errorf("unsupported provider query kind")
	}
	return workspace.NewProviderReconciliationObservation(options)
}

func (adapter *githubProviderAdapter) QueryPullRequest(ctx context.Context, query workspace.ProviderPullRequestQuery) (workspace.ProviderPullRequestState, error) {
	if query.Repository() != adapter.repositoryIdentity {
		return workspace.ProviderPullRequestState{}, fmt.Errorf("pull request query repository does not match provider adapter")
	}
	output, err := adapter.runGH(ctx, "api", fmt.Sprintf("repos/%s/pulls/%d", adapter.providerRepository, query.PullRequest().Number()))
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	var pullRequest githubPullRequest
	if err := decodeProviderJSON(output, &pullRequest); err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	algorithm, err := adapter.objectFormat(ctx)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	head, err := parseGitHubObject(pullRequest.Head.SHA, algorithm)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	headTree, err := adapter.commitTree(ctx, head)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	remoteBranch, _, err := adapter.remoteRef(ctx, "refs/heads/"+pullRequest.Head.Ref, algorithm)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	baseBefore, err := parseGitHubObject(pullRequest.Base.SHA, algorithm)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	requirements, err := adapter.queryBranchRequirements(ctx, pullRequest.Base.Ref)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	checks, err := adapter.queryChecks(ctx, pullRequest.Head.SHA, requirements)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	reviews, err := adapter.queryReviews(ctx, pullRequest.Number, requirements)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	merged := pullRequest.Merged || pullRequest.MergedAt != ""
	lifecycle, err := parseGitHubPullRequestLifecycle(pullRequest.State)
	if err != nil {
		return workspace.ProviderPullRequestState{}, err
	}
	mergeCommit := workspace.GitObjectID{}
	finalBase := workspace.GitObjectID{}
	strategy := workspace.ProviderMergeStrategy("")
	if merged {
		mergeCommit, err = parseGitHubObject(pullRequest.MergeCommitSHA, algorithm)
		if err != nil {
			return workspace.ProviderPullRequestState{}, err
		}
		finalBase, _, err = adapter.remoteRef(ctx, "refs/heads/"+pullRequest.Base.Ref, algorithm)
		if err != nil {
			return workspace.ProviderPullRequestState{}, err
		}
		strategy = workspace.ProviderMergeCommit
	}
	marker := fmt.Sprintf("github-pr:%d:%s", pullRequest.Number, pullRequest.UpdatedAt)
	return workspace.NewProviderPullRequestState(workspace.ProviderPullRequestStateOptions{
		Repository: adapter.repositoryIdentity, PullRequest: query.PullRequest(), BaseRef: pullRequest.Base.Ref,
		Branch: pullRequest.Head.Ref, Head: head, HeadTree: headTree, RemoteBranchHead: remoteBranch,
		BaseHeadBeforeMerge: baseBefore, Checks: checks, Reviews: reviews, Merged: merged,
		Lifecycle:     lifecycle,
		MergeStrategy: strategy, MergeCommit: mergeCommit, FinalBaseHead: finalBase, RequestMarker: marker,
	})
}

func parseGitHubPullRequestLifecycle(value string) (workspace.ProviderPullRequestLifecycle, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "open":
		return workspace.ProviderPullRequestOpen, nil
	case "closed":
		return workspace.ProviderPullRequestClosed, nil
	default:
		return "", fmt.Errorf("unsupported GitHub pull request state %q", value)
	}
}

type githubPullRequest struct {
	Number         uint64 `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	UpdatedAt      string `json:"updated_at"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
}

func (adapter *githubProviderAdapter) findPullRequests(ctx context.Context, branch, base, state string) ([]githubPullRequest, error) {
	if state != "open" && state != "all" {
		return nil, fmt.Errorf("unsupported pull request query state %q", state)
	}
	owner := strings.SplitN(adapter.providerRepository, "/", 2)[0]
	output, err := adapter.runGH(ctx,
		"api", "--method", "GET", "repos/"+adapter.providerRepository+"/pulls",
		"--raw-field", "state="+state, "--raw-field", "head="+owner+":"+branch, "--raw-field", "base="+base,
	)
	if err != nil {
		return nil, err
	}
	var values []githubPullRequest
	if err := decodeProviderJSON(output, &values); err != nil {
		return nil, err
	}
	return values, nil
}

type githubRequiredCheck struct {
	context string
	appID   *int64
}

type githubBranchRequirements struct {
	checks          []githubRequiredCheck
	reviewsRequired bool
	evidence        workspace.Digest
}

func (adapter *githubProviderAdapter) queryBranchRequirements(
	ctx context.Context,
	baseRef string,
) (githubBranchRequirements, error) {
	escaped := url.PathEscape(baseRef)
	output, err := adapter.runGH(ctx, "api", "repos/"+adapter.providerRepository+"/branches/"+escaped)
	if err != nil {
		return githubBranchRequirements{}, fmt.Errorf("query GitHub base branch protection marker: %w", err)
	}
	var branch struct {
		Protected bool `json:"protected"`
	}
	if err := decodeProviderJSON(output, &branch); err != nil {
		return githubBranchRequirements{}, err
	}
	if !branch.Protected {
		return githubBranchRequirements{checks: []githubRequiredCheck{}}, nil
	}
	output, err = adapter.runGH(ctx, "api", "repos/"+adapter.providerRepository+"/branches/"+escaped+"/protection")
	if err != nil {
		return githubBranchRequirements{}, fmt.Errorf("query GitHub branch protection requirements: %w", err)
	}
	return parseGitHubBranchRequirements(output)
}

func parseGitHubBranchRequirements(source []byte) (githubBranchRequirements, error) {
	var document struct {
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
				AppID   *int64 `json:"app_id"`
			} `json:"checks"`
		} `json:"required_status_checks"`
		RequiredPullRequestReviews *json.RawMessage `json:"required_pull_request_reviews"`
	}
	if err := decodeProviderJSON(source, &document); err != nil {
		return githubBranchRequirements{}, err
	}
	requirements := githubBranchRequirements{
		checks: []githubRequiredCheck{}, evidence: workspace.DigestBytes(source),
		reviewsRequired: document.RequiredPullRequestReviews != nil &&
			string(*document.RequiredPullRequestReviews) != "null",
	}
	seen := map[string]struct{}{}
	if document.RequiredStatusChecks != nil {
		for _, check := range document.RequiredStatusChecks.Checks {
			context := strings.TrimSpace(check.Context)
			if context == "" {
				return githubBranchRequirements{}, fmt.Errorf("GitHub branch protection contains an empty required check context")
			}
			key := context + "\x00"
			if check.AppID != nil {
				key += fmt.Sprint(*check.AppID)
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			requirements.checks = append(requirements.checks, githubRequiredCheck{context: context, appID: check.AppID})
		}
		for _, value := range document.RequiredStatusChecks.Contexts {
			context := strings.TrimSpace(value)
			if context == "" {
				return githubBranchRequirements{}, fmt.Errorf("GitHub branch protection contains an empty required status context")
			}
			alreadyRepresented := false
			for _, check := range requirements.checks {
				if check.context == context {
					alreadyRepresented = true
					break
				}
			}
			if !alreadyRepresented {
				requirements.checks = append(requirements.checks, githubRequiredCheck{context: context})
			}
		}
	}
	sort.Slice(requirements.checks, func(i, j int) bool {
		left, right := requirements.checks[i], requirements.checks[j]
		if left.context != right.context {
			return left.context < right.context
		}
		return githubRequiredCheckAppID(left) < githubRequiredCheckAppID(right)
	})
	return requirements, nil
}

func githubRequiredCheckAppID(check githubRequiredCheck) int64 {
	if check.appID == nil {
		return -1
	}
	return *check.appID
}

func (adapter *githubProviderAdapter) queryChecks(
	ctx context.Context,
	head string,
	requirements githubBranchRequirements,
) ([]workspace.ProviderCheckState, error) {
	if len(requirements.checks) == 0 {
		return []workspace.ProviderCheckState{}, nil
	}
	checkRuns, err := adapter.runGH(ctx, "api", "repos/"+adapter.providerRepository+"/commits/"+head+"/check-runs?per_page=100")
	if err != nil {
		return nil, err
	}
	statuses, err := adapter.runGH(ctx, "api", "repos/"+adapter.providerRepository+"/commits/"+head+"/statuses?per_page=100")
	if err != nil {
		return nil, err
	}
	return requiredGitHubCheckStates(requirements, checkRuns, statuses)
}

func requiredGitHubCheckStates(
	requirements githubBranchRequirements,
	checkRunsSource []byte,
	statusesSource []byte,
) ([]workspace.ProviderCheckState, error) {
	var checkRunDocument struct {
		CheckRuns []json.RawMessage `json:"check_runs"`
	}
	if err := decodeProviderJSON(checkRunsSource, &checkRunDocument); err != nil {
		return nil, err
	}
	type checkRunObservation struct {
		name       string
		appID      int64
		conclusion workspace.ProviderCheckConclusion
		evidence   workspace.Digest
	}
	checkRuns := make([]checkRunObservation, 0, len(checkRunDocument.CheckRuns))
	for _, raw := range checkRunDocument.CheckRuns {
		var item struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			App        struct {
				ID int64 `json:"id"`
			} `json:"app"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		conclusion := workspace.ProviderCheckPending
		if item.Status == "completed" {
			switch item.Conclusion {
			case "success", "neutral", "skipped":
				conclusion = workspace.ProviderCheckPassed
			default:
				conclusion = workspace.ProviderCheckFailed
			}
		}
		checkRuns = append(checkRuns, checkRunObservation{
			name: strings.TrimSpace(item.Name), appID: item.App.ID, conclusion: conclusion,
			evidence: combinedProviderEvidence(requirements.evidence, raw),
		})
	}
	var statusDocuments []json.RawMessage
	if err := decodeProviderJSON(statusesSource, &statusDocuments); err != nil {
		return nil, err
	}
	type statusObservation struct {
		context    string
		conclusion workspace.ProviderCheckConclusion
		evidence   workspace.Digest
	}
	statuses := make([]statusObservation, 0, len(statusDocuments))
	seenStatuses := map[string]struct{}{}
	for _, raw := range statusDocuments {
		var item struct {
			Context string `json:"context"`
			State   string `json:"state"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.Context = strings.TrimSpace(item.Context)
		if item.Context == "" {
			continue
		}
		if _, exists := seenStatuses[item.Context]; exists {
			continue
		}
		seenStatuses[item.Context] = struct{}{}
		conclusion := workspace.ProviderCheckPending
		switch item.State {
		case "success":
			conclusion = workspace.ProviderCheckPassed
		case "failure", "error":
			conclusion = workspace.ProviderCheckFailed
		}
		statuses = append(statuses, statusObservation{
			context: item.Context, conclusion: conclusion,
			evidence: combinedProviderEvidence(requirements.evidence, raw),
		})
	}
	result := make([]workspace.ProviderCheckState, 0, len(requirements.checks))
	for _, required := range requirements.checks {
		conclusion := workspace.ProviderCheckPending
		evidence := requirements.evidence
		for _, observed := range checkRuns {
			if observed.name == required.context && (required.appID == nil || observed.appID == *required.appID) {
				conclusion, evidence = observed.conclusion, observed.evidence
				break
			}
		}
		if conclusion == workspace.ProviderCheckPending && required.appID == nil {
			for _, observed := range statuses {
				if observed.context == required.context {
					conclusion, evidence = observed.conclusion, observed.evidence
					break
				}
			}
		}
		identity := required.context + fmt.Sprintf(":%d", githubRequiredCheckAppID(required))
		id, _ := workspace.NewID("check-" + shortDigest([]byte(identity)))
		state, err := workspace.NewProviderCheckState(id, true, conclusion, evidence)
		if err != nil {
			return nil, err
		}
		result = append(result, state)
	}
	return result, nil
}

func (adapter *githubProviderAdapter) queryReviews(
	ctx context.Context,
	number uint64,
	requirements githubBranchRequirements,
) ([]workspace.ProviderReviewState, error) {
	if !requirements.reviewsRequired {
		return []workspace.ProviderReviewState{}, nil
	}
	parts := strings.SplitN(adapter.providerRepository, "/", 2)
	query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewDecision}}}`
	output, err := adapter.runGH(ctx, "api", "graphql", "--raw-field", "query="+query,
		"--raw-field", "owner="+parts[0], "--raw-field", "name="+parts[1], "-F", fmt.Sprintf("number=%d", number))
	if err != nil {
		return nil, err
	}
	return requiredGitHubReviewStates(requirements, output)
}

func requiredGitHubReviewStates(
	requirements githubBranchRequirements,
	source []byte,
) ([]workspace.ProviderReviewState, error) {
	if !requirements.reviewsRequired {
		return []workspace.ProviderReviewState{}, nil
	}
	var document struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					ReviewDecision string `json:"reviewDecision"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := decodeProviderJSON(source, &document); err != nil {
		return nil, err
	}
	if document.Data.Repository.PullRequest == nil {
		return nil, fmt.Errorf("GitHub review-decision query returned no pull request")
	}
	conclusion := workspace.ProviderReviewPending
	switch document.Data.Repository.PullRequest.ReviewDecision {
	case "APPROVED":
		conclusion = workspace.ProviderReviewApproved
	case "CHANGES_REQUESTED":
		conclusion = workspace.ProviderReviewChangesRequested
	case "", "REVIEW_REQUIRED":
	default:
		return nil, fmt.Errorf("unsupported GitHub review decision %q", document.Data.Repository.PullRequest.ReviewDecision)
	}
	id, _ := workspace.NewID("review-" + shortDigest([]byte("branch-protection-review-decision")))
	state, err := workspace.NewProviderReviewState(
		id, true, conclusion, combinedProviderEvidence(requirements.evidence, source),
	)
	if err != nil {
		return nil, err
	}
	return []workspace.ProviderReviewState{state}, nil
}

func combinedProviderEvidence(required workspace.Digest, observed []byte) workspace.Digest {
	return workspace.DigestBytes(append([]byte(required.String()+"\n"), observed...))
}

func (adapter *githubProviderAdapter) validateRequestRepository(repository workspace.RepositoryIdentity, remote string) error {
	if repository != adapter.repositoryIdentity || remote != adapter.remote {
		return fmt.Errorf("provider request repository or remote does not match workspace")
	}
	return nil
}

func (adapter *githubProviderAdapter) remoteRef(ctx context.Context, ref string, algorithm workspace.GitHashAlgorithm) (workspace.GitObjectID, bool, error) {
	remoteURL, err := adapter.configuredRemoteURL(ctx)
	if err != nil {
		return workspace.GitObjectID{}, false, err
	}
	return adapter.remoteRefURL(ctx, remoteURL, ref, algorithm)
}

func (adapter *githubProviderAdapter) remoteRefURL(
	ctx context.Context,
	remoteURL, ref string,
	algorithm workspace.GitHashAlgorithm,
) (workspace.GitObjectID, bool, error) {
	output, err := adapter.runGitRemoteRead(ctx, remoteURL, "ls-remote", "--refs", remoteURL, ref)
	if err != nil {
		return workspace.GitObjectID{}, false, err
	}
	line := strings.TrimSpace(string(output))
	if line == "" {
		return workspace.GitObjectID{}, false, nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[1] != ref {
		return workspace.GitObjectID{}, false, fmt.Errorf("unexpected ls-remote response for %s", ref)
	}
	object, err := parseGitHubObject(fields[0], algorithm)
	return object, err == nil, err
}

func (adapter *githubProviderAdapter) configuredRemoteURL(ctx context.Context) (string, error) {
	output, err := adapter.runGit(ctx, "remote", "get-url", "--push", adapter.remote)
	if err != nil {
		return "", fmt.Errorf("resolve configured remote %s: %w", adapter.remote, err)
	}
	remoteURL := strings.TrimSpace(string(output))
	if remoteURL == "" || strings.ContainsAny(remoteURL, "\r\n") {
		return "", fmt.Errorf("configured remote %s has no single push URL", adapter.remote)
	}
	repository, err := githubRepositoryFromRemoteURL(remoteURL)
	if err != nil {
		return "", fmt.Errorf("configured remote %s: %w", adapter.remote, err)
	}
	if !strings.EqualFold(repository, adapter.providerRepository) {
		return "", fmt.Errorf("configured remote %s targets %s, not github provider repository %s", adapter.remote, repository, adapter.providerRepository)
	}
	return canonicalGitHubRemoteURL(adapter.providerRepository), nil
}

func canonicalGitHubRemoteURL(repository string) string {
	return "https://github.com/" + repository + ".git"
}

func githubRepositoryFromRemoteURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("remote URL is required")
	}
	host, repositoryPath := "", ""
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Hostname() == "" {
			return "", fmt.Errorf("remote URL is invalid")
		}
		if parsed.User != nil && !strings.EqualFold(parsed.Scheme, "ssh") {
			return "", fmt.Errorf("remote URL must not contain embedded credentials")
		}
		host, repositoryPath = parsed.Hostname(), strings.TrimPrefix(parsed.Path, "/")
	} else if at := strings.LastIndex(value, "@"); at >= 0 {
		hostAndPath := value[at+1:]
		separator := strings.IndexByte(hostAndPath, ':')
		if separator <= 0 {
			return "", fmt.Errorf("remote URL is invalid")
		}
		host, repositoryPath = hostAndPath[:separator], hostAndPath[separator+1:]
	} else {
		return "", fmt.Errorf("remote URL must identify github.com")
	}
	if !strings.EqualFold(host, "github.com") {
		return "", fmt.Errorf("remote host %s is not github.com", host)
	}
	repositoryPath = strings.TrimSuffix(strings.Trim(repositoryPath, "/"), ".git")
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("remote URL path must be owner/repository")
	}
	return parts[0] + "/" + parts[1], nil
}

func (adapter *githubProviderAdapter) objectFormat(ctx context.Context) (workspace.GitHashAlgorithm, error) {
	output, err := adapter.runGit(ctx, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	switch value {
	case "sha1":
		return workspace.GitHashSHA1, nil
	case "sha256":
		return workspace.GitHashSHA256, nil
	default:
		return "", fmt.Errorf("unsupported Git object format %q", value)
	}
}

func (adapter *githubProviderAdapter) commitTree(ctx context.Context, commit workspace.GitObjectID) (workspace.GitObjectID, error) {
	output, err := adapter.runGit(ctx, "rev-parse", gitObjectHex(commit)+"^{tree}")
	if err != nil {
		return workspace.GitObjectID{}, err
	}
	return parseGitHubObject(strings.TrimSpace(string(output)), commit.Algorithm())
}

func (adapter *githubProviderAdapter) runGit(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, adapter.gitExecutable, arguments...)
	command.Dir = adapter.repositoryRoot
	command.Env = providerLocalGitProcessEnvironment(os.Environ())
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (adapter *githubProviderAdapter) runGitRemoteRead(
	ctx context.Context,
	remoteURL string,
	arguments ...string,
) ([]byte, error) {
	output, err := adapter.runGitProviderNetwork(ctx, remoteURL, "", arguments...)
	if err == nil {
		return output, nil
	}
	authorization, authErr := adapter.githubGitAuthorization(ctx)
	if authErr != nil {
		return nil, fmt.Errorf("git provider read from configured remote %s failed authentication", adapter.remote)
	}
	output, err = adapter.runGitProviderNetwork(ctx, remoteURL, authorization, arguments...)
	if err != nil {
		return nil, fmt.Errorf("git provider read from configured remote %s failed", adapter.remote)
	}
	return output, nil
}

func (adapter *githubProviderAdapter) runGitLocalWrite(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, adapter.gitExecutable, arguments...)
	command.Dir = adapter.repositoryRoot
	command.Env = providerLocalGitProcessEnvironment(os.Environ())
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil {
		return nil, fmt.Errorf("git provider local write: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (adapter *githubProviderAdapter) runGitProviderWrite(
	ctx context.Context,
	remoteURL string,
	arguments ...string,
) ([]byte, error) {
	authorization, err := adapter.githubGitAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	output, err := adapter.runGitProviderNetwork(ctx, remoteURL, authorization, arguments...)
	if err != nil {
		return nil, fmt.Errorf("git provider write to configured remote %s: %w", adapter.remote, err)
	}
	return output, nil
}

func (adapter *githubProviderAdapter) runGitProviderNetwork(
	ctx context.Context,
	remoteURL, authorization string,
	arguments ...string,
) ([]byte, error) {
	if remoteURL != canonicalGitHubRemoteURL(adapter.providerRepository) {
		return nil, fmt.Errorf("provider Git transport requires canonical GitHub HTTPS URL")
	}
	execution, err := adapter.prepareProviderGitNetworkContext(ctx)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(execution.root)
	command := exec.CommandContext(ctx, adapter.gitExecutable, arguments...)
	command.Dir = execution.root
	command.Env = providerGitNetworkProcessEnvironment(os.Environ(), remoteURL, authorization, execution)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

type providerGitNetworkContext struct {
	root            string
	gitDirectory    string
	objectDirectory string
}

func (adapter *githubProviderAdapter) prepareProviderGitNetworkContext(
	ctx context.Context,
) (providerGitNetworkContext, error) {
	algorithm, err := adapter.objectFormat(ctx)
	if err != nil {
		return providerGitNetworkContext{}, fmt.Errorf("resolve provider Git object format: %w", err)
	}
	objectDirectory, err := adapter.providerGitObjectDirectory(ctx)
	if err != nil {
		return providerGitNetworkContext{}, err
	}
	root, err := os.MkdirTemp("", "feature-provider-git-")
	if err != nil {
		return providerGitNetworkContext{}, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return providerGitNetworkContext{}, err
	}
	root = canonicalRoot
	gitDirectory := filepath.Join(root, "repository.git")
	command := exec.CommandContext(
		ctx, adapter.gitExecutable,
		"init", "--bare", "--quiet", "--object-format="+string(algorithm), "--", gitDirectory,
	)
	command.Dir = root
	command.Env = providerLocalGitProcessEnvironment(os.Environ())
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = io.Discard, &stderr
	if err := command.Run(); err != nil {
		_ = os.RemoveAll(root)
		return providerGitNetworkContext{}, fmt.Errorf("initialize isolated provider Git context: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	gitDirectory, err = filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		_ = os.RemoveAll(root)
		return providerGitNetworkContext{}, fmt.Errorf("resolve isolated provider Git directory: %w", err)
	}
	return providerGitNetworkContext{
		root: root, gitDirectory: gitDirectory, objectDirectory: objectDirectory,
	}, nil
}

func (adapter *githubProviderAdapter) providerGitObjectDirectory(ctx context.Context) (string, error) {
	output, err := adapter.runGit(ctx, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", fmt.Errorf("resolve provider Git object directory: %w", err)
	}
	directory := strings.TrimSpace(string(output))
	if directory == "" || strings.ContainsAny(directory, "\r\n") || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("provider Git object directory is not one absolute path")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve provider Git object directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("provider Git object directory is unavailable")
	}
	return directory, nil
}

func (adapter *githubProviderAdapter) githubGitAuthorization(ctx context.Context) (string, error) {
	output, err := adapter.runGH(ctx, "auth", "token", "--hostname", "github.com")
	if err != nil {
		return "", fmt.Errorf("resolve pinned GitHub credential: %w", err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" || len(token) > 16*1024 || strings.ContainsAny(token, "\x00\r\n\t ") {
		return "", fmt.Errorf("pinned GitHub credential is empty or malformed")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return "Authorization: Basic " + encoded, nil
}

type providerGitConfigEntry struct {
	key   string
	value string
}

func providerLocalGitProcessEnvironment(base []string) []string {
	return providerGitEnvironment(base, "", "", nil)
}

func providerGitProcessEnvironment(base []string, remoteURL, authorization string) []string {
	return providerGitEnvironment(base, remoteURL, authorization, nil)
}

func providerGitNetworkProcessEnvironment(
	base []string,
	remoteURL, authorization string,
	execution providerGitNetworkContext,
) []string {
	return providerGitEnvironment(base, remoteURL, authorization, map[string]string{
		"GIT_CEILING_DIRECTORIES": execution.root,
		"GIT_DIR":                 execution.gitDirectory,
		"GIT_OBJECT_DIRECTORY":    execution.objectDirectory,
	})
}

func providerGitEnvironment(
	base []string,
	remoteURL, authorization string,
	fixed map[string]string,
) []string {
	values := make(map[string]string)
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "PATH", "TMPDIR", "TMP", "TEMP", "TZ", "LANG", "LC_ALL", "LC_CTYPE",
			"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT":
			values[strings.ToUpper(strings.TrimSpace(name))] = value
		}
	}
	values["HOME"] = os.DevNull
	values["XDG_CONFIG_HOME"] = os.DevNull
	values["GIT_CONFIG_NOSYSTEM"] = "1"
	values["GIT_CONFIG_GLOBAL"] = os.DevNull
	values["GIT_CONFIG_SYSTEM"] = os.DevNull
	values["GIT_TERMINAL_PROMPT"] = "0"
	values["GCM_INTERACTIVE"] = "Never"
	values["GIT_ASKPASS"] = os.DevNull
	values["SSH_ASKPASS"] = os.DevNull
	values["GIT_SSH_COMMAND"] = os.DevNull
	values["GIT_NO_REPLACE_OBJECTS"] = "1"
	values["GIT_GRAFT_FILE"] = os.DevNull
	values["GIT_OPTIONAL_LOCKS"] = "0"
	values["GIT_PROTOCOL_FROM_USER"] = "0"
	for name, value := range fixed {
		values[name] = value
	}

	config := []providerGitConfigEntry{
		{key: "core.hooksPath", value: os.DevNull},
		{key: "http.extraHeader", value: ""},
		{key: "credential.helper", value: ""},
		{key: "advice.graftFileDeprecated", value: "false"},
	}
	if remoteURL != "" {
		values["GIT_ALLOW_PROTOCOL"] = "https"
		httpScope := "http." + remoteURL + "."
		config = append(config,
			providerGitConfigEntry{key: "url." + remoteURL + ".insteadOf", value: remoteURL},
			providerGitConfigEntry{key: "url." + remoteURL + ".pushInsteadOf", value: remoteURL},
			providerGitConfigEntry{key: httpScope + "proxy", value: ""},
			providerGitConfigEntry{key: httpScope + "sslVerify", value: "true"},
			providerGitConfigEntry{key: httpScope + "followRedirects", value: "false"},
			providerGitConfigEntry{key: httpScope + "cookieFile", value: ""},
			providerGitConfigEntry{key: httpScope + "saveCookies", value: "false"},
			providerGitConfigEntry{key: httpScope + "curloptResolve", value: ""},
			providerGitConfigEntry{key: httpScope + "extraHeader", value: ""},
		)
		if authorization != "" {
			config = append(config, providerGitConfigEntry{key: httpScope + "extraHeader", value: authorization})
		}
	}
	values["GIT_CONFIG_COUNT"] = fmt.Sprintf("%d", len(config))
	for index, entry := range config {
		values[fmt.Sprintf("GIT_CONFIG_KEY_%d", index)] = entry.key
		values[fmt.Sprintf("GIT_CONFIG_VALUE_%d", index)] = entry.value
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func (adapter *githubProviderAdapter) runGH(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, adapter.ghExecutable, arguments...)
	command.Dir = adapter.repositoryRoot
	command.Env = githubProcessEnvironment(os.Environ(), adapter.providerRepository)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func githubProcessEnvironment(base []string, repository string) []string {
	result := make([]string, 0, len(base)+3)
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "GH_HOST", "GH_REPO", "GH_PROMPT_DISABLED":
			continue
		default:
			result = append(result, entry)
		}
	}
	result = append(result,
		"GH_HOST=github.com",
		"GH_REPO=github.com/"+repository,
		"GH_PROMPT_DISABLED=1",
	)
	return result
}

func providerAdapterFailure(kind workspace.ProviderAdapterFailureKind, marker string, cause error) error {
	failure, err := workspace.NewProviderAdapterFailure(kind, marker, cause)
	if err != nil {
		return fmt.Errorf("construct provider adapter failure: %w", err)
	}
	return failure
}

func parseGitHubObject(value string, algorithm workspace.GitHashAlgorithm) (workspace.GitObjectID, error) {
	return workspace.ParseGitObjectID(string(algorithm) + ":" + strings.TrimSpace(value))
}

func gitObjectHex(object workspace.GitObjectID) string { return hex.EncodeToString(object.Bytes()) }

func decodeProviderJSON(source []byte, target any) error {
	if len(source) == 0 || len(source) > workspace.MaxArtifactBytes {
		return fmt.Errorf("provider response is empty or exceeds %d bytes", workspace.MaxArtifactBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("provider response must contain exactly one JSON value")
	}
	return nil
}

func shortDigest(value []byte) string {
	return strings.TrimPrefix(workspace.DigestBytes(value).String(), "sha256:")[:16]
}

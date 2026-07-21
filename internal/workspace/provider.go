package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderIntentKind is the closed set of provider writes supported by v2.
// Querying is a separate broker operation because reconciliation and
// completion observations must remain possible after authorization is
// revoked. Remote deletion and close-without-merge deliberately have no kind.
type ProviderIntentKind string

const (
	ProviderIntentPush            ProviderIntentKind = "push"
	ProviderIntentOpenPullRequest ProviderIntentKind = "open_pull_request"
	ProviderIntentMerge           ProviderIntentKind = "merge"
)

func (kind ProviderIntentKind) valid() bool {
	return kind == ProviderIntentPush || kind == ProviderIntentOpenPullRequest || kind == ProviderIntentMerge
}

func (kind ProviderIntentKind) authorizationAction() StandingAuthorizationAction {
	switch kind {
	case ProviderIntentPush:
		return StandingAuthorizationPush
	case ProviderIntentOpenPullRequest:
		return StandingAuthorizationOpenPullRequest
	case ProviderIntentMerge:
		return StandingAuthorizationMerge
	default:
		return ""
	}
}

type ProviderMergeStrategy string

const ProviderMergeCommit ProviderMergeStrategy = "merge_commit"

func (strategy ProviderMergeStrategy) valid() bool { return strategy == ProviderMergeCommit }

// ProviderIntentScope contains the exact immutable authority and attempt
// bindings shared by every provider write. The authorization request is
// derived here, rather than accepted as a loosely related caller input.
type ProviderIntentScopeOptions struct {
	WorkspaceID   ID
	Generation    Digest
	AttemptID     ID
	MergeUnit     MergeUnitReference
	Repository    RepositoryIdentity
	Remote        string
	SerialSegment ID
	Frontier      AuthorizationFrontier
	PullRequest   PullRequestIdentity
	Epoch         uint64
}

type providerIntentScope struct {
	workspaceID   ID
	generation    Digest
	attemptID     ID
	mergeUnit     MergeUnitReference
	repository    RepositoryIdentity
	remote        string
	serialSegment ID
	frontier      AuthorizationFrontier
	pullRequest   PullRequestIdentity
	epoch         uint64
}

func newProviderIntentScope(options ProviderIntentScopeOptions) (providerIntentScope, error) {
	remote := strings.TrimSpace(options.Remote)
	if options.WorkspaceID.IsZero() || options.Generation.IsZero() || options.AttemptID.IsZero() ||
		options.MergeUnit.planID.IsZero() || options.MergeUnit.mergeUnitID.IsZero() ||
		options.Repository.String() == "" || options.SerialSegment.IsZero() ||
		options.Frontier.base.IsZero() || options.Frontier.head.IsZero() || options.Epoch == 0 {
		return providerIntentScope{}, fmt.Errorf("provider intent scope requires workspace, generation, attempt, merge unit, repository, segment, frontier, and epoch")
	}
	if err := validateBoundedText("provider intent remote", remote, 512); err != nil {
		return providerIntentScope{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") {
		return providerIntentScope{}, fmt.Errorf("provider intent remote must be a single token")
	}
	if options.Frontier.base.Algorithm() != options.Frontier.head.Algorithm() {
		return providerIntentScope{}, fmt.Errorf("provider intent frontier uses different Git object formats")
	}
	if !options.PullRequest.IsZero() && options.PullRequest.repository != options.Repository {
		return providerIntentScope{}, fmt.Errorf("provider intent pull request belongs to a different repository")
	}
	return providerIntentScope{
		workspaceID: options.WorkspaceID, generation: options.Generation, attemptID: options.AttemptID,
		mergeUnit: options.MergeUnit, repository: options.Repository, remote: remote,
		serialSegment: options.SerialSegment, frontier: options.Frontier,
		pullRequest: options.PullRequest, epoch: options.Epoch,
	}, nil
}

// ProviderIntent is immutable typed data. It deliberately has no argv,
// executable, command rendering, arbitrary action label, or remote-delete
// representation.
type ProviderIntent struct {
	intentID       ID
	idempotencyKey Digest
	digest         Digest
	kind           ProviderIntentKind
	scope          providerIntentScope
	authorization  AuthorizationRequest
	branch         string
	expectedRemote GitObjectID
	baseRef        string
	head           GitObjectID
	tree           GitObjectID
	title          string
	body           string
	pullRequest    PullRequestIdentity
	mergeStrategy  ProviderMergeStrategy
}

type ProviderPushIntentOptions struct {
	Scope              ProviderIntentScopeOptions
	Branch             string
	ExpectedRemoteHead GitObjectID
	Head               GitObjectID
	Tree               GitObjectID
}

func NewProviderPushIntent(options ProviderPushIntentOptions) (ProviderIntent, error) {
	scope, err := newProviderIntentScope(options.Scope)
	if err != nil {
		return ProviderIntent{}, err
	}
	if options.Head.IsZero() || options.Head != scope.frontier.head {
		return ProviderIntent{}, fmt.Errorf("provider push must bind the exact authorized frontier head")
	}
	if !options.ExpectedRemoteHead.IsZero() && options.ExpectedRemoteHead.Algorithm() != options.Head.Algorithm() {
		return ProviderIntent{}, fmt.Errorf("provider push remote and target heads use different Git object formats")
	}
	if !scope.pullRequest.IsZero() && (options.Tree.IsZero() || options.Tree.Algorithm() != options.Head.Algorithm()) {
		return ProviderIntent{}, fmt.Errorf("provider push for an established pull request requires the final head tree")
	}
	if scope.pullRequest.IsZero() && !options.Tree.IsZero() {
		return ProviderIntent{}, fmt.Errorf("provider push cannot bind a pull request tree before provider identity exists")
	}
	return newProviderIntent(ProviderIntent{
		kind: ProviderIntentPush, scope: scope, branch: options.Branch,
		expectedRemote: options.ExpectedRemoteHead, head: options.Head, tree: options.Tree, pullRequest: scope.pullRequest,
	})
}

type ProviderOpenPullRequestIntentOptions struct {
	Scope   ProviderIntentScopeOptions
	Branch  string
	BaseRef string
	Head    GitObjectID
	Tree    GitObjectID
	Title   string
	Body    string
}

func NewProviderOpenPullRequestIntent(options ProviderOpenPullRequestIntentOptions) (ProviderIntent, error) {
	scope, err := newProviderIntentScope(options.Scope)
	if err != nil {
		return ProviderIntent{}, err
	}
	if !scope.pullRequest.IsZero() {
		return ProviderIntent{}, fmt.Errorf("open-pull-request intent cannot accept caller-supplied pull request identity")
	}
	if options.Head.IsZero() || options.Tree.IsZero() || options.Head != scope.frontier.head ||
		options.Head.Algorithm() != options.Tree.Algorithm() {
		return ProviderIntent{}, fmt.Errorf("open-pull-request intent requires the authorized head and matching tree")
	}
	return newProviderIntent(ProviderIntent{
		kind: ProviderIntentOpenPullRequest, scope: scope, branch: options.Branch,
		baseRef: options.BaseRef, head: options.Head, tree: options.Tree,
		title: options.Title, body: options.Body,
	})
}

type ProviderMergeIntentOptions struct {
	Scope    ProviderIntentScopeOptions
	Branch   string
	BaseRef  string
	Head     GitObjectID
	Tree     GitObjectID
	Strategy ProviderMergeStrategy
}

func NewProviderMergeIntent(options ProviderMergeIntentOptions) (ProviderIntent, error) {
	scope, err := newProviderIntentScope(options.Scope)
	if err != nil {
		return ProviderIntent{}, err
	}
	if scope.pullRequest.IsZero() || options.Head.IsZero() || options.Tree.IsZero() ||
		options.Head != scope.frontier.head || options.Head.Algorithm() != options.Tree.Algorithm() ||
		!options.Strategy.valid() {
		return ProviderIntent{}, fmt.Errorf("merge intent requires provider-derived pull request, exact authorized head and tree, and merge-commit strategy")
	}
	return newProviderIntent(ProviderIntent{
		kind: ProviderIntentMerge, scope: scope, branch: options.Branch, baseRef: options.BaseRef,
		head: options.Head, tree: options.Tree,
		pullRequest: scope.pullRequest, mergeStrategy: options.Strategy,
	})
}

func newProviderIntent(intent ProviderIntent) (ProviderIntent, error) {
	branch := strings.TrimSpace(intent.branch)
	baseRef := strings.TrimSpace(intent.baseRef)
	title := strings.TrimSpace(intent.title)
	if intent.kind == ProviderIntentPush || intent.kind == ProviderIntentOpenPullRequest || intent.kind == ProviderIntentMerge {
		if err := validateAttemptBranchSyntax(branch); err != nil {
			return ProviderIntent{}, fmt.Errorf("provider intent branch: %w", err)
		}
	}
	if intent.kind == ProviderIntentOpenPullRequest || intent.kind == ProviderIntentMerge {
		if err := validateBoundedText("provider base ref", baseRef, 1024); err != nil {
			return ProviderIntent{}, err
		}
		if strings.ContainsAny(baseRef, "\t\r\n ") {
			return ProviderIntent{}, fmt.Errorf("provider base ref must be a single token")
		}
		if intent.kind == ProviderIntentOpenPullRequest {
			if err := validateBoundedText("provider pull request title", title, 512); err != nil {
				return ProviderIntent{}, err
			}
			if err := validateBoundedText("provider pull request body", intent.body, 64*1024); err != nil {
				return ProviderIntent{}, err
			}
		}
	}
	authorization, err := NewAuthorizationRequest(AuthorizationRequestOptions{
		WorkspaceID: intent.scope.workspaceID, Repository: intent.scope.repository,
		Remote: intent.scope.remote, Generation: intent.scope.generation,
		SerialSegment: intent.scope.serialSegment, Frontier: intent.scope.frontier,
		Action: intent.kind.authorizationAction(), PullRequest: intent.scope.pullRequest,
		Epoch: intent.scope.epoch,
	})
	if err != nil {
		return ProviderIntent{}, err
	}
	intent.branch, intent.baseRef, intent.title = branch, baseRef, title
	intent.authorization = authorization
	canonical, err := canonicalProviderIntent(intent, false)
	if err != nil {
		return ProviderIntent{}, err
	}
	identityDigest := DigestBytes(canonical)
	identityHex := strings.TrimPrefix(identityDigest.String(), "sha256:")
	intent.intentID, err = NewID("intent-" + identityHex[:16])
	if err != nil {
		return ProviderIntent{}, err
	}
	idempotencyBytes, err := canonicalProviderIntent(intent, true)
	if err != nil {
		return ProviderIntent{}, err
	}
	intent.idempotencyKey = DigestBytes(append([]byte("provider-idempotency-v2\x00"), idempotencyBytes...))
	intent.digest = DigestBytes(append([]byte("provider-intent-v2\x00"), idempotencyBytes...))
	return intent, nil
}

type providerIntentCanonical struct {
	SchemaVersion      int                          `json:"schema_version"`
	IntentID           string                       `json:"intent_id,omitempty"`
	Kind               ProviderIntentKind           `json:"kind"`
	WorkspaceID        string                       `json:"workspace_id"`
	Generation         string                       `json:"generation"`
	AttemptID          string                       `json:"attempt_id"`
	PlanID             string                       `json:"plan_id"`
	MergeUnitID        string                       `json:"merge_unit_id"`
	Repository         string                       `json:"repository"`
	Remote             string                       `json:"remote"`
	SerialSegment      string                       `json:"serial_segment"`
	Base               string                       `json:"base"`
	AuthorizedHead     string                       `json:"authorized_head"`
	AuthorizationEpoch uint64                       `json:"authorization_epoch"`
	Authorization      string                       `json:"authorization_request_digest"`
	Branch             string                       `json:"branch,omitempty"`
	ExpectedRemoteHead string                       `json:"expected_remote_head,omitempty"`
	BaseRef            string                       `json:"base_ref,omitempty"`
	Head               string                       `json:"head"`
	Tree               string                       `json:"tree,omitempty"`
	Title              string                       `json:"title,omitempty"`
	Body               string                       `json:"body,omitempty"`
	PullRequest        *controlPlanePullRequestWire `json:"pull_request,omitempty"`
	MergeStrategy      ProviderMergeStrategy        `json:"merge_strategy,omitempty"`
}

func canonicalProviderIntent(intent ProviderIntent, includeIdentity bool) ([]byte, error) {
	if !intent.kind.valid() || intent.scope.workspaceID.IsZero() || intent.scope.generation.IsZero() ||
		intent.scope.attemptID.IsZero() || intent.scope.mergeUnit.planID.IsZero() ||
		intent.scope.mergeUnit.mergeUnitID.IsZero() || intent.scope.repository.String() == "" ||
		intent.scope.serialSegment.IsZero() || intent.scope.frontier.base.IsZero() ||
		intent.scope.frontier.head.IsZero() || intent.scope.epoch == 0 || intent.authorization.digest.IsZero() {
		return nil, fmt.Errorf("provider intent is incomplete")
	}
	wire := providerIntentCanonical{
		SchemaVersion: JournalSchemaVersion, Kind: intent.kind,
		WorkspaceID: intent.scope.workspaceID.String(), Generation: intent.scope.generation.String(),
		AttemptID: intent.scope.attemptID.String(), PlanID: intent.scope.mergeUnit.planID.String(),
		MergeUnitID: intent.scope.mergeUnit.mergeUnitID.String(), Repository: intent.scope.repository.String(),
		Remote: intent.scope.remote, SerialSegment: intent.scope.serialSegment.String(),
		Base: intent.scope.frontier.base.String(), AuthorizedHead: intent.scope.frontier.head.String(),
		AuthorizationEpoch: intent.scope.epoch, Authorization: intent.authorization.digest.String(),
		Branch: intent.branch, ExpectedRemoteHead: intent.expectedRemote.String(), BaseRef: intent.baseRef,
		Head: intent.head.String(), Tree: intent.tree.String(), Title: intent.title, Body: intent.body,
		MergeStrategy: intent.mergeStrategy,
	}
	if includeIdentity {
		wire.IntentID = intent.intentID.String()
	}
	if !intent.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: intent.pullRequest.provider.String(), Repository: intent.pullRequest.repository.String(),
			Number: intent.pullRequest.number,
		}
	}
	return json.Marshal(wire)
}

func (intent ProviderIntent) IntentID() ID                               { return intent.intentID }
func (intent ProviderIntent) IdempotencyKey() Digest                     { return intent.idempotencyKey }
func (intent ProviderIntent) Digest() Digest                             { return intent.digest }
func (intent ProviderIntent) Kind() ProviderIntentKind                   { return intent.kind }
func (intent ProviderIntent) WorkspaceID() ID                            { return intent.scope.workspaceID }
func (intent ProviderIntent) Generation() Digest                         { return intent.scope.generation }
func (intent ProviderIntent) AttemptID() ID                              { return intent.scope.attemptID }
func (intent ProviderIntent) MergeUnit() MergeUnitReference              { return intent.scope.mergeUnit }
func (intent ProviderIntent) Repository() RepositoryIdentity             { return intent.scope.repository }
func (intent ProviderIntent) Remote() string                             { return intent.scope.remote }
func (intent ProviderIntent) SerialSegment() ID                          { return intent.scope.serialSegment }
func (intent ProviderIntent) Frontier() AuthorizationFrontier            { return intent.scope.frontier }
func (intent ProviderIntent) AuthorizationRequest() AuthorizationRequest { return intent.authorization }
func (intent ProviderIntent) Branch() string                             { return intent.branch }
func (intent ProviderIntent) ExpectedRemoteHead() (GitObjectID, bool) {
	return intent.expectedRemote, !intent.expectedRemote.IsZero()
}
func (intent ProviderIntent) BaseRef() string   { return intent.baseRef }
func (intent ProviderIntent) Head() GitObjectID { return intent.head }
func (intent ProviderIntent) Tree() GitObjectID { return intent.tree }
func (intent ProviderIntent) Title() string     { return intent.title }
func (intent ProviderIntent) Body() string      { return intent.body }
func (intent ProviderIntent) PullRequest() (PullRequestIdentity, bool) {
	return intent.pullRequest, !intent.pullRequest.IsZero()
}
func (intent ProviderIntent) MergeStrategy() ProviderMergeStrategy { return intent.mergeStrategy }

type ProviderIntentStatus string

const (
	ProviderIntentReserved           ProviderIntentStatus = "reserved"
	ProviderIntentDispatched         ProviderIntentStatus = "dispatched"
	ProviderIntentSucceeded          ProviderIntentStatus = "succeeded"
	ProviderIntentFailedBeforeEffect ProviderIntentStatus = "failed_before_effect"
	ProviderIntentFailedAfterEffect  ProviderIntentStatus = "failed_after_effect"
	ProviderIntentAmbiguous          ProviderIntentStatus = "ambiguous"
	ProviderIntentReconciled         ProviderIntentStatus = "reconciled"
)

func (status ProviderIntentStatus) valid() bool {
	switch status {
	case ProviderIntentReserved, ProviderIntentDispatched, ProviderIntentSucceeded,
		ProviderIntentFailedBeforeEffect, ProviderIntentFailedAfterEffect,
		ProviderIntentAmbiguous, ProviderIntentReconciled:
		return true
	default:
		return false
	}
}

func (status ProviderIntentStatus) terminal() bool {
	return status == ProviderIntentSucceeded || status == ProviderIntentFailedBeforeEffect || status == ProviderIntentReconciled
}

func (status ProviderIntentStatus) needsReconciliation() bool {
	return status == ProviderIntentDispatched || status == ProviderIntentFailedAfterEffect || status == ProviderIntentAmbiguous
}

// ProviderResult is canonical broker output. Constructors are intentionally
// package-private: callers can provide typed adapter observations to a broker,
// but only the broker can bind them to a dispatched capability and intent.
type ProviderResult struct {
	intentID        ID
	intentDigest    Digest
	kind            ProviderIntentKind
	status          ProviderIntentStatus
	idempotencyKey  Digest
	provider        ID
	requestMarker   string
	remoteHead      GitObjectID
	pullRequest     PullRequestIdentity
	pullRequestHead GitObjectID
	mergeCommit     GitObjectID
	finalBaseHead   GitObjectID
	digest          Digest
}

func newProviderResult(result ProviderResult) (ProviderResult, error) {
	result.requestMarker = strings.TrimSpace(result.requestMarker)
	if result.intentID.IsZero() || result.intentDigest.IsZero() || !result.kind.valid() ||
		!result.status.valid() || result.status == ProviderIntentReserved ||
		result.status == ProviderIntentDispatched || result.status == ProviderIntentReconciled ||
		result.idempotencyKey.IsZero() || result.provider.IsZero() {
		return ProviderResult{}, fmt.Errorf("provider result requires dispatched intent, provider, idempotency, and non-reconciled outcome")
	}
	if err := validateBoundedText("provider request marker", result.requestMarker, 2048); err != nil {
		return ProviderResult{}, err
	}
	if result.status == ProviderIntentSucceeded {
		switch result.kind {
		case ProviderIntentPush:
			if result.remoteHead.IsZero() {
				return ProviderResult{}, fmt.Errorf("successful push result requires remote head")
			}
		case ProviderIntentOpenPullRequest:
			if result.pullRequest.IsZero() || result.pullRequestHead.IsZero() {
				return ProviderResult{}, fmt.Errorf("successful open-pull-request result requires provider identity and head")
			}
		case ProviderIntentMerge:
			if result.mergeCommit.IsZero() || result.finalBaseHead.IsZero() {
				return ProviderResult{}, fmt.Errorf("successful merge result requires merge commit and final base head")
			}
		}
	} else if !result.remoteHead.IsZero() || !result.pullRequest.IsZero() || !result.pullRequestHead.IsZero() ||
		!result.mergeCommit.IsZero() || !result.finalBaseHead.IsZero() {
		return ProviderResult{}, fmt.Errorf("non-success provider result cannot claim effect identities")
	}
	canonical, err := canonicalProviderResult(result)
	if err != nil {
		return ProviderResult{}, err
	}
	result.digest = DigestBytes(canonical)
	return result, nil
}

func canonicalProviderResult(result ProviderResult) ([]byte, error) {
	type resultJSON struct {
		SchemaVersion   int                          `json:"schema_version"`
		IntentID        string                       `json:"intent_id"`
		IntentDigest    string                       `json:"intent_digest"`
		Kind            ProviderIntentKind           `json:"kind"`
		Status          ProviderIntentStatus         `json:"status"`
		IdempotencyKey  string                       `json:"idempotency_key"`
		Provider        string                       `json:"provider"`
		RequestMarker   string                       `json:"request_marker"`
		RemoteHead      string                       `json:"remote_head,omitempty"`
		PullRequest     *controlPlanePullRequestWire `json:"pull_request,omitempty"`
		PullRequestHead string                       `json:"pull_request_head,omitempty"`
		MergeCommit     string                       `json:"merge_commit,omitempty"`
		FinalBaseHead   string                       `json:"final_base_head,omitempty"`
	}
	wire := resultJSON{
		SchemaVersion: JournalSchemaVersion, IntentID: result.intentID.String(),
		IntentDigest: result.intentDigest.String(), Kind: result.kind, Status: result.status,
		IdempotencyKey: result.idempotencyKey.String(), Provider: result.provider.String(),
		RequestMarker: result.requestMarker, RemoteHead: result.remoteHead.String(),
		PullRequestHead: result.pullRequestHead.String(), MergeCommit: result.mergeCommit.String(),
		FinalBaseHead: result.finalBaseHead.String(),
	}
	if !result.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: result.pullRequest.provider.String(), Repository: result.pullRequest.repository.String(),
			Number: result.pullRequest.number,
		}
	}
	return json.Marshal(wire)
}

func (result ProviderResult) IntentID() ID                 { return result.intentID }
func (result ProviderResult) IntentDigest() Digest         { return result.intentDigest }
func (result ProviderResult) Kind() ProviderIntentKind     { return result.kind }
func (result ProviderResult) Status() ProviderIntentStatus { return result.status }
func (result ProviderResult) IdempotencyKey() Digest       { return result.idempotencyKey }
func (result ProviderResult) Provider() ID                 { return result.provider }
func (result ProviderResult) RequestMarker() string        { return result.requestMarker }
func (result ProviderResult) RemoteHead() GitObjectID      { return result.remoteHead }
func (result ProviderResult) PullRequest() (PullRequestIdentity, bool) {
	return result.pullRequest, !result.pullRequest.IsZero()
}
func (result ProviderResult) PullRequestHead() GitObjectID { return result.pullRequestHead }
func (result ProviderResult) MergeCommit() GitObjectID     { return result.mergeCommit }
func (result ProviderResult) FinalBaseHead() GitObjectID   { return result.finalBaseHead }
func (result ProviderResult) Digest() Digest               { return result.digest }

func (result ProviderResult) pullRequestObservation() (ProviderPullRequestObservation, error) {
	if result.kind != ProviderIntentOpenPullRequest || result.status != ProviderIntentSucceeded ||
		result.pullRequest.IsZero() || result.pullRequestHead.IsZero() || result.digest.IsZero() {
		return ProviderPullRequestObservation{}, fmt.Errorf("provider result is not a successful open-pull-request observation")
	}
	return NewProviderPullRequestObservation(
		result.pullRequest.provider, result.pullRequest.repository, result.pullRequest.number,
		result.pullRequestHead, result.digest,
	)
}

func (result ProviderResult) PullRequestObservation() (ProviderPullRequestObservation, error) {
	return result.pullRequestObservation()
}

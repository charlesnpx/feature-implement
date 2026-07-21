package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ProviderCheckConclusion string

const (
	ProviderCheckPassed  ProviderCheckConclusion = "passed"
	ProviderCheckFailed  ProviderCheckConclusion = "failed"
	ProviderCheckPending ProviderCheckConclusion = "pending"
)

func (conclusion ProviderCheckConclusion) valid() bool {
	return conclusion == ProviderCheckPassed || conclusion == ProviderCheckFailed || conclusion == ProviderCheckPending
}

type ProviderReviewConclusion string

const (
	ProviderReviewApproved         ProviderReviewConclusion = "approved"
	ProviderReviewChangesRequested ProviderReviewConclusion = "changes_requested"
	ProviderReviewPending          ProviderReviewConclusion = "pending"
)

func (conclusion ProviderReviewConclusion) valid() bool {
	return conclusion == ProviderReviewApproved || conclusion == ProviderReviewChangesRequested || conclusion == ProviderReviewPending
}

type ProviderCheckState struct {
	id         ID
	required   bool
	conclusion ProviderCheckConclusion
	evidence   Digest
}

func NewProviderCheckState(
	id ID,
	required bool,
	conclusion ProviderCheckConclusion,
	evidence Digest,
) (ProviderCheckState, error) {
	if id.IsZero() || !conclusion.valid() || evidence.IsZero() {
		return ProviderCheckState{}, fmt.Errorf("provider check state requires identity, conclusion, and evidence")
	}
	return ProviderCheckState{id: id, required: required, conclusion: conclusion, evidence: evidence}, nil
}

func (state ProviderCheckState) ID() ID                              { return state.id }
func (state ProviderCheckState) Required() bool                      { return state.required }
func (state ProviderCheckState) Conclusion() ProviderCheckConclusion { return state.conclusion }
func (state ProviderCheckState) EvidenceDigest() Digest              { return state.evidence }

type ProviderReviewState struct {
	id         ID
	required   bool
	conclusion ProviderReviewConclusion
	evidence   Digest
}

func NewProviderReviewState(
	id ID,
	required bool,
	conclusion ProviderReviewConclusion,
	evidence Digest,
) (ProviderReviewState, error) {
	if id.IsZero() || !conclusion.valid() || evidence.IsZero() {
		return ProviderReviewState{}, fmt.Errorf("provider review state requires identity, conclusion, and evidence")
	}
	return ProviderReviewState{id: id, required: required, conclusion: conclusion, evidence: evidence}, nil
}

func (state ProviderReviewState) ID() ID                               { return state.id }
func (state ProviderReviewState) Required() bool                       { return state.required }
func (state ProviderReviewState) Conclusion() ProviderReviewConclusion { return state.conclusion }
func (state ProviderReviewState) EvidenceDigest() Digest               { return state.evidence }

type ProviderPullRequestStateOptions struct {
	Repository          RepositoryIdentity
	PullRequest         PullRequestIdentity
	BaseRef             string
	Branch              string
	Head                GitObjectID
	HeadTree            GitObjectID
	RemoteBranchHead    GitObjectID
	BaseHeadBeforeMerge GitObjectID
	Checks              []ProviderCheckState
	Reviews             []ProviderReviewState
	Merged              bool
	MergeStrategy       ProviderMergeStrategy
	MergeCommit         GitObjectID
	FinalBaseHead       GitObjectID
	RequestMarker       string
}

// ProviderPullRequestState is a canonical typed observation from the trusted
// query adapter. It is still independently checked against local Git before
// completion; broker trust does not substitute for topology verification.
type ProviderPullRequestState struct {
	repository          RepositoryIdentity
	pullRequest         PullRequestIdentity
	baseRef             string
	branch              string
	head                GitObjectID
	headTree            GitObjectID
	remoteBranchHead    GitObjectID
	baseHeadBeforeMerge GitObjectID
	checks              []ProviderCheckState
	reviews             []ProviderReviewState
	merged              bool
	mergeStrategy       ProviderMergeStrategy
	mergeCommit         GitObjectID
	finalBaseHead       GitObjectID
	requestMarker       string
	digest              Digest
}

func NewProviderPullRequestState(options ProviderPullRequestStateOptions) (ProviderPullRequestState, error) {
	state := ProviderPullRequestState{
		repository: options.Repository, pullRequest: options.PullRequest,
		baseRef: strings.TrimSpace(options.BaseRef), branch: strings.TrimSpace(options.Branch),
		head: options.Head, headTree: options.HeadTree, remoteBranchHead: options.RemoteBranchHead,
		baseHeadBeforeMerge: options.BaseHeadBeforeMerge,
		checks:              append([]ProviderCheckState(nil), options.Checks...),
		reviews:             append([]ProviderReviewState(nil), options.Reviews...),
		merged:              options.Merged, mergeStrategy: options.MergeStrategy,
		mergeCommit: options.MergeCommit, finalBaseHead: options.FinalBaseHead,
		requestMarker: strings.TrimSpace(options.RequestMarker),
	}
	if err := normalizeProviderPullRequestState(&state); err != nil {
		return ProviderPullRequestState{}, err
	}
	canonical, err := canonicalProviderPullRequestState(state)
	if err != nil {
		return ProviderPullRequestState{}, err
	}
	state.digest = DigestBytes(canonical)
	return state, nil
}

func normalizeProviderPullRequestState(state *ProviderPullRequestState) error {
	if state.repository.String() == "" || state.pullRequest.IsZero() ||
		state.pullRequest.repository != state.repository || state.head.IsZero() || state.headTree.IsZero() ||
		state.remoteBranchHead.IsZero() || state.baseHeadBeforeMerge.IsZero() {
		return fmt.Errorf("provider pull request state requires repository, identity, head/tree, remote head, and pre-merge base")
	}
	if err := validateBoundedText("provider pull request base ref", state.baseRef, 1024); err != nil {
		return err
	}
	if strings.ContainsAny(state.baseRef, "\t\r\n ") {
		return fmt.Errorf("provider pull request base ref must be a single token")
	}
	if err := validateAttemptBranchSyntax(state.branch); err != nil {
		return fmt.Errorf("provider pull request branch: %w", err)
	}
	if err := validateProviderAdapterMarker(state.requestMarker); err != nil {
		return err
	}
	algorithm := state.head.Algorithm()
	for _, object := range []GitObjectID{state.headTree, state.remoteBranchHead, state.baseHeadBeforeMerge} {
		if object.Algorithm() != algorithm {
			return fmt.Errorf("provider pull request state uses different Git object formats")
		}
	}
	if state.merged {
		if state.mergeStrategy != ProviderMergeCommit || state.mergeCommit.IsZero() ||
			state.finalBaseHead.IsZero() || state.mergeCommit.Algorithm() != algorithm ||
			state.finalBaseHead.Algorithm() != algorithm {
			return fmt.Errorf("merged pull request state requires merge-commit strategy and exact merge/final base objects")
		}
	} else if state.mergeStrategy != "" || !state.mergeCommit.IsZero() || !state.finalBaseHead.IsZero() {
		return fmt.Errorf("unmerged pull request state cannot carry merge completion identities")
	}
	if err := normalizeProviderChecks(&state.checks); err != nil {
		return err
	}
	if err := normalizeProviderReviews(&state.reviews); err != nil {
		return err
	}
	return nil
}

func normalizeProviderChecks(values *[]ProviderCheckState) error {
	result := append([]ProviderCheckState(nil), (*values)...)
	sort.Slice(result, func(i, j int) bool { return result[i].id.String() < result[j].id.String() })
	for index, state := range result {
		if state.id.IsZero() || !state.conclusion.valid() || state.evidence.IsZero() ||
			index > 0 && state.id == result[index-1].id {
			return fmt.Errorf("provider checks require unique identities, conclusions, and evidence")
		}
	}
	*values = result
	return nil
}

func normalizeProviderReviews(values *[]ProviderReviewState) error {
	result := append([]ProviderReviewState(nil), (*values)...)
	sort.Slice(result, func(i, j int) bool { return result[i].id.String() < result[j].id.String() })
	for index, state := range result {
		if state.id.IsZero() || !state.conclusion.valid() || state.evidence.IsZero() ||
			index > 0 && state.id == result[index-1].id {
			return fmt.Errorf("provider reviews require unique identities, conclusions, and evidence")
		}
	}
	*values = result
	return nil
}

func (state ProviderPullRequestState) validate() error {
	copyState := state
	if err := normalizeProviderPullRequestState(&copyState); err != nil {
		return err
	}
	canonical, err := canonicalProviderPullRequestState(copyState)
	if err != nil || state.digest.IsZero() || DigestBytes(canonical) != state.digest {
		return fmt.Errorf("provider pull request state digest mismatch")
	}
	return nil
}

func (state ProviderPullRequestState) Repository() RepositoryIdentity   { return state.repository }
func (state ProviderPullRequestState) PullRequest() PullRequestIdentity { return state.pullRequest }
func (state ProviderPullRequestState) BaseRef() string                  { return state.baseRef }
func (state ProviderPullRequestState) Branch() string                   { return state.branch }
func (state ProviderPullRequestState) Head() GitObjectID                { return state.head }
func (state ProviderPullRequestState) HeadTree() GitObjectID            { return state.headTree }
func (state ProviderPullRequestState) RemoteBranchHead() GitObjectID    { return state.remoteBranchHead }
func (state ProviderPullRequestState) BaseHeadBeforeMerge() GitObjectID {
	return state.baseHeadBeforeMerge
}
func (state ProviderPullRequestState) Checks() []ProviderCheckState {
	return append([]ProviderCheckState(nil), state.checks...)
}
func (state ProviderPullRequestState) Reviews() []ProviderReviewState {
	return append([]ProviderReviewState(nil), state.reviews...)
}
func (state ProviderPullRequestState) Merged() bool { return state.merged }
func (state ProviderPullRequestState) MergeStrategy() ProviderMergeStrategy {
	return state.mergeStrategy
}
func (state ProviderPullRequestState) MergeCommit() GitObjectID   { return state.mergeCommit }
func (state ProviderPullRequestState) FinalBaseHead() GitObjectID { return state.finalBaseHead }
func (state ProviderPullRequestState) Digest() Digest             { return state.digest }

type providerCheckStateWire struct {
	ID         string                  `json:"id"`
	Required   bool                    `json:"required"`
	Conclusion ProviderCheckConclusion `json:"conclusion"`
	Evidence   string                  `json:"evidence_digest"`
}

type providerReviewStateWire struct {
	ID         string                   `json:"id"`
	Required   bool                     `json:"required"`
	Conclusion ProviderReviewConclusion `json:"conclusion"`
	Evidence   string                   `json:"evidence_digest"`
}

type providerPullRequestStateCanonical struct {
	SchemaVersion       int                         `json:"schema_version"`
	Repository          string                      `json:"repository"`
	PullRequest         controlPlanePullRequestWire `json:"pull_request"`
	BaseRef             string                      `json:"base_ref"`
	Branch              string                      `json:"branch"`
	Head                string                      `json:"head"`
	HeadTree            string                      `json:"head_tree"`
	RemoteBranchHead    string                      `json:"remote_branch_head"`
	BaseHeadBeforeMerge string                      `json:"base_head_before_merge"`
	Checks              []providerCheckStateWire    `json:"checks"`
	Reviews             []providerReviewStateWire   `json:"reviews"`
	Merged              bool                        `json:"merged"`
	MergeStrategy       ProviderMergeStrategy       `json:"merge_strategy,omitempty"`
	MergeCommit         string                      `json:"merge_commit,omitempty"`
	FinalBaseHead       string                      `json:"final_base_head,omitempty"`
	RequestMarker       string                      `json:"request_marker"`
}

func canonicalProviderPullRequestState(state ProviderPullRequestState) ([]byte, error) {
	wire := providerPullRequestStateCanonical{
		SchemaVersion: JournalSchemaVersion, Repository: state.repository.String(),
		PullRequest: controlPlanePullRequestWire{
			Provider: state.pullRequest.provider.String(), Repository: state.pullRequest.repository.String(),
			Number: state.pullRequest.number,
		},
		BaseRef: state.baseRef, Branch: state.branch, Head: state.head.String(), HeadTree: state.headTree.String(),
		RemoteBranchHead: state.remoteBranchHead.String(), BaseHeadBeforeMerge: state.baseHeadBeforeMerge.String(),
		Checks:  make([]providerCheckStateWire, 0, len(state.checks)),
		Reviews: make([]providerReviewStateWire, 0, len(state.reviews)),
		Merged:  state.merged, MergeStrategy: state.mergeStrategy,
		MergeCommit: state.mergeCommit.String(), FinalBaseHead: state.finalBaseHead.String(),
		RequestMarker: state.requestMarker,
	}
	for _, check := range state.checks {
		wire.Checks = append(wire.Checks, providerCheckStateWire{
			ID: check.id.String(), Required: check.required, Conclusion: check.conclusion, Evidence: check.evidence.String(),
		})
	}
	for _, review := range state.reviews {
		wire.Reviews = append(wire.Reviews, providerReviewStateWire{
			ID: review.id.String(), Required: review.required, Conclusion: review.conclusion, Evidence: review.evidence.String(),
		})
	}
	return json.Marshal(wire)
}

// ProviderMergePreflight is the durable, provider-observed policy boundary
// captured before a merge write is dispatched. Required check and review
// identities therefore cannot first appear after the external merge.
type ProviderMergePreflight struct {
	intentID        ID
	intentDigest    Digest
	repository      RepositoryIdentity
	pullRequest     PullRequestIdentity
	baseRef         string
	branch          string
	baseHead        GitObjectID
	head            GitObjectID
	headTree        GitObjectID
	remoteHead      GitObjectID
	requiredChecks  []ProviderCheckState
	requiredReviews []ProviderReviewState
	observation     Digest
	digest          Digest
}

func newProviderMergePreflight(
	intent ProviderIntent,
	state ProviderPullRequestState,
) (ProviderMergePreflight, error) {
	if intent.kind != ProviderIntentMerge || intent.intentID.IsZero() || intent.digest.IsZero() {
		return ProviderMergePreflight{}, fmt.Errorf("provider merge preflight requires a typed merge intent")
	}
	if err := state.validate(); err != nil {
		return ProviderMergePreflight{}, err
	}
	if state.repository != intent.scope.repository || state.pullRequest != intent.pullRequest ||
		state.baseRef != intent.baseRef || state.branch != intent.branch ||
		state.baseHeadBeforeMerge != intent.scope.frontier.base || state.head != intent.head ||
		state.headTree != intent.tree || state.remoteBranchHead != intent.head || state.merged {
		return ProviderMergePreflight{}, fmt.Errorf("provider merge preflight does not match the exact unmerged intent topology")
	}
	preflight := ProviderMergePreflight{
		intentID: intent.intentID, intentDigest: intent.digest, repository: state.repository,
		pullRequest: state.pullRequest, baseRef: state.baseRef, branch: state.branch,
		baseHead: state.baseHeadBeforeMerge, head: state.head, headTree: state.headTree,
		remoteHead: state.remoteBranchHead, observation: state.digest,
	}
	for _, check := range state.checks {
		if !check.required {
			continue
		}
		if check.conclusion != ProviderCheckPassed {
			return ProviderMergePreflight{}, fmt.Errorf("provider-required check %s is not passing", check.id)
		}
		preflight.requiredChecks = append(preflight.requiredChecks, check)
	}
	for _, review := range state.reviews {
		if !review.required {
			continue
		}
		if review.conclusion != ProviderReviewApproved {
			return ProviderMergePreflight{}, fmt.Errorf("provider-required review %s is not approved", review.id)
		}
		preflight.requiredReviews = append(preflight.requiredReviews, review)
	}
	canonical, err := canonicalProviderMergePreflight(preflight)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	preflight.digest = DigestBytes(canonical)
	return preflight, nil
}

func (preflight ProviderMergePreflight) IntentID() ID              { return preflight.intentID }
func (preflight ProviderMergePreflight) IntentDigest() Digest      { return preflight.intentDigest }
func (preflight ProviderMergePreflight) ObservationDigest() Digest { return preflight.observation }
func (preflight ProviderMergePreflight) Digest() Digest            { return preflight.digest }
func (preflight ProviderMergePreflight) RequiredChecks() []ProviderCheckState {
	return append([]ProviderCheckState(nil), preflight.requiredChecks...)
}
func (preflight ProviderMergePreflight) RequiredReviews() []ProviderReviewState {
	return append([]ProviderReviewState(nil), preflight.requiredReviews...)
}

func canonicalProviderMergePreflight(preflight ProviderMergePreflight) ([]byte, error) {
	if preflight.intentID.IsZero() || preflight.intentDigest.IsZero() ||
		preflight.repository.String() == "" || preflight.pullRequest.IsZero() ||
		preflight.pullRequest.repository != preflight.repository || preflight.baseHead.IsZero() ||
		preflight.head.IsZero() || preflight.headTree.IsZero() || preflight.remoteHead.IsZero() ||
		preflight.observation.IsZero() {
		return nil, fmt.Errorf("provider merge preflight requires exact intent, provider, and Git bindings")
	}
	if preflight.head.Algorithm() != preflight.headTree.Algorithm() ||
		preflight.head.Algorithm() != preflight.remoteHead.Algorithm() ||
		preflight.head.Algorithm() != preflight.baseHead.Algorithm() {
		return nil, fmt.Errorf("provider merge preflight uses different Git object formats")
	}
	type canonical struct {
		SchemaVersion   int                         `json:"schema_version"`
		IntentID        string                      `json:"intent_id"`
		IntentDigest    string                      `json:"intent_digest"`
		Repository      string                      `json:"repository"`
		PullRequest     controlPlanePullRequestWire `json:"pull_request"`
		BaseRef         string                      `json:"base_ref"`
		Branch          string                      `json:"branch"`
		BaseHead        string                      `json:"base_head"`
		Head            string                      `json:"head"`
		HeadTree        string                      `json:"head_tree"`
		RemoteHead      string                      `json:"remote_head"`
		RequiredChecks  []providerCheckStateWire    `json:"required_checks"`
		RequiredReviews []providerReviewStateWire   `json:"required_reviews"`
		Observation     string                      `json:"provider_observation_digest"`
	}
	wire := canonical{
		SchemaVersion: JournalSchemaVersion, IntentID: preflight.intentID.String(),
		IntentDigest: preflight.intentDigest.String(), Repository: preflight.repository.String(),
		PullRequest: controlPlanePullRequestWire{
			Provider: preflight.pullRequest.provider.String(), Repository: preflight.pullRequest.repository.String(),
			Number: preflight.pullRequest.number,
		},
		BaseRef: preflight.baseRef, Branch: preflight.branch, BaseHead: preflight.baseHead.String(),
		Head: preflight.head.String(), HeadTree: preflight.headTree.String(), RemoteHead: preflight.remoteHead.String(),
		RequiredChecks:  make([]providerCheckStateWire, 0, len(preflight.requiredChecks)),
		RequiredReviews: make([]providerReviewStateWire, 0, len(preflight.requiredReviews)),
		Observation:     preflight.observation.String(),
	}
	for _, check := range preflight.requiredChecks {
		if !check.required || check.conclusion != ProviderCheckPassed || check.id.IsZero() || check.evidence.IsZero() {
			return nil, fmt.Errorf("provider merge preflight has invalid required check evidence")
		}
		wire.RequiredChecks = append(wire.RequiredChecks, providerCheckStateWire{
			ID: check.id.String(), Required: true, Conclusion: check.conclusion, Evidence: check.evidence.String(),
		})
	}
	for _, review := range preflight.requiredReviews {
		if !review.required || review.conclusion != ProviderReviewApproved || review.id.IsZero() || review.evidence.IsZero() {
			return nil, fmt.Errorf("provider merge preflight has invalid required review evidence")
		}
		wire.RequiredReviews = append(wire.RequiredReviews, providerReviewStateWire{
			ID: review.id.String(), Required: true, Conclusion: review.conclusion, Evidence: review.evidence.String(),
		})
	}
	return json.Marshal(wire)
}

func validateProviderStateAgainstMergePreflight(
	preflight ProviderMergePreflight,
	state ProviderPullRequestState,
) error {
	canonical, err := canonicalProviderMergePreflight(preflight)
	if err != nil || preflight.digest != DigestBytes(canonical) {
		return fmt.Errorf("provider merge preflight digest mismatch")
	}
	if err := state.validate(); err != nil {
		return err
	}
	if state.repository != preflight.repository || state.pullRequest != preflight.pullRequest ||
		state.baseRef != preflight.baseRef || state.branch != preflight.branch ||
		state.baseHeadBeforeMerge != preflight.baseHead || state.head != preflight.head ||
		state.headTree != preflight.headTree || state.remoteBranchHead != preflight.remoteHead {
		return fmt.Errorf("provider pull request state drifted from its durable merge preflight")
	}
	requiredChecks := make([]ProviderCheckState, 0)
	for _, check := range state.checks {
		if check.required {
			requiredChecks = append(requiredChecks, check)
		}
	}
	requiredReviews := make([]ProviderReviewState, 0)
	for _, review := range state.reviews {
		if review.required {
			requiredReviews = append(requiredReviews, review)
		}
	}
	if len(requiredChecks) != len(preflight.requiredChecks) || len(requiredReviews) != len(preflight.requiredReviews) {
		return fmt.Errorf("provider-required evidence identities drifted from durable merge preflight")
	}
	for index := range requiredChecks {
		if requiredChecks[index] != preflight.requiredChecks[index] || requiredChecks[index].conclusion != ProviderCheckPassed {
			return fmt.Errorf("provider-required check evidence drifted from durable merge preflight")
		}
	}
	for index := range requiredReviews {
		if requiredReviews[index] != preflight.requiredReviews[index] || requiredReviews[index].conclusion != ProviderReviewApproved {
			return fmt.Errorf("provider-required review evidence drifted from durable merge preflight")
		}
	}
	return nil
}

type ProviderGitCommit struct {
	commit  GitObjectID
	tree    GitObjectID
	parents []GitObjectID
}

func NewProviderGitCommit(commit, tree GitObjectID, parents []GitObjectID) (ProviderGitCommit, error) {
	if commit.IsZero() || tree.IsZero() || commit.Algorithm() != tree.Algorithm() {
		return ProviderGitCommit{}, fmt.Errorf("provider Git commit requires matching commit and tree identities")
	}
	copyParents := append([]GitObjectID(nil), parents...)
	for _, parent := range copyParents {
		if parent.IsZero() || parent.Algorithm() != commit.Algorithm() {
			return ProviderGitCommit{}, fmt.Errorf("provider Git commit parent uses a different object format")
		}
	}
	return ProviderGitCommit{commit: commit, tree: tree, parents: copyParents}, nil
}

func (commit ProviderGitCommit) Commit() GitObjectID { return commit.commit }
func (commit ProviderGitCommit) Tree() GitObjectID   { return commit.tree }
func (commit ProviderGitCommit) Parents() []GitObjectID {
	return append([]GitObjectID(nil), commit.parents...)
}

type ProviderCompletionGitInspection struct {
	remoteBranchHead GitObjectID
	finalBaseHead    GitObjectID
	headCommit       ProviderGitCommit
	mergeCommit      ProviderGitCommit
	baseAncestor     bool
	headAncestor     bool
}

func NewProviderCompletionGitInspection(
	remoteBranchHead, finalBaseHead GitObjectID,
	headCommit, mergeCommit ProviderGitCommit,
	baseAncestor, headAncestor bool,
) (ProviderCompletionGitInspection, error) {
	if remoteBranchHead.IsZero() || finalBaseHead.IsZero() ||
		headCommit.commit.IsZero() || mergeCommit.commit.IsZero() {
		return ProviderCompletionGitInspection{}, fmt.Errorf("provider Git inspection requires exact remote and commit identities")
	}
	return ProviderCompletionGitInspection{
		remoteBranchHead: remoteBranchHead, finalBaseHead: finalBaseHead,
		headCommit: headCommit, mergeCommit: mergeCommit,
		baseAncestor: baseAncestor, headAncestor: headAncestor,
	}, nil
}

func (inspection ProviderCompletionGitInspection) RemoteBranchHead() GitObjectID {
	return inspection.remoteBranchHead
}
func (inspection ProviderCompletionGitInspection) FinalBaseHead() GitObjectID {
	return inspection.finalBaseHead
}
func (inspection ProviderCompletionGitInspection) HeadCommit() ProviderGitCommit {
	return inspection.headCommit
}
func (inspection ProviderCompletionGitInspection) MergeCommit() ProviderGitCommit {
	return inspection.mergeCommit
}
func (inspection ProviderCompletionGitInspection) BaseAncestor() bool { return inspection.baseAncestor }
func (inspection ProviderCompletionGitInspection) HeadAncestor() bool { return inspection.headAncestor }

// ProviderCompletionGitPort independently observes refs, acquires their
// objects into an isolated store, and verifies topology there. Implementations
// must not move user refs or obtain a provider-broker capability.
type ProviderCompletionGitPort interface {
	InspectRemoteTopology(
		context.Context,
		string,
		string,
		string,
		string,
		GitObjectID,
		GitObjectID,
		GitObjectID,
	) (ProviderCompletionGitInspection, error)
}

type ProviderCompletionRequest struct {
	AttemptID  ID
	OccurredAt time.Time
}

type ProviderCompletionReceipt struct {
	workspaceID         ID
	generation          Digest
	definitionDigest    Digest
	attemptID           ID
	mergeUnit           MergeUnitReference
	repository          RepositoryIdentity
	provider            ProviderIdentity
	remote              string
	baseRef             string
	branch              string
	pullRequest         PullRequestIdentity
	baseHead            GitObjectID
	head                GitObjectID
	headTree            GitObjectID
	mergeCommit         GitObjectID
	mergeTree           GitObjectID
	finalBaseHead       GitObjectID
	mergeStrategy       ProviderMergeStrategy
	providerObservation Digest
	reviewReadiness     Digest
	checkEvidence       []Digest
	reviewEvidence      []Digest
	ownerReceipts       []Digest
	providerResults     []Digest
	topologyDigest      Digest
	digest              Digest
}

func (receipt ProviderCompletionReceipt) WorkspaceID() ID               { return receipt.workspaceID }
func (receipt ProviderCompletionReceipt) Generation() Digest            { return receipt.generation }
func (receipt ProviderCompletionReceipt) AttemptID() ID                 { return receipt.attemptID }
func (receipt ProviderCompletionReceipt) MergeUnit() MergeUnitReference { return receipt.mergeUnit }
func (receipt ProviderCompletionReceipt) PullRequest() PullRequestIdentity {
	return receipt.pullRequest
}
func (receipt ProviderCompletionReceipt) BaseHead() GitObjectID      { return receipt.baseHead }
func (receipt ProviderCompletionReceipt) Head() GitObjectID          { return receipt.head }
func (receipt ProviderCompletionReceipt) HeadTree() GitObjectID      { return receipt.headTree }
func (receipt ProviderCompletionReceipt) MergeCommit() GitObjectID   { return receipt.mergeCommit }
func (receipt ProviderCompletionReceipt) MergeTree() GitObjectID     { return receipt.mergeTree }
func (receipt ProviderCompletionReceipt) FinalBaseHead() GitObjectID { return receipt.finalBaseHead }
func (receipt ProviderCompletionReceipt) Digest() Digest             { return receipt.digest }
func (receipt ProviderCompletionReceipt) MarshalJSON() ([]byte, error) {
	wire := providerCompletionReceiptToWire(receipt)
	return json.Marshal(wire)
}

type providerCompletionReceiptWire struct {
	SchemaVersion       int                         `json:"schema_version"`
	WorkspaceID         string                      `json:"workspace_id"`
	Generation          string                      `json:"generation"`
	DefinitionDigest    string                      `json:"definition_digest"`
	AttemptID           string                      `json:"attempt_id"`
	PlanID              string                      `json:"plan_id"`
	MergeUnitID         string                      `json:"merge_unit_id"`
	Repository          string                      `json:"repository"`
	ProviderKind        string                      `json:"provider_kind"`
	ProviderRepository  string                      `json:"provider_repository"`
	Remote              string                      `json:"remote"`
	BaseRef             string                      `json:"base_ref"`
	Branch              string                      `json:"branch"`
	PullRequest         controlPlanePullRequestWire `json:"pull_request"`
	BaseHead            string                      `json:"base_head"`
	Head                string                      `json:"head"`
	HeadTree            string                      `json:"head_tree"`
	MergeCommit         string                      `json:"merge_commit"`
	MergeTree           string                      `json:"merge_tree"`
	FinalBaseHead       string                      `json:"final_base_head"`
	MergeStrategy       ProviderMergeStrategy       `json:"merge_strategy"`
	ProviderObservation string                      `json:"provider_observation_digest"`
	ReviewReadiness     string                      `json:"review_readiness_digest,omitempty"`
	CheckEvidence       []string                    `json:"check_evidence_digests"`
	ReviewEvidence      []string                    `json:"review_evidence_digests"`
	OwnerReceipts       []string                    `json:"owner_receipt_digests"`
	ProviderResults     []string                    `json:"provider_result_digests"`
	TopologyDigest      string                      `json:"topology_digest"`
	Digest              string                      `json:"digest"`
}

func providerCompletionReceiptToWire(receipt ProviderCompletionReceipt) providerCompletionReceiptWire {
	wire := providerCompletionReceiptWire{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: receipt.workspaceID.String(),
		Generation: receipt.generation.String(), DefinitionDigest: receipt.definitionDigest.String(),
		AttemptID: receipt.attemptID.String(), PlanID: receipt.mergeUnit.planID.String(),
		MergeUnitID: receipt.mergeUnit.mergeUnitID.String(), Repository: receipt.repository.String(),
		ProviderKind: receipt.provider.kind.String(), ProviderRepository: receipt.provider.repository,
		Remote: receipt.remote, BaseRef: receipt.baseRef, Branch: receipt.branch,
		PullRequest: controlPlanePullRequestWire{
			Provider: receipt.pullRequest.provider.String(), Repository: receipt.pullRequest.repository.String(),
			Number: receipt.pullRequest.number,
		},
		BaseHead: receipt.baseHead.String(), Head: receipt.head.String(), HeadTree: receipt.headTree.String(),
		MergeCommit: receipt.mergeCommit.String(), MergeTree: receipt.mergeTree.String(),
		FinalBaseHead: receipt.finalBaseHead.String(), MergeStrategy: receipt.mergeStrategy,
		ProviderObservation: receipt.providerObservation.String(), ReviewReadiness: receipt.reviewReadiness.String(),
		CheckEvidence: digestStrings(receipt.checkEvidence), ReviewEvidence: digestStrings(receipt.reviewEvidence),
		OwnerReceipts: digestStrings(receipt.ownerReceipts), ProviderResults: digestStrings(receipt.providerResults),
		TopologyDigest: receipt.topologyDigest.String(), Digest: receipt.digest.String(),
	}
	return wire
}

func providerCompletionReceiptFromWire(wire providerCompletionReceiptWire) (ProviderCompletionReceipt, error) {
	if wire.SchemaVersion != JournalSchemaVersion {
		return ProviderCompletionReceipt{}, fmt.Errorf("provider completion receipt schema_version %d is not supported", wire.SchemaVersion)
	}
	workspaceID, err := NewID(wire.WorkspaceID)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	definitionDigest, err := ParseDigest(wire.DefinitionDigest)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	attemptID, err := NewID(wire.AttemptID)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	planID, err := NewID(wire.PlanID)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	unitID, err := NewID(wire.MergeUnitID)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	mergeUnit, _ := NewMergeUnitReference(planID, unitID)
	repository, err := NewRepositoryIdentity(wire.Repository)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	providerKind, err := NewID(wire.ProviderKind)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	provider, err := NewProviderIdentity(providerKind, wire.ProviderRepository)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	prProvider, err := NewID(wire.PullRequest.Provider)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	prRepository, err := NewRepositoryIdentity(wire.PullRequest.Repository)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	pullRequest, err := newPullRequestIdentity(prProvider, prRepository, wire.PullRequest.Number)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	parseObject := func(value string) (GitObjectID, error) { return ParseGitObjectID(value) }
	baseHead, err := parseObject(wire.BaseHead)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	head, err := parseObject(wire.Head)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	headTree, err := parseObject(wire.HeadTree)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	mergeCommit, err := parseObject(wire.MergeCommit)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	mergeTree, err := parseObject(wire.MergeTree)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	finalBaseHead, err := parseObject(wire.FinalBaseHead)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	providerObservation, err := ParseDigest(wire.ProviderObservation)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	var readiness Digest
	if wire.ReviewReadiness != "" {
		readiness, err = ParseDigest(wire.ReviewReadiness)
		if err != nil {
			return ProviderCompletionReceipt{}, err
		}
	}
	parseDigests := func(values []string) ([]Digest, error) {
		result := make([]Digest, 0, len(values))
		for _, value := range values {
			digest, parseErr := ParseDigest(value)
			if parseErr != nil {
				return nil, parseErr
			}
			result = append(result, digest)
		}
		return normalizeDigestEvidence(result)
	}
	checkEvidence, err := parseDigests(wire.CheckEvidence)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	reviewEvidence, err := parseDigests(wire.ReviewEvidence)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	ownerReceipts, err := parseDigests(wire.OwnerReceipts)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	providerResults, err := parseDigests(wire.ProviderResults)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	topology, err := ParseDigest(wire.TopologyDigest)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	receipt := ProviderCompletionReceipt{
		workspaceID: workspaceID, generation: generation, definitionDigest: definitionDigest,
		attemptID: attemptID, mergeUnit: mergeUnit, repository: repository, provider: provider,
		remote: wire.Remote, baseRef: wire.BaseRef, branch: wire.Branch, pullRequest: pullRequest,
		baseHead: baseHead, head: head, headTree: headTree, mergeCommit: mergeCommit,
		mergeTree: mergeTree, finalBaseHead: finalBaseHead, mergeStrategy: wire.MergeStrategy,
		providerObservation: providerObservation, reviewReadiness: readiness,
		checkEvidence: checkEvidence, reviewEvidence: reviewEvidence,
		ownerReceipts: ownerReceipts, providerResults: providerResults, topologyDigest: topology,
	}
	canonical, err := canonicalProviderCompletionReceipt(receipt)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	receipt.digest = DigestBytes(canonical)
	wireDigest, err := ParseDigest(wire.Digest)
	if err != nil || wireDigest != receipt.digest {
		return ProviderCompletionReceipt{}, fmt.Errorf("provider completion receipt digest mismatch")
	}
	return receipt, nil
}

func canonicalProviderCompletionReceipt(receipt ProviderCompletionReceipt) ([]byte, error) {
	wire := providerCompletionReceiptToWire(receipt)
	wire.Digest = ""
	return json.Marshal(wire)
}

func cloneProviderCompletionReceipt(receipt ProviderCompletionReceipt) ProviderCompletionReceipt {
	receipt.checkEvidence = append([]Digest(nil), receipt.checkEvidence...)
	receipt.reviewEvidence = append([]Digest(nil), receipt.reviewEvidence...)
	receipt.ownerReceipts = append([]Digest(nil), receipt.ownerReceipts...)
	receipt.providerResults = append([]Digest(nil), receipt.providerResults...)
	return receipt
}

func digestStrings(values []Digest) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.String())
	}
	return result
}

func normalizeDigestEvidence(values []Digest) ([]Digest, error) {
	result := append([]Digest(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	for index, digest := range result {
		if digest.IsZero() || index > 0 && digest == result[index-1] {
			return nil, fmt.Errorf("receipt evidence digests must be nonzero and unique")
		}
	}
	return result, nil
}

func definitionEvidenceDigest(definition EffectiveWorkspaceDefinition) (Digest, error) {
	type artifactWire struct {
		Kind         ArtifactKind `json:"kind"`
		ID           string       `json:"id"`
		Path         string       `json:"path"`
		SourceHash   string       `json:"source_hash"`
		SemanticHash string       `json:"semantic_hash"`
	}
	type authorityWire struct {
		ID           string        `json:"id"`
		Kind         AuthorityKind `json:"kind"`
		Location     string        `json:"location"`
		SourceHash   string        `json:"source_hash"`
		SemanticHash string        `json:"semantic_hash"`
	}
	type canonical struct {
		SchemaVersion int             `json:"schema_version"`
		WorkspaceID   string          `json:"workspace_id"`
		Generation    string          `json:"generation"`
		Artifacts     []artifactWire  `json:"artifacts"`
		Authorities   []authorityWire `json:"authorities"`
	}
	wire := canonical{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: definition.workspace.id.String(),
		Generation: definition.generation.String(), Artifacts: make([]artifactWire, 0, len(definition.artifacts)),
		Authorities: make([]authorityWire, 0, len(definition.authorities)),
	}
	for _, artifact := range definition.artifacts {
		wire.Artifacts = append(wire.Artifacts, artifactWire{
			Kind: artifact.kind, ID: artifact.id.String(), Path: artifact.path,
			SourceHash: artifact.sourceHash.String(), SemanticHash: artifact.semanticHash.String(),
		})
	}
	for _, authority := range definition.authorities {
		wire.Authorities = append(wire.Authorities, authorityWire{
			ID: authority.id.String(), Kind: authority.kind, Location: authority.location,
			SourceHash: authority.sourceHash.String(), SemanticHash: authority.semanticHash.String(),
		})
	}
	content, err := json.Marshal(wire)
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func newProviderCompletionReceipt(
	definition EffectiveWorkspaceDefinition,
	attempt RuntimeAttemptProjection,
	state ProviderPullRequestState,
	mergeTree GitObjectID,
	readiness ReviewReadiness,
	checkEvidence, reviewEvidence []Digest,
	ownerReceipts, providerResults []Digest,
	topology Digest,
) (ProviderCompletionReceipt, error) {
	definitionDigest, err := definitionEvidenceDigest(definition)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	checks, err := normalizeDigestEvidence(checkEvidence)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	reviews := append([]Digest(nil), reviewEvidence...)
	if !readiness.digest.IsZero() {
		reviews = append(reviews, readiness.digest)
	}
	reviews, err = normalizeDigestEvidence(reviews)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	owners, err := normalizeDigestEvidence(ownerReceipts)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	results, err := normalizeDigestEvidence(providerResults)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	receipt := ProviderCompletionReceipt{
		workspaceID: definition.workspace.id, generation: definition.generation,
		definitionDigest: definitionDigest, attemptID: attempt.attemptID, mergeUnit: attempt.mergeUnit,
		repository: definition.workspace.repository, provider: definition.workspace.provider,
		remote: definition.workspace.remote, baseRef: state.baseRef, branch: state.branch,
		pullRequest: state.pullRequest, baseHead: state.baseHeadBeforeMerge,
		head: state.head, headTree: state.headTree, mergeCommit: state.mergeCommit,
		mergeTree: mergeTree, finalBaseHead: state.finalBaseHead, mergeStrategy: state.mergeStrategy,
		providerObservation: state.digest, reviewReadiness: readiness.digest,
		checkEvidence: checks, reviewEvidence: reviews, ownerReceipts: owners,
		providerResults: results, topologyDigest: topology,
	}
	canonical, err := canonicalProviderCompletionReceipt(receipt)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	receipt.digest = DigestBytes(canonical)
	return receipt, nil
}

func VerifyAndRecordProviderCompletion(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	broker *ProviderBroker,
	git ProviderCompletionGitPort,
	request ProviderCompletionRequest,
) (ProviderCompletionReceipt, JournalRecord, error) {
	if journal == nil || broker == nil || git == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion requires journal, broker, independent Git adapter, attempt, and occurrence time")
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	providerProjection, err := RebuildProviderRuntime(snapshot, definition)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	for _, receipt := range providerProjection.completionReceipts {
		if receipt.attemptID == request.AttemptID {
			return cloneProviderCompletionReceipt(receipt), JournalRecord{}, nil
		}
	}
	core, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	attempt, exists := core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || !attempt.serialSegmentHeld ||
		attempt.generation != definition.generation || attempt.repository != definition.workspace.repository {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion attempt does not match the active definition")
	}
	var openIntent, mergeIntent ProviderIntentProjection
	providerResults := make([]Digest, 0)
	for _, projected := range providerProjection.intents {
		if projected.intent.scope.attemptID != request.AttemptID {
			continue
		}
		if !projected.status.terminal() {
			return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider intent %s remains unresolved", projected.intent.intentID)
		}
		if !projected.result.digest.IsZero() {
			providerResults = append(providerResults, projected.result.digest)
		}
		if !projected.reconciliation.digest.IsZero() {
			providerResults = append(providerResults, projected.reconciliation.digest)
		}
		if !projected.preflight.digest.IsZero() {
			providerResults = append(providerResults, projected.preflight.digest)
		}
		if !providerProjectionEffectApplied(projected) {
			continue
		}
		switch projected.intent.kind {
		case ProviderIntentOpenPullRequest:
			if !openIntent.intent.intentID.IsZero() {
				return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion has multiple applied open-pull-request intents")
			}
			openIntent = projected
		case ProviderIntentMerge:
			if !mergeIntent.intent.intentID.IsZero() {
				return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion has multiple applied merge intents")
			}
			mergeIntent = projected
		}
	}
	pullRequest, hasPullRequest, err := providerPullRequestForAttempt(providerProjection, request.AttemptID)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	if openIntent.intent.intentID.IsZero() || mergeIntent.intent.intentID.IsZero() || !hasPullRequest {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion requires applied open-pull-request and merge results")
	}
	if mergeIntent.intent.pullRequest != pullRequest ||
		openIntent.intent.scope.repository != mergeIntent.intent.scope.repository ||
		openIntent.intent.baseRef != definition.workspace.baseRef {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion intent topology does not match durable pull request and workspace base")
	}
	preflight, hasPreflight := mergeIntent.MergePreflight()
	if !hasPreflight || preflight.intentID != mergeIntent.intent.intentID ||
		preflight.intentDigest != mergeIntent.intent.digest {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion requires durable pre-effect merge evidence")
	}
	if err := validateProviderMergeExecutionReadiness(
		snapshot, definition, attempt, mergeIntent.intent.head, mergeIntent.intent.tree,
	); err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	readiness, checkEvidence, reviewEvidence, err := providerCompletionLocalEvidence(
		snapshot, definition, attempt, mergeIntent.intent.head, mergeIntent.intent.tree,
	)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	query, err := NewProviderPullRequestQuery(definition.workspace.repository, pullRequest)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	providerState, err := broker.observePullRequest(ctx, query)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("observe provider completion: %w", err)
	}
	if providerState.baseRef != definition.workspace.baseRef || providerState.branch != openIntent.intent.branch ||
		providerState.head != mergeIntent.intent.head || providerState.headTree != mergeIntent.intent.tree ||
		providerState.remoteBranchHead != mergeIntent.intent.head ||
		providerState.baseHeadBeforeMerge != mergeIntent.intent.scope.frontier.base ||
		!providerState.merged || providerState.mergeStrategy != ProviderMergeCommit ||
		providerState.mergeCommit.IsZero() || providerState.finalBaseHead != providerState.mergeCommit {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion observation has wrong branch, head/tree, base, strategy, or merge state")
	}
	if err := validateProviderStateAgainstMergePreflight(preflight, providerState); err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	mergeIdentity, ok := providerProjectionMergeIdentity(mergeIntent)
	if !ok || mergeIdentity != providerState.mergeCommit {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("provider completion observation does not match durable merge result")
	}
	for _, check := range providerState.checks {
		checkEvidence = append(checkEvidence, check.evidence)
	}
	for _, review := range providerState.reviews {
		reviewEvidence = append(reviewEvidence, review.evidence)
	}
	repositoryRoot := definition.workspace.repositoryRoot
	inspection, err := git.InspectRemoteTopology(
		ctx, repositoryRoot, definition.workspace.remote, providerState.branch, definition.workspace.baseRef,
		providerState.head, providerState.mergeCommit, providerState.baseHeadBeforeMerge,
	)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git remote topology inspection failed: %w", err)
	}
	if inspection.remoteBranchHead != providerState.head {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git remote branch verification failed")
	}
	if inspection.finalBaseHead != providerState.mergeCommit || inspection.finalBaseHead != providerState.finalBaseHead {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git final base verification failed")
	}
	headCommit := inspection.headCommit
	if headCommit.commit != providerState.head || headCommit.tree != providerState.headTree {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git reviewed head/tree verification failed")
	}
	mergeCommit := inspection.mergeCommit
	if mergeCommit.commit != providerState.mergeCommit || len(mergeCommit.parents) != 2 ||
		mergeCommit.parents[0] != providerState.baseHeadBeforeMerge || mergeCommit.parents[1] != providerState.head ||
		mergeCommit.tree != providerState.headTree {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git merge parents or tree do not match exact merge topology")
	}
	if !inspection.baseAncestor {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git base ancestry verification failed")
	}
	if !inspection.headAncestor {
		return ProviderCompletionReceipt{}, JournalRecord{}, fmt.Errorf("independent Git head ancestry verification failed")
	}
	topologyDigest, err := providerTopologyDigest(providerState, headCommit, mergeCommit)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	authorization, err := RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	receipt, err := newProviderCompletionReceipt(
		definition, attempt, providerState, mergeCommit.tree, readiness, checkEvidence, reviewEvidence,
		authorization.receipts, providerResults, topologyDigest,
	)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	event, err := NewProviderCompletionVerifiedJournalEvent(
		definition.workspace.id, definition.generation, receipt,
	)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	record, err := appendProviderJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return ProviderCompletionReceipt{}, JournalRecord{}, err
	}
	return receipt, record, nil
}

func providerCompletionLocalEvidence(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	attempt RuntimeAttemptProjection,
	head, tree GitObjectID,
) (ReviewReadiness, []Digest, []Digest, error) {
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewReadiness{}, nil, nil, err
	}
	checkEvidence := make([]Digest, 0)
	if protocol, configured := unit.CommitProtocol(); configured {
		if attempt.commitProtocol == nil || attempt.commitProtocol.protocol.digest != protocol.digest ||
			attempt.commitProtocol.generation != definition.generation ||
			attempt.commitProtocol.phase != CommitProtocolComplete || attempt.commitProtocol.Head() != head {
			return ReviewReadiness{}, nil, nil, fmt.Errorf("provider completion commit protocol evidence is incomplete or stale")
		}
		completed, err := completedProtocolEffect(*attempt.commitProtocol)
		if err != nil {
			return ReviewReadiness{}, nil, nil, err
		}
		checkEvidence = append(checkEvidence, completed.evidence)
		for _, step := range attempt.commitProtocol.steps {
			for _, check := range step.checks {
				checkEvidence = append(checkEvidence, check.evidence)
			}
		}
	}
	if attempt.reviewFixes != nil {
		for index, fix := range attempt.reviewFixes.fixes {
			if fix.phase != ReviewFixComplete {
				return ReviewReadiness{}, nil, nil, fmt.Errorf("provider completion review-fix evidence is incomplete")
			}
			completed, err := completedReviewFixEffect(*attempt.reviewFixes, index)
			if err != nil {
				return ReviewReadiness{}, nil, nil, err
			}
			checkEvidence = append(checkEvidence, completed.evidence)
			for _, check := range fix.checks {
				checkEvidence = append(checkEvidence, check.evidence)
			}
		}
	}
	var readiness ReviewReadiness
	reviewEvidence := make([]Digest, 0)
	if _, configured := unit.ReviewLoop(); configured {
		projection, err := RebuildReviewRuntime(snapshot, definition)
		if err != nil {
			return ReviewReadiness{}, nil, nil, err
		}
		state, exists := projection.State(attempt.attemptID)
		if !exists || !state.MergeReady() || state.head != head || state.tree != tree {
			return ReviewReadiness{}, nil, nil, fmt.Errorf("provider completion review evidence is incomplete or stale")
		}
		readiness, err = newReviewMergeReadiness(definition, attempt, state)
		if err != nil {
			return ReviewReadiness{}, nil, nil, err
		}
		rounds := state.Rounds()
		last := rounds[len(rounds)-1]
		for _, result := range last.Results() {
			reviewEvidence = append(
				reviewEvidence,
				result.submission.digest,
				result.reservationDigest,
				result.receiptDigest,
			)
		}
		for _, fix := range state.Fixes() {
			reviewEvidence = append(reviewEvidence, fix.evidence)
		}
	}
	return readiness, checkEvidence, reviewEvidence, nil
}

func providerProjectionEffectApplied(projected ProviderIntentProjection) bool {
	if projected.status == ProviderIntentSucceeded {
		return true
	}
	return projected.status == ProviderIntentReconciled && projected.reconciliation.effectApplied
}

func providerProjectionMergeIdentity(projected ProviderIntentProjection) (GitObjectID, bool) {
	if projected.status == ProviderIntentSucceeded && !projected.result.mergeCommit.IsZero() {
		return projected.result.mergeCommit, true
	}
	if projected.status == ProviderIntentReconciled && projected.reconciliation.effectApplied &&
		!projected.reconciliation.mergeCommit.IsZero() {
		return projected.reconciliation.mergeCommit, true
	}
	return GitObjectID{}, false
}

func providerTopologyDigest(
	state ProviderPullRequestState,
	head, merge ProviderGitCommit,
) (Digest, error) {
	type canonical struct {
		SchemaVersion       int                   `json:"schema_version"`
		ProviderObservation string                `json:"provider_observation"`
		RemoteBranchHead    string                `json:"remote_branch_head"`
		BaseBeforeMerge     string                `json:"base_before_merge"`
		Head                string                `json:"head"`
		HeadTree            string                `json:"head_tree"`
		MergeCommit         string                `json:"merge_commit"`
		MergeFirstParent    string                `json:"merge_first_parent"`
		MergeSecondParent   string                `json:"merge_second_parent"`
		MergeTree           string                `json:"merge_tree"`
		FinalBaseHead       string                `json:"final_base_head"`
		MergeStrategy       ProviderMergeStrategy `json:"merge_strategy"`
	}
	if len(merge.parents) != 2 {
		return Digest{}, fmt.Errorf("provider topology requires exactly two merge parents")
	}
	content, err := json.Marshal(canonical{
		SchemaVersion: JournalSchemaVersion, ProviderObservation: state.digest.String(),
		RemoteBranchHead: state.remoteBranchHead.String(), BaseBeforeMerge: state.baseHeadBeforeMerge.String(),
		Head: head.commit.String(), HeadTree: head.tree.String(), MergeCommit: merge.commit.String(),
		MergeFirstParent: merge.parents[0].String(), MergeSecondParent: merge.parents[1].String(),
		MergeTree: merge.tree.String(), FinalBaseHead: state.finalBaseHead.String(),
		MergeStrategy: state.mergeStrategy,
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func DecodeProviderCompletionReceipt(source []byte) (ProviderCompletionReceipt, error) {
	var wire providerCompletionReceiptWire
	if err := decodeStrictJSON(source, &wire); err != nil {
		return ProviderCompletionReceipt{}, fmt.Errorf("decode provider completion receipt: %w", err)
	}
	receipt, err := providerCompletionReceiptFromWire(wire)
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	canonical, err := receipt.MarshalJSON()
	if err != nil {
		return ProviderCompletionReceipt{}, err
	}
	if !bytes.Equal(canonical, source) {
		return ProviderCompletionReceipt{}, fmt.Errorf("provider completion receipt is not canonical JSON")
	}
	return receipt, nil
}

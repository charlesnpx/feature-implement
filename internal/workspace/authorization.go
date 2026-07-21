package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type StandingAuthorizationAction string

const (
	StandingAuthorizationPush            StandingAuthorizationAction = "push"
	StandingAuthorizationOpenPullRequest StandingAuthorizationAction = "open_pull_request"
	StandingAuthorizationMerge           StandingAuthorizationAction = "merge"
)

func (action StandingAuthorizationAction) valid() bool {
	return action == StandingAuthorizationPush ||
		action == StandingAuthorizationOpenPullRequest ||
		action == StandingAuthorizationMerge
}

type AuthorizationFrontier struct {
	base GitObjectID
	head GitObjectID
}

func NewAuthorizationFrontier(base, head GitObjectID) (AuthorizationFrontier, error) {
	if base.IsZero() || head.IsZero() || base.Algorithm() != head.Algorithm() {
		return AuthorizationFrontier{}, fmt.Errorf("authorization frontier requires algorithm-matched base and head")
	}
	return AuthorizationFrontier{base: base, head: head}, nil
}

func (frontier AuthorizationFrontier) Base() GitObjectID { return frontier.base }
func (frontier AuthorizationFrontier) Head() GitObjectID { return frontier.head }

type StandingGrantScopeOptions struct {
	WorkspaceID   ID
	Repository    RepositoryIdentity
	Remote        string
	Generation    Digest
	SerialSegment ID
	Frontier      AuthorizationFrontier
	Actions       []StandingAuthorizationAction
	ExpiresAt     time.Time
	Epoch         uint64
	PullRequest   PullRequestIdentity
}

// StandingGrantScope is exact and immutable. A pre-PR scope has no pull
// request identity; only a provider-derived result can create the derived
// post-PR grant used for review-fix pushes and merge.
type StandingGrantScope struct {
	workspaceID   ID
	repository    RepositoryIdentity
	remote        string
	generation    Digest
	serialSegment ID
	frontier      AuthorizationFrontier
	actions       []StandingAuthorizationAction
	expiresAt     time.Time
	epoch         uint64
	pullRequest   PullRequestIdentity
}

func NewStandingGrantScope(options StandingGrantScopeOptions) (StandingGrantScope, error) {
	remote := strings.TrimSpace(options.Remote)
	if options.WorkspaceID.IsZero() || options.Repository.String() == "" || options.Generation.IsZero() ||
		options.SerialSegment.IsZero() || options.Frontier.base.IsZero() || options.Frontier.head.IsZero() ||
		len(options.Actions) == 0 || options.ExpiresAt.IsZero() || options.Epoch == 0 {
		return StandingGrantScope{}, fmt.Errorf("standing grant scope requires workspace, repository, remote, generation, segment, frontier, actions, expiry, and epoch")
	}
	if err := validateBoundedText("standing grant remote", remote, 512); err != nil {
		return StandingGrantScope{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") {
		return StandingGrantScope{}, fmt.Errorf("standing grant remote must be a single token")
	}
	if options.Frontier.base.Algorithm() != options.Frontier.head.Algorithm() {
		return StandingGrantScope{}, fmt.Errorf("standing grant frontier uses different Git object formats")
	}
	if !options.PullRequest.IsZero() && options.PullRequest.repository != options.Repository {
		return StandingGrantScope{}, fmt.Errorf("standing grant pull request belongs to a different repository")
	}
	actions := append([]StandingAuthorizationAction(nil), options.Actions...)
	seen := make(map[StandingAuthorizationAction]struct{}, len(actions))
	for _, action := range actions {
		if !action.valid() {
			return StandingGrantScope{}, fmt.Errorf("standing grant action %q is unsupported", action)
		}
		if _, exists := seen[action]; exists {
			return StandingGrantScope{}, fmt.Errorf("standing grant action %s is duplicated", action)
		}
		seen[action] = struct{}{}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	return StandingGrantScope{
		workspaceID: options.WorkspaceID, repository: options.Repository, remote: remote,
		generation: options.Generation, serialSegment: options.SerialSegment,
		frontier: options.Frontier, actions: actions, expiresAt: options.ExpiresAt.UTC(),
		epoch: options.Epoch, pullRequest: options.PullRequest,
	}, nil
}

func (scope StandingGrantScope) WorkspaceID() ID                 { return scope.workspaceID }
func (scope StandingGrantScope) Repository() RepositoryIdentity  { return scope.repository }
func (scope StandingGrantScope) Remote() string                  { return scope.remote }
func (scope StandingGrantScope) Generation() Digest              { return scope.generation }
func (scope StandingGrantScope) SerialSegment() ID               { return scope.serialSegment }
func (scope StandingGrantScope) Frontier() AuthorizationFrontier { return scope.frontier }
func (scope StandingGrantScope) ExpiresAt() time.Time            { return scope.expiresAt }
func (scope StandingGrantScope) Epoch() uint64                   { return scope.epoch }
func (scope StandingGrantScope) Actions() []StandingAuthorizationAction {
	return append([]StandingAuthorizationAction(nil), scope.actions...)
}
func (scope StandingGrantScope) PullRequest() (PullRequestIdentity, bool) {
	return scope.pullRequest, !scope.pullRequest.IsZero()
}
func (scope StandingGrantScope) Allows(action StandingAuthorizationAction) bool {
	for _, candidate := range scope.actions {
		if candidate == action {
			return true
		}
	}
	return false
}
func (scope StandingGrantScope) Digest() Digest {
	canonical, err := canonicalStandingGrantScope(scope)
	if err != nil {
		return Digest{}
	}
	return DigestBytes(canonical)
}

type StandingGrant struct {
	scope         StandingGrantScope
	grantID       Digest
	requestDigest Digest
	receiptDigest Digest
	parentGrantID Digest
}

func VerifyStandingGrant(
	ctx context.Context,
	verifier ControlPlaneVerifierPort,
	scope StandingGrantScope,
	receipt ControlPlaneReceiptV2,
) (StandingGrant, error) {
	if verifier == nil || scope.Digest().IsZero() || receipt.ReceiptDigest().IsZero() {
		return StandingGrant{}, fmt.Errorf("standing grant requires scope, receipt, and protected verifier")
	}
	if receipt.ExpiresAt().Before(scope.expiresAt) {
		return StandingGrant{}, fmt.Errorf("standing grant outlives its control-plane receipt")
	}
	binding, err := standingGrantControlPlaneBinding(scope)
	if err != nil {
		return StandingGrant{}, err
	}
	verification, err := NewControlPlaneVerification(binding)
	if err != nil {
		return StandingGrant{}, err
	}
	if err := verifier.Verify(ctx, verification, receipt); err != nil {
		return StandingGrant{}, fmt.Errorf("verify standing grant: %w", err)
	}
	return StandingGrant{
		scope: cloneStandingGrantScope(scope), grantID: scope.Digest(),
		requestDigest: scope.Digest(), receiptDigest: receipt.ReceiptDigest(),
	}, nil
}

func standingGrantControlPlaneBinding(scope StandingGrantScope) (ControlPlaneBinding, error) {
	return NewControlPlaneBinding(ControlPlaneBindingOptions{
		Kind: ControlPlaneReceiptStandingGrant, WorkspaceID: scope.workspaceID,
		Generation: scope.generation, RequestDigest: scope.Digest(), Repository: scope.repository,
		Remote: scope.remote, Base: scope.frontier.base, Head: scope.frontier.head,
		PullRequest: scope.pullRequest, Epoch: scope.epoch,
	})
}

func StandingGrantControlPlaneBinding(scope StandingGrantScope) (ControlPlaneBinding, error) {
	return standingGrantControlPlaneBinding(scope)
}

func (grant StandingGrant) Scope() StandingGrantScope { return cloneStandingGrantScope(grant.scope) }
func (grant StandingGrant) GrantID() Digest           { return grant.grantID }
func (grant StandingGrant) RequestDigest() Digest     { return grant.requestDigest }
func (grant StandingGrant) ReceiptDigest() Digest     { return grant.receiptDigest }
func (grant StandingGrant) ParentGrantID() (Digest, bool) {
	return grant.parentGrantID, !grant.parentGrantID.IsZero()
}

func DeriveStandingGrantPullRequest(
	grant StandingGrant,
	identity PullRequestIdentity,
	observedHead GitObjectID,
) (StandingGrant, error) {
	if grant.grantID.IsZero() || grant.scope.pullRequest.IsZero() == false || identity.IsZero() ||
		identity.repository != grant.scope.repository || observedHead != grant.scope.frontier.head ||
		!grant.scope.Allows(StandingAuthorizationOpenPullRequest) {
		return StandingGrant{}, fmt.Errorf("derived pull request does not match the pre-PR grant and frontier")
	}
	derivedScope := cloneStandingGrantScope(grant.scope)
	derivedScope.pullRequest = identity
	grantID, err := derivedStandingGrantID(grant.grantID, grant.receiptDigest, identity, observedHead)
	if err != nil {
		return StandingGrant{}, err
	}
	return StandingGrant{
		scope: derivedScope, grantID: grantID, requestDigest: grant.requestDigest,
		receiptDigest: grant.receiptDigest, parentGrantID: grant.grantID,
	}, nil
}

func derivedStandingGrantID(
	parentGrantID Digest,
	receiptDigest Digest,
	identity PullRequestIdentity,
	observedHead GitObjectID,
) (Digest, error) {
	if parentGrantID.IsZero() || receiptDigest.IsZero() || identity.IsZero() || observedHead.IsZero() {
		return Digest{}, fmt.Errorf("derived standing grant requires parent, receipt, pull request, and head")
	}
	type derivedJSON struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		ParentGrant   string `json:"parent_grant"`
		Receipt       string `json:"receipt"`
		Provider      string `json:"provider"`
		Repository    string `json:"repository"`
		Number        uint64 `json:"number"`
		Head          string `json:"head"`
	}
	canonical, err := json.Marshal(derivedJSON{
		SchemaVersion: 2, Kind: "derived_pull_request", ParentGrant: parentGrantID.String(),
		Receipt: receiptDigest.String(), Provider: identity.provider.String(),
		Repository: identity.repository.String(), Number: identity.number, Head: observedHead.String(),
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(canonical), nil
}

type AuthorizationRevocationOptions struct {
	WorkspaceID ID
	Repository  RepositoryIdentity
	Remote      string
	Generation  Digest
	TargetGrant Digest
	NextEpoch   uint64
	Reason      ID
}

type AuthorizationRevocation struct {
	workspaceID ID
	repository  RepositoryIdentity
	remote      string
	generation  Digest
	targetGrant Digest
	nextEpoch   uint64
	reason      ID
	digest      Digest
	receipt     Digest
}

func VerifyAuthorizationRevocation(
	ctx context.Context,
	verifier ControlPlaneVerifierPort,
	options AuthorizationRevocationOptions,
	receipt ControlPlaneReceiptV2,
) (AuthorizationRevocation, error) {
	revocation, err := newAuthorizationRevocation(options)
	if err != nil {
		return AuthorizationRevocation{}, err
	}
	if verifier == nil || receipt.ReceiptDigest().IsZero() {
		return AuthorizationRevocation{}, fmt.Errorf("authorization revocation requires a protected verifier and receipt")
	}
	binding, err := AuthorizationRevocationControlPlaneBinding(options)
	if err != nil {
		return AuthorizationRevocation{}, err
	}
	verification, _ := NewControlPlaneVerification(binding)
	if err := verifier.Verify(ctx, verification, receipt); err != nil {
		return AuthorizationRevocation{}, fmt.Errorf("verify authorization revocation: %w", err)
	}
	revocation.receipt = receipt.ReceiptDigest()
	return revocation, nil
}

func AuthorizationRevocationControlPlaneBinding(options AuthorizationRevocationOptions) (ControlPlaneBinding, error) {
	revocation, err := newAuthorizationRevocation(options)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	return NewControlPlaneBinding(ControlPlaneBindingOptions{
		Kind: ControlPlaneReceiptRevocation, WorkspaceID: revocation.workspaceID,
		Generation: revocation.generation, RequestDigest: revocation.digest,
		Repository: revocation.repository, Remote: revocation.remote, Epoch: revocation.nextEpoch,
	})
}

func newAuthorizationRevocation(options AuthorizationRevocationOptions) (AuthorizationRevocation, error) {
	remote := strings.TrimSpace(options.Remote)
	if options.WorkspaceID.IsZero() || options.Repository.String() == "" || options.Generation.IsZero() ||
		options.NextEpoch == 0 || options.Reason.IsZero() {
		return AuthorizationRevocation{}, fmt.Errorf("authorization revocation requires workspace, repository, generation, next epoch, and reason")
	}
	if err := validateBoundedText("authorization revocation remote", remote, 512); err != nil {
		return AuthorizationRevocation{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") {
		return AuthorizationRevocation{}, fmt.Errorf("authorization revocation remote must be a single token")
	}
	type revocationJSON struct {
		SchemaVersion int    `json:"schema_version"`
		WorkspaceID   string `json:"workspace_id"`
		Repository    string `json:"repository"`
		Remote        string `json:"remote"`
		Generation    string `json:"generation"`
		TargetGrant   string `json:"target_grant,omitempty"`
		NextEpoch     uint64 `json:"next_epoch"`
		Reason        string `json:"reason"`
	}
	canonical, err := json.Marshal(revocationJSON{
		SchemaVersion: 2, WorkspaceID: options.WorkspaceID.String(), Repository: options.Repository.String(),
		Remote: remote, Generation: options.Generation.String(), TargetGrant: options.TargetGrant.String(),
		NextEpoch: options.NextEpoch, Reason: options.Reason.String(),
	})
	if err != nil {
		return AuthorizationRevocation{}, err
	}
	return AuthorizationRevocation{
		workspaceID: options.WorkspaceID, repository: options.Repository, remote: remote,
		generation: options.Generation, targetGrant: options.TargetGrant,
		nextEpoch: options.NextEpoch, reason: options.Reason, digest: DigestBytes(canonical),
	}, nil
}

func (revocation AuthorizationRevocation) WorkspaceID() ID { return revocation.workspaceID }
func (revocation AuthorizationRevocation) Repository() RepositoryIdentity {
	return revocation.repository
}
func (revocation AuthorizationRevocation) Remote() string     { return revocation.remote }
func (revocation AuthorizationRevocation) Generation() Digest { return revocation.generation }
func (revocation AuthorizationRevocation) TargetGrant() (Digest, bool) {
	return revocation.targetGrant, !revocation.targetGrant.IsZero()
}
func (revocation AuthorizationRevocation) NextEpoch() uint64     { return revocation.nextEpoch }
func (revocation AuthorizationRevocation) Reason() ID            { return revocation.reason }
func (revocation AuthorizationRevocation) Digest() Digest        { return revocation.digest }
func (revocation AuthorizationRevocation) ReceiptDigest() Digest { return revocation.receipt }

type AuthorizationCheckpoint string

const (
	AuthorizationAtPlanning          AuthorizationCheckpoint = "planning"
	AuthorizationAtIntentReservation AuthorizationCheckpoint = "intent_reservation"
	AuthorizationAtQueueEntry        AuthorizationCheckpoint = "queue_entry"
	AuthorizationBeforeDispatch      AuthorizationCheckpoint = "before_dispatch"
)

func (checkpoint AuthorizationCheckpoint) valid() bool {
	return checkpoint == AuthorizationAtPlanning || checkpoint == AuthorizationAtIntentReservation ||
		checkpoint == AuthorizationAtQueueEntry || checkpoint == AuthorizationBeforeDispatch
}

type AuthorizationSafetyState struct {
	gatesBlocked          bool
	reconciliationPending bool
	driftDetected         bool
	ambiguousEffect       bool
}

func NewAuthorizationSafetyState(gatesBlocked, reconciliationPending, driftDetected, ambiguousEffect bool) AuthorizationSafetyState {
	return AuthorizationSafetyState{
		gatesBlocked: gatesBlocked, reconciliationPending: reconciliationPending,
		driftDetected: driftDetected, ambiguousEffect: ambiguousEffect,
	}
}

func (state AuthorizationSafetyState) GatesBlocked() bool { return state.gatesBlocked }
func (state AuthorizationSafetyState) ReconciliationPending() bool {
	return state.reconciliationPending
}
func (state AuthorizationSafetyState) DriftDetected() bool   { return state.driftDetected }
func (state AuthorizationSafetyState) AmbiguousEffect() bool { return state.ambiguousEffect }

type AuthorizationRequestOptions struct {
	WorkspaceID   ID
	Repository    RepositoryIdentity
	Remote        string
	Generation    Digest
	SerialSegment ID
	Frontier      AuthorizationFrontier
	Action        StandingAuthorizationAction
	PullRequest   PullRequestIdentity
	Epoch         uint64
}

type AuthorizationRequest struct {
	workspaceID   ID
	repository    RepositoryIdentity
	remote        string
	generation    Digest
	serialSegment ID
	frontier      AuthorizationFrontier
	action        StandingAuthorizationAction
	pullRequest   PullRequestIdentity
	epoch         uint64
	digest        Digest
}

func NewAuthorizationRequest(options AuthorizationRequestOptions) (AuthorizationRequest, error) {
	remote := strings.TrimSpace(options.Remote)
	if options.WorkspaceID.IsZero() || options.Repository.String() == "" || options.Generation.IsZero() ||
		options.SerialSegment.IsZero() || options.Frontier.base.IsZero() || options.Frontier.head.IsZero() ||
		!options.Action.valid() || options.Epoch == 0 {
		return AuthorizationRequest{}, fmt.Errorf("authorization request requires exact workspace, repository, generation, segment, frontier, action, and epoch")
	}
	if err := validateBoundedText("authorization request remote", remote, 512); err != nil {
		return AuthorizationRequest{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") {
		return AuthorizationRequest{}, fmt.Errorf("authorization request remote must be a single token")
	}
	if !options.PullRequest.IsZero() && options.PullRequest.repository != options.Repository {
		return AuthorizationRequest{}, fmt.Errorf("authorization request pull request belongs to a different repository")
	}
	type requestJSON struct {
		SchemaVersion int                          `json:"schema_version"`
		WorkspaceID   string                       `json:"workspace_id"`
		Repository    string                       `json:"repository"`
		Remote        string                       `json:"remote"`
		Generation    string                       `json:"generation"`
		SerialSegment string                       `json:"serial_segment"`
		Base          string                       `json:"base"`
		Head          string                       `json:"head"`
		Action        StandingAuthorizationAction  `json:"action"`
		PullRequest   *controlPlanePullRequestWire `json:"pull_request,omitempty"`
		Epoch         uint64                       `json:"epoch"`
	}
	wire := requestJSON{
		SchemaVersion: 2, WorkspaceID: options.WorkspaceID.String(), Repository: options.Repository.String(),
		Remote: remote, Generation: options.Generation.String(), SerialSegment: options.SerialSegment.String(),
		Base: options.Frontier.base.String(), Head: options.Frontier.head.String(), Action: options.Action, Epoch: options.Epoch,
	}
	if !options.PullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: options.PullRequest.provider.String(), Repository: options.PullRequest.repository.String(),
			Number: options.PullRequest.number,
		}
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return AuthorizationRequest{}, err
	}
	return AuthorizationRequest{
		workspaceID: options.WorkspaceID, repository: options.Repository, remote: remote,
		generation: options.Generation, serialSegment: options.SerialSegment,
		frontier: options.Frontier, action: options.Action, pullRequest: options.PullRequest,
		epoch: options.Epoch, digest: DigestBytes(canonical),
	}, nil
}

func (request AuthorizationRequest) Digest() Digest { return request.digest }

type AuthorizationCapability struct {
	grantID       Digest
	requestDigest Digest
	stateDigest   Digest
	checkpoint    AuthorizationCheckpoint
	epoch         uint64
	evaluatedAt   time.Time
	expiresAt     time.Time
}

func (capability AuthorizationCapability) GrantID() Digest       { return capability.grantID }
func (capability AuthorizationCapability) RequestDigest() Digest { return capability.requestDigest }
func (capability AuthorizationCapability) StateDigest() Digest   { return capability.stateDigest }
func (capability AuthorizationCapability) Checkpoint() AuthorizationCheckpoint {
	return capability.checkpoint
}
func (capability AuthorizationCapability) Epoch() uint64          { return capability.epoch }
func (capability AuthorizationCapability) EvaluatedAt() time.Time { return capability.evaluatedAt }
func (capability AuthorizationCapability) ExpiresAt() time.Time   { return capability.expiresAt }

type AuthorizationDispatchObligation struct {
	effectID      ID
	requestDigest Digest
	grantID       Digest
	epoch         uint64
}

func (obligation AuthorizationDispatchObligation) EffectID() ID { return obligation.effectID }
func (obligation AuthorizationDispatchObligation) RequestDigest() Digest {
	return obligation.requestDigest
}
func (obligation AuthorizationDispatchObligation) GrantID() Digest { return obligation.grantID }
func (obligation AuthorizationDispatchObligation) Epoch() uint64   { return obligation.epoch }

type AuthorizationState struct {
	workspaceID       ID
	repository        RepositoryIdentity
	remote            string
	generation        Digest
	epoch             uint64
	grants            []StandingGrant
	revokedGrantIDs   []Digest
	completedSegments []ID
	safety            AuthorizationSafetyState
	obligations       []AuthorizationDispatchObligation
}

func NewAuthorizationState(
	workspaceID ID,
	repository RepositoryIdentity,
	remote string,
	generation Digest,
	epoch uint64,
) (AuthorizationState, error) {
	remote = strings.TrimSpace(remote)
	if workspaceID.IsZero() || repository.String() == "" || generation.IsZero() || epoch == 0 {
		return AuthorizationState{}, fmt.Errorf("authorization state requires workspace, repository, generation, and epoch")
	}
	if err := validateBoundedText("authorization state remote", remote, 512); err != nil {
		return AuthorizationState{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") {
		return AuthorizationState{}, fmt.Errorf("authorization state remote must be a single token")
	}
	return AuthorizationState{
		workspaceID: workspaceID, repository: repository, remote: remote,
		generation: generation, epoch: epoch,
	}, nil
}

func (state AuthorizationState) Epoch() uint64 { return state.epoch }
func (state AuthorizationState) Grants() []StandingGrant {
	result := append([]StandingGrant(nil), state.grants...)
	for index := range result {
		result[index].scope = cloneStandingGrantScope(result[index].scope)
	}
	return result
}
func (state AuthorizationState) CompletedSegments() []ID {
	return append([]ID(nil), state.completedSegments...)
}
func (state AuthorizationState) OutstandingReconciliationObligations() []AuthorizationDispatchObligation {
	return append([]AuthorizationDispatchObligation(nil), state.obligations...)
}

type AuthorizationEvent interface{ isAuthorizationEvent() }

type AuthorizationGrantRecorded struct{ grant StandingGrant }

func NewAuthorizationGrantRecorded(grant StandingGrant) (AuthorizationGrantRecorded, error) {
	if grant.grantID.IsZero() || grant.receiptDigest.IsZero() {
		return AuthorizationGrantRecorded{}, fmt.Errorf("authorization grant event requires verified grant evidence")
	}
	return AuthorizationGrantRecorded{grant: grant}, nil
}
func (AuthorizationGrantRecorded) isAuthorizationEvent() {}

type AuthorizationRevoked struct{ revocation AuthorizationRevocation }

func NewAuthorizationRevoked(revocation AuthorizationRevocation) (AuthorizationRevoked, error) {
	if revocation.digest.IsZero() || revocation.receipt.IsZero() {
		return AuthorizationRevoked{}, fmt.Errorf("authorization revocation event requires verified receipt evidence")
	}
	return AuthorizationRevoked{revocation: revocation}, nil
}
func (AuthorizationRevoked) isAuthorizationEvent() {}

type AuthorizationSegmentCompleted struct{ segment ID }

func NewAuthorizationSegmentCompleted(segment ID) (AuthorizationSegmentCompleted, error) {
	if segment.IsZero() {
		return AuthorizationSegmentCompleted{}, fmt.Errorf("authorization segment completion requires a segment")
	}
	return AuthorizationSegmentCompleted{segment: segment}, nil
}
func (AuthorizationSegmentCompleted) isAuthorizationEvent() {}

type AuthorizationSafetyChanged struct{ safety AuthorizationSafetyState }

func NewAuthorizationSafetyChanged(safety AuthorizationSafetyState) AuthorizationSafetyChanged {
	return AuthorizationSafetyChanged{safety: safety}
}
func (AuthorizationSafetyChanged) isAuthorizationEvent() {}

type AuthorizationEffectDispatched struct {
	effectID     ID
	capability   AuthorizationCapability
	dispatchedAt time.Time
}

func NewAuthorizationEffectDispatched(
	effectID ID,
	capability AuthorizationCapability,
	dispatchedAt time.Time,
) (AuthorizationEffectDispatched, error) {
	if effectID.IsZero() || capability.grantID.IsZero() || capability.requestDigest.IsZero() ||
		capability.stateDigest.IsZero() || capability.checkpoint != AuthorizationBeforeDispatch || capability.epoch == 0 ||
		capability.evaluatedAt.IsZero() || dispatchedAt.IsZero() || dispatchedAt.Before(capability.evaluatedAt) ||
		!dispatchedAt.Before(capability.expiresAt) {
		return AuthorizationEffectDispatched{}, fmt.Errorf("dispatched effect requires a pre-dispatch capability")
	}
	return AuthorizationEffectDispatched{
		effectID: effectID, capability: capability, dispatchedAt: dispatchedAt.UTC(),
	}, nil
}
func (AuthorizationEffectDispatched) isAuthorizationEvent() {}

func ReduceAuthorization(current AuthorizationState, event AuthorizationEvent) (AuthorizationState, error) {
	if current.workspaceID.IsZero() || current.repository.String() == "" || current.generation.IsZero() || current.epoch == 0 {
		return AuthorizationState{}, fmt.Errorf("authorization reducer requires initialized state")
	}
	if event == nil {
		return AuthorizationState{}, fmt.Errorf("authorization event is required")
	}
	next := cloneAuthorizationState(current)
	switch value := event.(type) {
	case AuthorizationGrantRecorded:
		scope := value.grant.scope
		if scope.workspaceID != current.workspaceID || scope.repository != current.repository ||
			scope.remote != current.remote || scope.generation != current.generation || scope.epoch != current.epoch {
			return AuthorizationState{}, fmt.Errorf("standing grant does not match the current authorization epoch")
		}
		for _, grant := range current.grants {
			if grant.grantID == value.grant.grantID {
				return AuthorizationState{}, fmt.Errorf("standing grant %s is already recorded", value.grant.grantID)
			}
		}
		if !value.grant.parentGrantID.IsZero() {
			var parent StandingGrant
			for _, grant := range current.grants {
				if grant.grantID == value.grant.parentGrantID {
					parent = grant
					break
				}
			}
			if parent.grantID.IsZero() {
				return AuthorizationState{}, fmt.Errorf("derived standing grant has no durable parent %s", value.grant.parentGrantID)
			}
			derived, err := DeriveStandingGrantPullRequest(parent, scope.pullRequest, scope.frontier.head)
			if err != nil || derived.grantID != value.grant.grantID ||
				derived.requestDigest != value.grant.requestDigest || derived.receiptDigest != value.grant.receiptDigest ||
				derived.scope.Digest() != value.grant.scope.Digest() {
				return AuthorizationState{}, fmt.Errorf("derived standing grant does not match its durable parent")
			}
		}
		next.grants = append(next.grants, value.grant)
		sort.Slice(next.grants, func(i, j int) bool { return next.grants[i].grantID.String() < next.grants[j].grantID.String() })
	case AuthorizationRevoked:
		revocation := value.revocation
		if revocation.workspaceID != current.workspaceID || revocation.repository != current.repository ||
			revocation.remote != current.remote || revocation.generation != current.generation ||
			revocation.nextEpoch != current.epoch+1 {
			return AuthorizationState{}, fmt.Errorf("authorization revocation does not advance the current epoch")
		}
		if !revocation.targetGrant.IsZero() {
			found := false
			for _, grant := range current.grants {
				if grant.grantID == revocation.targetGrant || grant.parentGrantID == revocation.targetGrant {
					found = true
					break
				}
			}
			if !found {
				return AuthorizationState{}, fmt.Errorf("authorization revocation targets unknown grant %s", revocation.targetGrant)
			}
			next.revokedGrantIDs = append(next.revokedGrantIDs, revocation.targetGrant)
		}
		next.epoch = revocation.nextEpoch
		next.grants = nil
	case AuthorizationSegmentCompleted:
		for _, segment := range current.completedSegments {
			if segment == value.segment {
				return current, nil
			}
		}
		next.completedSegments = append(next.completedSegments, value.segment)
		sort.Slice(next.completedSegments, func(i, j int) bool {
			return next.completedSegments[i].String() < next.completedSegments[j].String()
		})
	case AuthorizationSafetyChanged:
		next.safety = value.safety
	case AuthorizationEffectDispatched:
		if value.capability.epoch != current.epoch ||
			value.capability.stateDigest != authorizationStateDigest(current) {
			return AuthorizationState{}, fmt.Errorf("authorization capability has stale epoch or state bindings")
		}
		for _, obligation := range current.obligations {
			if obligation.effectID == value.effectID {
				return AuthorizationState{}, fmt.Errorf("effect %s is already a reconciliation obligation", value.effectID)
			}
		}
		next.obligations = append(next.obligations, AuthorizationDispatchObligation{
			effectID: value.effectID, requestDigest: value.capability.requestDigest,
			grantID: value.capability.grantID, epoch: value.capability.epoch,
		})
	default:
		return AuthorizationState{}, fmt.Errorf("unsupported authorization event %T", event)
	}
	return next, nil
}

func PlanAuthorization(
	state AuthorizationState,
	request AuthorizationRequest,
	now time.Time,
) (AuthorizationCapability, error) {
	return evaluateAuthorization(state, request, AuthorizationAtPlanning, AuthorizationCapability{}, now)
}

func ReserveAuthorizationIntent(
	state AuthorizationState,
	request AuthorizationRequest,
	planning AuthorizationCapability,
	now time.Time,
) (AuthorizationCapability, error) {
	return evaluateAuthorization(state, request, AuthorizationAtIntentReservation, planning, now)
}

func EnterAuthorizationQueue(
	state AuthorizationState,
	request AuthorizationRequest,
	reservation AuthorizationCapability,
	now time.Time,
) (AuthorizationCapability, error) {
	return evaluateAuthorization(state, request, AuthorizationAtQueueEntry, reservation, now)
}

func AuthorizeImmediatelyBeforeDispatch(
	state AuthorizationState,
	request AuthorizationRequest,
	queueEntry AuthorizationCapability,
	now time.Time,
) (AuthorizationCapability, error) {
	return evaluateAuthorization(state, request, AuthorizationBeforeDispatch, queueEntry, now)
}

func evaluateAuthorization(
	state AuthorizationState,
	request AuthorizationRequest,
	checkpoint AuthorizationCheckpoint,
	prior AuthorizationCapability,
	now time.Time,
) (AuthorizationCapability, error) {
	if !checkpoint.valid() || now.IsZero() || request.digest.IsZero() {
		return AuthorizationCapability{}, fmt.Errorf("authorization evaluation requires checkpoint, request, and time")
	}
	if checkpoint == AuthorizationAtPlanning {
		if !prior.grantID.IsZero() || !prior.requestDigest.IsZero() || !prior.stateDigest.IsZero() ||
			prior.checkpoint != "" || prior.epoch != 0 || !prior.evaluatedAt.IsZero() {
			return AuthorizationCapability{}, fmt.Errorf("planning authorization cannot reuse a prior capability")
		}
	} else {
		expectedPrior := AuthorizationAtPlanning
		switch checkpoint {
		case AuthorizationAtQueueEntry:
			expectedPrior = AuthorizationAtIntentReservation
		case AuthorizationBeforeDispatch:
			expectedPrior = AuthorizationAtQueueEntry
		}
		if prior.checkpoint != expectedPrior || prior.requestDigest != request.digest ||
			prior.grantID.IsZero() || prior.stateDigest.IsZero() || prior.evaluatedAt.IsZero() ||
			now.Before(prior.evaluatedAt) || !now.Before(prior.expiresAt) {
			return AuthorizationCapability{}, fmt.Errorf("authorization checkpoint %s requires a fresh matching %s capability", checkpoint, expectedPrior)
		}
	}
	if state.workspaceID != request.workspaceID || state.repository != request.repository ||
		state.remote != request.remote || state.generation != request.generation || state.epoch != request.epoch {
		return AuthorizationCapability{}, fmt.Errorf("authorization request has stale workspace, generation, repository, remote, or epoch bindings")
	}
	if state.safety.gatesBlocked {
		return AuthorizationCapability{}, fmt.Errorf("authorization is blocked by a protected gate")
	}
	if state.safety.reconciliationPending {
		return AuthorizationCapability{}, fmt.Errorf("authorization is blocked by reconciliation")
	}
	if state.safety.driftDetected {
		return AuthorizationCapability{}, fmt.Errorf("authorization is blocked by provider or Git drift")
	}
	if state.safety.ambiguousEffect {
		return AuthorizationCapability{}, fmt.Errorf("authorization is blocked by an ambiguous provider effect")
	}
	if len(state.obligations) != 0 {
		return AuthorizationCapability{}, fmt.Errorf("authorization is blocked by dispatched effects awaiting reconciliation")
	}
	for _, segment := range state.completedSegments {
		if segment == request.serialSegment {
			return AuthorizationCapability{}, fmt.Errorf("authorization serial segment %s is complete", segment)
		}
	}
	matches := make([]StandingGrant, 0, 1)
	for _, grant := range state.grants {
		if standingGrantMatchesRequest(grant, request, now.UTC()) {
			matches = append(matches, grant)
		}
	}
	if len(matches) == 0 {
		return AuthorizationCapability{}, fmt.Errorf("no standing grant authorizes the exact request")
	}
	if len(matches) != 1 {
		return AuthorizationCapability{}, fmt.Errorf("standing grant authorization is ambiguous")
	}
	grant := matches[0]
	stateDigest := authorizationStateDigest(state)
	if stateDigest.IsZero() {
		return AuthorizationCapability{}, fmt.Errorf("authorization state cannot be bound to a capability")
	}
	if checkpoint != AuthorizationAtPlanning &&
		(prior.epoch != state.epoch || prior.stateDigest != stateDigest || prior.grantID != grant.grantID) {
		return AuthorizationCapability{}, fmt.Errorf("authorization state or grant changed between %s and %s", prior.checkpoint, checkpoint)
	}
	return AuthorizationCapability{
		grantID: grant.grantID, requestDigest: request.digest, stateDigest: stateDigest,
		checkpoint: checkpoint, epoch: state.epoch, evaluatedAt: now.UTC(), expiresAt: grant.scope.expiresAt,
	}, nil
}

func standingGrantMatchesRequest(grant StandingGrant, request AuthorizationRequest, now time.Time) bool {
	scope := grant.scope
	if scope.workspaceID != request.workspaceID || scope.repository != request.repository || scope.remote != request.remote ||
		scope.generation != request.generation || scope.serialSegment != request.serialSegment ||
		scope.frontier != request.frontier || scope.epoch != request.epoch ||
		!now.Before(scope.expiresAt) || !scope.Allows(request.action) {
		return false
	}
	if scope.pullRequest.IsZero() {
		return request.pullRequest.IsZero() && request.action != StandingAuthorizationMerge
	}
	if request.pullRequest != scope.pullRequest {
		return false
	}
	return request.action != StandingAuthorizationOpenPullRequest
}

func canonicalStandingGrantScope(scope StandingGrantScope) ([]byte, error) {
	if scope.workspaceID.IsZero() || scope.repository.String() == "" || scope.generation.IsZero() ||
		scope.serialSegment.IsZero() || scope.frontier.base.IsZero() || scope.frontier.head.IsZero() ||
		len(scope.actions) == 0 || scope.expiresAt.IsZero() || scope.epoch == 0 {
		return nil, fmt.Errorf("standing grant scope is incomplete")
	}
	type scopeJSON struct {
		SchemaVersion int                           `json:"schema_version"`
		WorkspaceID   string                        `json:"workspace_id"`
		Repository    string                        `json:"repository"`
		Remote        string                        `json:"remote"`
		Generation    string                        `json:"generation"`
		SerialSegment string                        `json:"serial_segment"`
		Base          string                        `json:"base"`
		Head          string                        `json:"head"`
		Actions       []StandingAuthorizationAction `json:"actions"`
		ExpiresAt     string                        `json:"expires_at"`
		Epoch         uint64                        `json:"epoch"`
		PullRequest   *controlPlanePullRequestWire  `json:"pull_request,omitempty"`
	}
	wire := scopeJSON{
		SchemaVersion: 2, WorkspaceID: scope.workspaceID.String(), Repository: scope.repository.String(),
		Remote: scope.remote, Generation: scope.generation.String(), SerialSegment: scope.serialSegment.String(),
		Base: scope.frontier.base.String(), Head: scope.frontier.head.String(),
		Actions:   append([]StandingAuthorizationAction(nil), scope.actions...),
		ExpiresAt: scope.expiresAt.UTC().Format(time.RFC3339Nano), Epoch: scope.epoch,
	}
	if !scope.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: scope.pullRequest.provider.String(), Repository: scope.pullRequest.repository.String(),
			Number: scope.pullRequest.number,
		}
	}
	return json.Marshal(wire)
}

func cloneStandingGrantScope(scope StandingGrantScope) StandingGrantScope {
	scope.actions = append([]StandingAuthorizationAction(nil), scope.actions...)
	return scope
}

func cloneAuthorizationState(state AuthorizationState) AuthorizationState {
	state.grants = append([]StandingGrant(nil), state.grants...)
	for index := range state.grants {
		state.grants[index].scope = cloneStandingGrantScope(state.grants[index].scope)
	}
	state.revokedGrantIDs = append([]Digest(nil), state.revokedGrantIDs...)
	state.completedSegments = append([]ID(nil), state.completedSegments...)
	state.obligations = append([]AuthorizationDispatchObligation(nil), state.obligations...)
	return state
}

func authorizationStateDigest(state AuthorizationState) Digest {
	type grantJSON struct {
		GrantID       string `json:"grant_id"`
		RequestDigest string `json:"request_digest"`
		ReceiptDigest string `json:"receipt_digest"`
		ParentGrantID string `json:"parent_grant_id,omitempty"`
	}
	type obligationJSON struct {
		EffectID      string `json:"effect_id"`
		RequestDigest string `json:"request_digest"`
		GrantID       string `json:"grant_id"`
		Epoch         uint64 `json:"epoch"`
	}
	type stateJSON struct {
		SchemaVersion         int              `json:"schema_version"`
		WorkspaceID           string           `json:"workspace_id"`
		Repository            string           `json:"repository"`
		Remote                string           `json:"remote"`
		Generation            string           `json:"generation"`
		Epoch                 uint64           `json:"epoch"`
		Grants                []grantJSON      `json:"grants"`
		RevokedGrantIDs       []string         `json:"revoked_grant_ids"`
		CompletedSegments     []string         `json:"completed_segments"`
		GatesBlocked          bool             `json:"gates_blocked"`
		ReconciliationPending bool             `json:"reconciliation_pending"`
		DriftDetected         bool             `json:"drift_detected"`
		AmbiguousEffect       bool             `json:"ambiguous_effect"`
		Obligations           []obligationJSON `json:"obligations"`
	}
	if state.workspaceID.IsZero() || state.repository.String() == "" || state.generation.IsZero() || state.epoch == 0 {
		return Digest{}
	}
	wire := stateJSON{
		SchemaVersion: 2, WorkspaceID: state.workspaceID.String(), Repository: state.repository.String(),
		Remote: state.remote, Generation: state.generation.String(), Epoch: state.epoch,
		GatesBlocked: state.safety.gatesBlocked, ReconciliationPending: state.safety.reconciliationPending,
		DriftDetected: state.safety.driftDetected, AmbiguousEffect: state.safety.ambiguousEffect,
	}
	for _, grant := range state.grants {
		wire.Grants = append(wire.Grants, grantJSON{
			GrantID: grant.grantID.String(), RequestDigest: grant.requestDigest.String(),
			ReceiptDigest: grant.receiptDigest.String(), ParentGrantID: grant.parentGrantID.String(),
		})
	}
	for _, grantID := range state.revokedGrantIDs {
		wire.RevokedGrantIDs = append(wire.RevokedGrantIDs, grantID.String())
	}
	for _, segment := range state.completedSegments {
		wire.CompletedSegments = append(wire.CompletedSegments, segment.String())
	}
	for _, obligation := range state.obligations {
		wire.Obligations = append(wire.Obligations, obligationJSON{
			EffectID: obligation.effectID.String(), RequestDigest: obligation.requestDigest.String(),
			GrantID: obligation.grantID.String(), Epoch: obligation.epoch,
		})
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Digest{}
	}
	return DigestBytes(canonical)
}

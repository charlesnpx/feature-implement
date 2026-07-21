package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The adapter request types are closed, typed provider inputs. None can carry
// an executable, argv, shell fragment, arbitrary provider action, delete ref,
// or close-without-merge operation.
type ProviderPushRequest struct{ intent ProviderIntent }

func (request ProviderPushRequest) Repository() RepositoryIdentity {
	return request.intent.scope.repository
}
func (request ProviderPushRequest) Remote() string    { return request.intent.scope.remote }
func (request ProviderPushRequest) Branch() string    { return request.intent.branch }
func (request ProviderPushRequest) Head() GitObjectID { return request.intent.head }
func (request ProviderPushRequest) ExpectedRemoteHead() (GitObjectID, bool) {
	return request.intent.expectedRemote, !request.intent.expectedRemote.IsZero()
}
func (request ProviderPushRequest) PullRequest() (PullRequestIdentity, bool) {
	return request.intent.pullRequest, !request.intent.pullRequest.IsZero()
}
func (request ProviderPushRequest) IdempotencyKey() Digest { return request.intent.idempotencyKey }

type ProviderOpenPullRequestRequest struct{ intent ProviderIntent }

func (request ProviderOpenPullRequestRequest) Repository() RepositoryIdentity {
	return request.intent.scope.repository
}
func (request ProviderOpenPullRequestRequest) Remote() string    { return request.intent.scope.remote }
func (request ProviderOpenPullRequestRequest) Branch() string    { return request.intent.branch }
func (request ProviderOpenPullRequestRequest) BaseRef() string   { return request.intent.baseRef }
func (request ProviderOpenPullRequestRequest) Head() GitObjectID { return request.intent.head }
func (request ProviderOpenPullRequestRequest) Tree() GitObjectID { return request.intent.tree }
func (request ProviderOpenPullRequestRequest) Title() string     { return request.intent.title }
func (request ProviderOpenPullRequestRequest) Body() string      { return request.intent.body }
func (request ProviderOpenPullRequestRequest) IdempotencyKey() Digest {
	return request.intent.idempotencyKey
}

type ProviderMergeRequest struct{ intent ProviderIntent }

func (request ProviderMergeRequest) Repository() RepositoryIdentity {
	return request.intent.scope.repository
}
func (request ProviderMergeRequest) Remote() string  { return request.intent.scope.remote }
func (request ProviderMergeRequest) Branch() string  { return request.intent.branch }
func (request ProviderMergeRequest) BaseRef() string { return request.intent.baseRef }
func (request ProviderMergeRequest) PullRequest() PullRequestIdentity {
	return request.intent.pullRequest
}
func (request ProviderMergeRequest) ExpectedBaseHead() GitObjectID {
	return request.intent.scope.frontier.base
}
func (request ProviderMergeRequest) Head() GitObjectID { return request.intent.head }
func (request ProviderMergeRequest) Tree() GitObjectID { return request.intent.tree }
func (request ProviderMergeRequest) Strategy() ProviderMergeStrategy {
	return request.intent.mergeStrategy
}
func (request ProviderMergeRequest) IdempotencyKey() Digest { return request.intent.idempotencyKey }

type ProviderPushAdapterResult struct {
	requestMarker string
	remoteHead    GitObjectID
}

func NewProviderPushAdapterResult(requestMarker string, remoteHead GitObjectID) (ProviderPushAdapterResult, error) {
	if err := validateProviderAdapterMarker(requestMarker); err != nil || remoteHead.IsZero() {
		if err != nil {
			return ProviderPushAdapterResult{}, err
		}
		return ProviderPushAdapterResult{}, fmt.Errorf("provider push adapter result requires remote head")
	}
	return ProviderPushAdapterResult{requestMarker: requestMarker, remoteHead: remoteHead}, nil
}

func (result ProviderPushAdapterResult) RequestMarker() string   { return result.requestMarker }
func (result ProviderPushAdapterResult) RemoteHead() GitObjectID { return result.remoteHead }

type ProviderOpenPullRequestAdapterResult struct {
	requestMarker string
	number        uint64
	head          GitObjectID
}

func NewProviderOpenPullRequestAdapterResult(
	requestMarker string,
	number uint64,
	head GitObjectID,
) (ProviderOpenPullRequestAdapterResult, error) {
	if err := validateProviderAdapterMarker(requestMarker); err != nil || number == 0 || head.IsZero() {
		if err != nil {
			return ProviderOpenPullRequestAdapterResult{}, err
		}
		return ProviderOpenPullRequestAdapterResult{}, fmt.Errorf("provider open-pull-request adapter result requires number and head")
	}
	return ProviderOpenPullRequestAdapterResult{requestMarker: requestMarker, number: number, head: head}, nil
}

func (result ProviderOpenPullRequestAdapterResult) RequestMarker() string {
	return result.requestMarker
}
func (result ProviderOpenPullRequestAdapterResult) Number() uint64    { return result.number }
func (result ProviderOpenPullRequestAdapterResult) Head() GitObjectID { return result.head }

type ProviderMergeAdapterResult struct {
	requestMarker string
	mergeCommit   GitObjectID
	finalBaseHead GitObjectID
}

func NewProviderMergeAdapterResult(
	requestMarker string,
	mergeCommit, finalBaseHead GitObjectID,
) (ProviderMergeAdapterResult, error) {
	if err := validateProviderAdapterMarker(requestMarker); err != nil || mergeCommit.IsZero() || finalBaseHead.IsZero() ||
		mergeCommit.Algorithm() != finalBaseHead.Algorithm() {
		if err != nil {
			return ProviderMergeAdapterResult{}, err
		}
		return ProviderMergeAdapterResult{}, fmt.Errorf("provider merge adapter result requires algorithm-matched merge and final base heads")
	}
	return ProviderMergeAdapterResult{
		requestMarker: requestMarker, mergeCommit: mergeCommit, finalBaseHead: finalBaseHead,
	}, nil
}

func (result ProviderMergeAdapterResult) RequestMarker() string      { return result.requestMarker }
func (result ProviderMergeAdapterResult) MergeCommit() GitObjectID   { return result.mergeCommit }
func (result ProviderMergeAdapterResult) FinalBaseHead() GitObjectID { return result.finalBaseHead }

func validateProviderAdapterMarker(value string) error {
	return validateBoundedText("provider request marker", value, 2048)
}

type ProviderAdapterFailureKind string

const (
	ProviderAdapterFailedBeforeEffect ProviderAdapterFailureKind = "failed_before_effect"
	ProviderAdapterFailedAfterEffect  ProviderAdapterFailureKind = "failed_after_effect"
	ProviderAdapterAmbiguous          ProviderAdapterFailureKind = "ambiguous"
)

type ProviderAdapterFailure struct {
	kind   ProviderAdapterFailureKind
	marker string
	cause  error
}

func NewProviderAdapterFailure(
	kind ProviderAdapterFailureKind,
	marker string,
	cause error,
) (ProviderAdapterFailure, error) {
	if kind != ProviderAdapterFailedBeforeEffect && kind != ProviderAdapterFailedAfterEffect && kind != ProviderAdapterAmbiguous {
		return ProviderAdapterFailure{}, fmt.Errorf("unsupported provider adapter failure kind %q", kind)
	}
	if err := validateProviderAdapterMarker(marker); err != nil {
		return ProviderAdapterFailure{}, err
	}
	if cause == nil {
		cause = fmt.Errorf("provider adapter reported %s", kind)
	}
	return ProviderAdapterFailure{kind: kind, marker: marker, cause: cause}, nil
}

func (failure ProviderAdapterFailure) Error() string                    { return failure.cause.Error() }
func (failure ProviderAdapterFailure) Unwrap() error                    { return failure.cause }
func (failure ProviderAdapterFailure) Kind() ProviderAdapterFailureKind { return failure.kind }
func (failure ProviderAdapterFailure) RequestMarker() string            { return failure.marker }

type ProviderReconciliationDisposition string

const (
	ProviderEffectApplied    ProviderReconciliationDisposition = "applied"
	ProviderEffectNotApplied ProviderReconciliationDisposition = "not_applied"
	ProviderEffectUnknown    ProviderReconciliationDisposition = "unknown"
)

func (disposition ProviderReconciliationDisposition) valid() bool {
	return disposition == ProviderEffectApplied || disposition == ProviderEffectNotApplied || disposition == ProviderEffectUnknown
}

// ProviderIntentQuery is keyed by the immutable provider idempotency marker;
// it cannot be redirected to a different action or repository.
type ProviderIntentQuery struct{ intent ProviderIntent }

func (query ProviderIntentQuery) Kind() ProviderIntentKind { return query.intent.kind }
func (query ProviderIntentQuery) Repository() RepositoryIdentity {
	return query.intent.scope.repository
}
func (query ProviderIntentQuery) Remote() string         { return query.intent.scope.remote }
func (query ProviderIntentQuery) IdempotencyKey() Digest { return query.intent.idempotencyKey }
func (query ProviderIntentQuery) IntentDigest() Digest   { return query.intent.digest }

type ProviderReconciliationObservation struct {
	disposition       ProviderReconciliationDisposition
	requestMarker     string
	remoteHead        GitObjectID
	pullRequestNumber uint64
	pullRequestHead   GitObjectID
	mergeCommit       GitObjectID
	finalBaseHead     GitObjectID
	digest            Digest
}

type ProviderReconciliationObservationOptions struct {
	Disposition       ProviderReconciliationDisposition
	RequestMarker     string
	RemoteHead        GitObjectID
	PullRequestNumber uint64
	PullRequestHead   GitObjectID
	MergeCommit       GitObjectID
	FinalBaseHead     GitObjectID
}

func NewProviderReconciliationObservation(
	options ProviderReconciliationObservationOptions,
) (ProviderReconciliationObservation, error) {
	if !options.Disposition.valid() {
		return ProviderReconciliationObservation{}, fmt.Errorf("provider reconciliation requires a supported disposition")
	}
	if err := validateProviderAdapterMarker(options.RequestMarker); err != nil {
		return ProviderReconciliationObservation{}, err
	}
	observation := ProviderReconciliationObservation{
		disposition: options.Disposition, requestMarker: options.RequestMarker,
		remoteHead: options.RemoteHead, pullRequestNumber: options.PullRequestNumber,
		pullRequestHead: options.PullRequestHead, mergeCommit: options.MergeCommit,
		finalBaseHead: options.FinalBaseHead,
	}
	if options.Disposition != ProviderEffectApplied &&
		(!options.RemoteHead.IsZero() || options.PullRequestNumber != 0 || !options.PullRequestHead.IsZero() ||
			!options.MergeCommit.IsZero() || !options.FinalBaseHead.IsZero()) {
		return ProviderReconciliationObservation{}, fmt.Errorf("non-applied reconciliation cannot carry provider effect identities")
	}
	type canonical struct {
		SchemaVersion     int                               `json:"schema_version"`
		Disposition       ProviderReconciliationDisposition `json:"disposition"`
		RequestMarker     string                            `json:"request_marker"`
		RemoteHead        string                            `json:"remote_head,omitempty"`
		PullRequestNumber uint64                            `json:"pull_request_number,omitempty"`
		PullRequestHead   string                            `json:"pull_request_head,omitempty"`
		MergeCommit       string                            `json:"merge_commit,omitempty"`
		FinalBaseHead     string                            `json:"final_base_head,omitempty"`
	}
	content, err := json.Marshal(canonical{
		SchemaVersion: JournalSchemaVersion, Disposition: options.Disposition,
		RequestMarker: options.RequestMarker, RemoteHead: options.RemoteHead.String(),
		PullRequestNumber: options.PullRequestNumber, PullRequestHead: options.PullRequestHead.String(),
		MergeCommit: options.MergeCommit.String(), FinalBaseHead: options.FinalBaseHead.String(),
	})
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	observation.digest = DigestBytes(content)
	return observation, nil
}

func (observation ProviderReconciliationObservation) Disposition() ProviderReconciliationDisposition {
	return observation.disposition
}
func (observation ProviderReconciliationObservation) RequestMarker() string {
	return observation.requestMarker
}
func (observation ProviderReconciliationObservation) Digest() Digest { return observation.digest }

type ProviderPullRequestQuery struct {
	repository  RepositoryIdentity
	pullRequest PullRequestIdentity
}

func NewProviderPullRequestQuery(
	repository RepositoryIdentity,
	pullRequest PullRequestIdentity,
) (ProviderPullRequestQuery, error) {
	if repository.String() == "" || pullRequest.IsZero() || pullRequest.repository != repository {
		return ProviderPullRequestQuery{}, fmt.Errorf("provider pull request query requires matching repository and provider-derived identity")
	}
	return ProviderPullRequestQuery{repository: repository, pullRequest: pullRequest}, nil
}

func (query ProviderPullRequestQuery) Repository() RepositoryIdentity   { return query.repository }
func (query ProviderPullRequestQuery) PullRequest() PullRequestIdentity { return query.pullRequest }

// providerAdapterPort is intentionally unexported. Implementations live in a
// credential-bearing composition root and are reachable by the workspace
// engine only through ProviderBroker.
type providerAdapterPort interface {
	Push(context.Context, ProviderPushRequest) (ProviderPushAdapterResult, error)
	OpenPullRequest(context.Context, ProviderOpenPullRequestRequest) (ProviderOpenPullRequestAdapterResult, error)
	Merge(context.Context, ProviderMergeRequest) (ProviderMergeAdapterResult, error)
	QueryIntent(context.Context, ProviderIntentQuery) (ProviderReconciliationObservation, error)
	QueryPullRequest(context.Context, ProviderPullRequestQuery) (ProviderPullRequestState, error)
}

type providerBrokerCapability struct {
	intentID       ID
	intentDigest   Digest
	idempotencyKey Digest
	epoch          uint64
	dispatchRecord Digest
	digest         Digest
}

func newProviderBrokerCapability(intent ProviderIntent, epoch uint64, dispatchRecord Digest) (providerBrokerCapability, error) {
	if intent.intentID.IsZero() || intent.digest.IsZero() || intent.idempotencyKey.IsZero() || epoch == 0 || dispatchRecord.IsZero() {
		return providerBrokerCapability{}, fmt.Errorf("provider broker capability requires intent, authorization epoch, and durable dispatch record")
	}
	capability := providerBrokerCapability{
		intentID: intent.intentID, intentDigest: intent.digest, idempotencyKey: intent.idempotencyKey,
		epoch: epoch, dispatchRecord: dispatchRecord,
	}
	type canonical struct {
		SchemaVersion  int    `json:"schema_version"`
		IntentID       string `json:"intent_id"`
		IntentDigest   string `json:"intent_digest"`
		IdempotencyKey string `json:"idempotency_key"`
		Epoch          uint64 `json:"epoch"`
		DispatchRecord string `json:"dispatch_record"`
	}
	content, _ := json.Marshal(canonical{
		SchemaVersion: JournalSchemaVersion, IntentID: intent.intentID.String(),
		IntentDigest: intent.digest.String(), IdempotencyKey: intent.idempotencyKey.String(),
		Epoch: epoch, DispatchRecord: dispatchRecord.String(),
	})
	capability.digest = DigestBytes(content)
	return capability, nil
}

type providerQueryCapability struct {
	intentID       ID
	intentDigest   Digest
	idempotencyKey Digest
	journalHead    Digest
	queryAttempt   time.Time
	digest         Digest
}

func newProviderQueryCapability(
	intent ProviderIntent,
	journalHead Digest,
	queryAttempt time.Time,
) (providerQueryCapability, error) {
	if intent.intentID.IsZero() || intent.digest.IsZero() || intent.idempotencyKey.IsZero() ||
		journalHead.IsZero() || queryAttempt.IsZero() {
		return providerQueryCapability{}, fmt.Errorf("provider query capability requires intent, durable journal head, and query attempt")
	}
	queryAttempt = queryAttempt.UTC()
	capability := providerQueryCapability{
		intentID: intent.intentID, intentDigest: intent.digest,
		idempotencyKey: intent.idempotencyKey, journalHead: journalHead, queryAttempt: queryAttempt,
	}
	type canonical struct {
		SchemaVersion  int    `json:"schema_version"`
		IntentID       string `json:"intent_id"`
		IntentDigest   string `json:"intent_digest"`
		IdempotencyKey string `json:"idempotency_key"`
		JournalHead    string `json:"journal_head"`
		QueryAttempt   string `json:"query_attempt"`
	}
	content, _ := json.Marshal(canonical{
		SchemaVersion: JournalSchemaVersion, IntentID: intent.intentID.String(),
		IntentDigest: intent.digest.String(), IdempotencyKey: intent.idempotencyKey.String(),
		JournalHead: journalHead.String(), QueryAttempt: queryAttempt.Format(time.RFC3339Nano),
	})
	capability.digest = DigestBytes(content)
	return capability, nil
}

// ProviderBroker is the sole credential-bearing provider boundary. It owns
// the adapter and consumes every opaque dispatch/query capability exactly
// once before invoking that adapter.
type ProviderBroker struct {
	provider ProviderIdentity
	adapter  providerAdapterPort
	mu       sync.Mutex
	consumed map[Digest]struct{}
}

func NewProviderBroker(provider ProviderIdentity, adapter providerAdapterPort) (*ProviderBroker, error) {
	if provider.kind.IsZero() || provider.repository == "" || adapter == nil {
		return nil, fmt.Errorf("provider broker requires provider identity and typed adapter")
	}
	return &ProviderBroker{provider: provider, adapter: adapter, consumed: make(map[Digest]struct{})}, nil
}

func (broker *ProviderBroker) consume(capability Digest) error {
	if broker == nil || broker.adapter == nil || capability.IsZero() {
		return fmt.Errorf("provider broker capability is unavailable")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if _, exists := broker.consumed[capability]; exists {
		return fmt.Errorf("provider broker capability %s was already consumed", capability)
	}
	broker.consumed[capability] = struct{}{}
	return nil
}

func (broker *ProviderBroker) dispatch(
	ctx context.Context,
	capability providerBrokerCapability,
	intent ProviderIntent,
	preflight ProviderMergePreflight,
) (ProviderResult, error) {
	if capability.intentID != intent.intentID || capability.intentDigest != intent.digest ||
		capability.idempotencyKey != intent.idempotencyKey || capability.epoch != intent.scope.epoch ||
		capability.dispatchRecord.IsZero() || capability.digest.IsZero() {
		return ProviderResult{}, fmt.Errorf("provider broker capability does not match the exact intent and authorization epoch")
	}
	expected, err := newProviderBrokerCapability(intent, capability.epoch, capability.dispatchRecord)
	if err != nil || expected.digest != capability.digest {
		return ProviderResult{}, fmt.Errorf("provider broker capability digest is invalid")
	}
	if err := broker.consume(capability.digest); err != nil {
		return ProviderResult{}, err
	}
	if intent.kind == ProviderIntentMerge {
		if preflight.intentID != intent.intentID || preflight.intentDigest != intent.digest || preflight.digest.IsZero() {
			return broker.failureResult(intent, ProviderAdapterFailedBeforeEffect, "merge-preflight-missing", fmt.Errorf("merge dispatch lacks durable provider preflight evidence"))
		}
	} else if !preflight.digest.IsZero() {
		return ProviderResult{}, fmt.Errorf("non-merge provider dispatch cannot carry merge preflight evidence")
	}
	if err := ctx.Err(); err != nil {
		return broker.failureResult(intent, ProviderAdapterFailedBeforeEffect, "context-cancelled-before-provider", err)
	}
	switch intent.kind {
	case ProviderIntentPush:
		result, callErr := broker.adapter.Push(ctx, ProviderPushRequest{intent: intent})
		if callErr != nil {
			return broker.adapterFailureResult(intent, callErr)
		}
		if result.remoteHead != intent.head {
			return broker.failureResult(intent, ProviderAdapterAmbiguous, "push-result-head-mismatch", fmt.Errorf("provider push returned unexpected remote head"))
		}
		if !intent.pullRequest.IsZero() {
			observed, observeErr := broker.adapter.QueryPullRequest(ctx, ProviderPullRequestQuery{
				repository: intent.scope.repository, pullRequest: intent.pullRequest,
			})
			if observeErr != nil || observed.pullRequest != intent.pullRequest || observed.branch != intent.branch ||
				observed.head != intent.head || observed.headTree != intent.tree || observed.remoteBranchHead != intent.head {
				if observeErr == nil {
					observeErr = fmt.Errorf("provider pull request did not advance to pushed head and tree")
				}
				return broker.failureResult(intent, ProviderAdapterAmbiguous, "push-postflight-pr-drift", observeErr)
			}
		}
		return newProviderResult(ProviderResult{
			intentID: intent.intentID, intentDigest: intent.digest, kind: intent.kind,
			status: ProviderIntentSucceeded, idempotencyKey: intent.idempotencyKey,
			provider: broker.provider.kind, requestMarker: result.requestMarker, remoteHead: result.remoteHead,
		})
	case ProviderIntentOpenPullRequest:
		result, callErr := broker.adapter.OpenPullRequest(ctx, ProviderOpenPullRequestRequest{intent: intent})
		if callErr != nil {
			return broker.adapterFailureResult(intent, callErr)
		}
		if result.head != intent.head {
			return broker.failureResult(intent, ProviderAdapterAmbiguous, "open-pr-result-head-mismatch", fmt.Errorf("provider pull request returned unexpected head"))
		}
		identity, identityErr := newPullRequestIdentity(broker.provider.kind, intent.scope.repository, result.number)
		if identityErr != nil {
			return ProviderResult{}, identityErr
		}
		return newProviderResult(ProviderResult{
			intentID: intent.intentID, intentDigest: intent.digest, kind: intent.kind,
			status: ProviderIntentSucceeded, idempotencyKey: intent.idempotencyKey,
			provider: broker.provider.kind, requestMarker: result.requestMarker,
			pullRequest: identity, pullRequestHead: result.head,
		})
	case ProviderIntentMerge:
		observed, observeErr := broker.adapter.QueryPullRequest(ctx, ProviderPullRequestQuery{
			repository: intent.scope.repository, pullRequest: intent.pullRequest,
		})
		if observeErr != nil {
			return broker.failureResult(intent, ProviderAdapterFailedBeforeEffect, "merge-preflight-query-failed", observeErr)
		}
		if observed.merged || !providerRequiredEvidenceReady(observed) ||
			validateProviderStateAgainstMergePreflight(preflight, observed) != nil {
			return broker.failureResult(intent, ProviderAdapterFailedBeforeEffect, "merge-preflight-drift", fmt.Errorf("pull request head, tree, identity, or merge state drifted"))
		}
		result, callErr := broker.adapter.Merge(ctx, ProviderMergeRequest{intent: intent})
		if callErr != nil {
			return broker.adapterFailureResult(intent, callErr)
		}
		if result.mergeCommit != result.finalBaseHead {
			return broker.failureResult(intent, ProviderAdapterAmbiguous, "merge-result-base-mismatch", fmt.Errorf("provider merge did not return the merge commit as final base head"))
		}
		return newProviderResult(ProviderResult{
			intentID: intent.intentID, intentDigest: intent.digest, kind: intent.kind,
			status: ProviderIntentSucceeded, idempotencyKey: intent.idempotencyKey,
			provider: broker.provider.kind, requestMarker: result.requestMarker,
			mergeCommit: result.mergeCommit, finalBaseHead: result.finalBaseHead,
		})
	default:
		return ProviderResult{}, fmt.Errorf("unsupported provider intent kind %q", intent.kind)
	}
}

func providerRequiredEvidenceReady(state ProviderPullRequestState) bool {
	for _, check := range state.checks {
		if check.required && check.conclusion != ProviderCheckPassed {
			return false
		}
	}
	for _, review := range state.reviews {
		if review.required && review.conclusion != ProviderReviewApproved {
			return false
		}
	}
	return true
}

func (broker *ProviderBroker) adapterFailureResult(intent ProviderIntent, callErr error) (ProviderResult, error) {
	var failure ProviderAdapterFailure
	if errors.As(callErr, &failure) {
		return broker.failureResult(intent, failure.kind, failure.marker, callErr)
	}
	marker := "unclassified-provider-error-" + DigestBytes([]byte(callErr.Error())).String()
	return broker.failureResult(intent, ProviderAdapterAmbiguous, marker, callErr)
}

func (broker *ProviderBroker) failureResult(
	intent ProviderIntent,
	kind ProviderAdapterFailureKind,
	marker string,
	cause error,
) (ProviderResult, error) {
	status := ProviderIntentAmbiguous
	switch kind {
	case ProviderAdapterFailedBeforeEffect:
		status = ProviderIntentFailedBeforeEffect
	case ProviderAdapterFailedAfterEffect:
		status = ProviderIntentFailedAfterEffect
	case ProviderAdapterAmbiguous:
		status = ProviderIntentAmbiguous
	}
	result, err := newProviderResult(ProviderResult{
		intentID: intent.intentID, intentDigest: intent.digest, kind: intent.kind,
		status: status, idempotencyKey: intent.idempotencyKey,
		provider: broker.provider.kind, requestMarker: marker,
	})
	if err != nil {
		return ProviderResult{}, err
	}
	return result, cause
}

func (broker *ProviderBroker) reconcile(
	ctx context.Context,
	capability providerQueryCapability,
	intent ProviderIntent,
) (ProviderReconciliationObservation, error) {
	if capability.intentID != intent.intentID || capability.intentDigest != intent.digest ||
		capability.idempotencyKey != intent.idempotencyKey || capability.journalHead.IsZero() ||
		capability.queryAttempt.IsZero() || capability.digest.IsZero() {
		return ProviderReconciliationObservation{}, fmt.Errorf("provider reconciliation capability does not match the exact intent")
	}
	expected, err := newProviderQueryCapability(intent, capability.journalHead, capability.queryAttempt)
	if err != nil || expected.digest != capability.digest {
		return ProviderReconciliationObservation{}, fmt.Errorf("provider reconciliation capability digest is invalid")
	}
	if err := broker.consume(capability.digest); err != nil {
		return ProviderReconciliationObservation{}, err
	}
	observation, err := broker.adapter.QueryIntent(ctx, ProviderIntentQuery{intent: intent})
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	if observation.digest.IsZero() || !observation.disposition.valid() {
		return ProviderReconciliationObservation{}, fmt.Errorf("provider reconciliation returned an invalid observation")
	}
	return observation, nil
}

func (broker *ProviderBroker) observePullRequest(
	ctx context.Context,
	query ProviderPullRequestQuery,
) (ProviderPullRequestState, error) {
	if broker == nil || broker.adapter == nil || query.repository.String() == "" || query.pullRequest.IsZero() ||
		query.pullRequest.repository != query.repository || query.pullRequest.provider != broker.provider.kind {
		return ProviderPullRequestState{}, fmt.Errorf("provider broker pull request query has invalid identity")
	}
	state, err := broker.adapter.QueryPullRequest(ctx, query)
	if err != nil {
		return ProviderPullRequestState{}, err
	}
	if err := state.validate(); err != nil {
		return ProviderPullRequestState{}, err
	}
	if state.repository != query.repository || state.pullRequest != query.pullRequest {
		return ProviderPullRequestState{}, fmt.Errorf("provider pull request query returned a different identity")
	}
	return state, nil
}

func (broker *ProviderBroker) VerifyProviderPullRequest(
	ctx context.Context,
	verification ProviderPullRequestVerification,
	observation ProviderPullRequestObservation,
) error {
	if broker == nil || observation.digest.IsZero() || observation.identity.provider != broker.provider.kind ||
		verification.repository != observation.identity.repository || verification.observedHead != observation.head ||
		verification.observation != observation.digest {
		return fmt.Errorf("provider pull request verification does not match broker and observation bindings")
	}
	state, err := broker.observePullRequest(ctx, ProviderPullRequestQuery{
		repository: observation.identity.repository, pullRequest: observation.identity,
	})
	if err != nil {
		return err
	}
	if state.head != observation.head {
		return fmt.Errorf("provider pull request head drifted from the recorded observation")
	}
	return nil
}

package workspace

import (
	"encoding/json"
	"fmt"
)

type providerIntentWire struct {
	providerIntentCanonical
	IdempotencyKey string `json:"idempotency_key"`
	Digest         string `json:"digest"`
}

type providerIntentReservedPayloadWire struct {
	WorkspaceID string                      `json:"workspace_id"`
	Generation  string                      `json:"generation"`
	Intent      providerIntentWire          `json:"intent"`
	Planning    authorizationCapabilityWire `json:"planning"`
	Reservation authorizationCapabilityWire `json:"reservation"`
}

type providerIntentAbandonedPayloadWire struct {
	WorkspaceID  string `json:"workspace_id"`
	Generation   string `json:"generation"`
	IntentID     string `json:"intent_id"`
	IntentDigest string `json:"intent_digest"`
}

type providerMergePreflightWire struct {
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
	Digest          string                      `json:"digest"`
}

type providerMergePreflightPayloadWire struct {
	WorkspaceID string                     `json:"workspace_id"`
	Generation  string                     `json:"generation"`
	Preflight   providerMergePreflightWire `json:"preflight"`
}

type providerIntentDispatchedPayloadWire struct {
	WorkspaceID    string                      `json:"workspace_id"`
	Generation     string                      `json:"generation"`
	IntentID       string                      `json:"intent_id"`
	IntentDigest   string                      `json:"intent_digest"`
	Planning       authorizationCapabilityWire `json:"planning"`
	Reservation    authorizationCapabilityWire `json:"reservation"`
	Queue          authorizationCapabilityWire `json:"queue"`
	BeforeDispatch authorizationCapabilityWire `json:"before_dispatch"`
}

type providerResultWire struct {
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
	Digest          string                       `json:"digest"`
}

type providerResultPayloadWire struct {
	WorkspaceID          string             `json:"workspace_id"`
	Generation           string             `json:"generation"`
	AuthorizationRequest string             `json:"authorization_request_digest"`
	DispatchEpoch        uint64             `json:"dispatch_epoch"`
	Result               providerResultWire `json:"result"`
}

type providerReconciliationObservationWire struct {
	Disposition       ProviderReconciliationDisposition `json:"disposition"`
	RequestMarker     string                            `json:"request_marker"`
	RemoteHead        string                            `json:"remote_head,omitempty"`
	PullRequestNumber uint64                            `json:"pull_request_number,omitempty"`
	PullRequestHead   string                            `json:"pull_request_head,omitempty"`
	MergeCommit       string                            `json:"merge_commit,omitempty"`
	FinalBaseHead     string                            `json:"final_base_head,omitempty"`
	Digest            string                            `json:"digest"`
}

type providerReconciliationWire struct {
	IntentID        string                                `json:"intent_id"`
	IntentDigest    string                                `json:"intent_digest"`
	PriorStatus     ProviderIntentStatus                  `json:"prior_status"`
	IdempotencyKey  string                                `json:"idempotency_key"`
	Provider        string                                `json:"provider"`
	Observation     providerReconciliationObservationWire `json:"observation"`
	EffectApplied   bool                                  `json:"effect_applied"`
	RemoteHead      string                                `json:"remote_head,omitempty"`
	PullRequest     *controlPlanePullRequestWire          `json:"pull_request,omitempty"`
	PullRequestHead string                                `json:"pull_request_head,omitempty"`
	MergeCommit     string                                `json:"merge_commit,omitempty"`
	FinalBaseHead   string                                `json:"final_base_head,omitempty"`
	Digest          string                                `json:"digest"`
}

type providerReconciledPayloadWire struct {
	WorkspaceID          string                     `json:"workspace_id"`
	Generation           string                     `json:"generation"`
	AuthorizationRequest string                     `json:"authorization_request_digest"`
	DispatchEpoch        uint64                     `json:"dispatch_epoch"`
	Reconciliation       providerReconciliationWire `json:"reconciliation"`
}

type providerCompletionPayloadWire struct {
	WorkspaceID string                        `json:"workspace_id"`
	Generation  string                        `json:"generation"`
	Receipt     providerCompletionReceiptWire `json:"receipt"`
}

func marshalProviderJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case ProviderIntentReservedJournalEvent:
		value = providerIntentReservedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Intent: providerIntentToWire(event.intent), Planning: authorizationCapabilityToWire(event.planning),
			Reservation: authorizationCapabilityToWire(event.reservation),
		}
	case ProviderIntentAbandonedJournalEvent:
		value = providerIntentAbandonedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			IntentID: event.intentID.String(), IntentDigest: event.intentDigest.String(),
		}
	case ProviderMergePreflightRecordedJournalEvent:
		value = providerMergePreflightPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Preflight: providerMergePreflightToWire(event.preflight),
		}
	case ProviderIntentDispatchedJournalEvent:
		value = providerIntentDispatchedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			IntentID: event.intentID.String(), IntentDigest: event.intentDigest.String(),
			Planning:       authorizationCapabilityToWire(event.planning),
			Reservation:    authorizationCapabilityToWire(event.reservation),
			Queue:          authorizationCapabilityToWire(event.queue),
			BeforeDispatch: authorizationCapabilityToWire(event.effect.capability),
		}
	case ProviderResultRecordedJournalEvent:
		value = providerResultPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AuthorizationRequest: event.authorizationRequest.String(),
			DispatchEpoch:        event.dispatchEpoch, Result: providerResultToWire(event.result),
		}
	case ProviderIntentReconciledJournalEvent:
		value = providerReconciledPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AuthorizationRequest: event.authorizationRequest.String(),
			DispatchEpoch:        event.dispatchEpoch, Reconciliation: providerReconciliationToWire(event.reconciliation),
		}
	case ProviderCompletionVerifiedJournalEvent:
		value = providerCompletionPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Receipt: providerCompletionReceiptToWire(event.receipt),
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeProviderJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventProviderIntentReserved:
		var wire providerIntentReservedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider intent reservation: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		intent, err := providerIntentFromWire(wire.Intent)
		if err != nil {
			return nil, true, err
		}
		planning, err := authorizationCapabilityFromWire(wire.Planning)
		if err != nil {
			return nil, true, err
		}
		reservation, err := authorizationCapabilityFromWire(wire.Reservation)
		if err != nil {
			return nil, true, err
		}
		event, err := NewProviderIntentReservedJournalEvent(workspaceID, generation, intent, planning, reservation)
		return event, true, err
	case JournalEventProviderIntentAbandoned:
		var wire providerIntentAbandonedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider intent abandonment: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		intentID, err := NewID(wire.IntentID)
		if err != nil {
			return nil, true, err
		}
		intentDigest, err := ParseDigest(wire.IntentDigest)
		if err != nil {
			return nil, true, err
		}
		event := ProviderIntentAbandonedJournalEvent{
			workspaceID: workspaceID, generation: generation,
			intentID: intentID, intentDigest: intentDigest,
		}
		return event, true, event.validate()
	case JournalEventProviderMergePreflightRecorded:
		var wire providerMergePreflightPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider merge preflight: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		preflight, err := providerMergePreflightFromWire(wire.Preflight)
		if err != nil {
			return nil, true, err
		}
		event, err := NewProviderMergePreflightRecordedJournalEvent(workspaceID, generation, preflight)
		return event, true, err
	case JournalEventProviderIntentDispatched:
		var wire providerIntentDispatchedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider intent dispatch: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		intentID, err := NewID(wire.IntentID)
		if err != nil {
			return nil, true, err
		}
		intentDigest, err := ParseDigest(wire.IntentDigest)
		if err != nil {
			return nil, true, err
		}
		planning, err := authorizationCapabilityFromWire(wire.Planning)
		if err != nil {
			return nil, true, err
		}
		reservation, err := authorizationCapabilityFromWire(wire.Reservation)
		if err != nil {
			return nil, true, err
		}
		queue, err := authorizationCapabilityFromWire(wire.Queue)
		if err != nil {
			return nil, true, err
		}
		before, err := authorizationCapabilityFromWire(wire.BeforeDispatch)
		if err != nil {
			return nil, true, err
		}
		effect, err := NewAuthorizationEffectDispatched(intentID, before)
		if err != nil {
			return nil, true, err
		}
		event := ProviderIntentDispatchedJournalEvent{
			workspaceID: workspaceID, generation: generation, intentID: intentID, intentDigest: intentDigest,
			planning: planning, reservation: reservation, queue: queue, effect: effect,
		}
		return event, true, event.validate()
	case JournalEventProviderResultRecorded:
		var wire providerResultPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider result: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		result, err := providerResultFromWire(wire.Result)
		if err != nil {
			return nil, true, err
		}
		authorizationRequest, err := ParseDigest(wire.AuthorizationRequest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewProviderResultRecordedJournalEvent(
			workspaceID, generation, result, authorizationRequest, wire.DispatchEpoch,
		)
		return event, true, err
	case JournalEventProviderIntentReconciled:
		var wire providerReconciledPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider reconciliation: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		reconciliation, err := providerReconciliationFromWire(wire.Reconciliation)
		if err != nil {
			return nil, true, err
		}
		authorizationRequest, err := ParseDigest(wire.AuthorizationRequest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewProviderIntentReconciledJournalEvent(
			workspaceID, generation, reconciliation, authorizationRequest, wire.DispatchEpoch,
		)
		return event, true, err
	case JournalEventProviderCompletionVerified:
		var wire providerCompletionPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode provider completion: %w", err)
		}
		workspaceID, generation, err := parseProviderEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		receipt, err := providerCompletionReceiptFromWire(wire.Receipt)
		if err != nil {
			return nil, true, err
		}
		event, err := NewProviderCompletionVerifiedJournalEvent(workspaceID, generation, receipt)
		return event, true, err
	default:
		return nil, false, nil
	}
}

func parseProviderEnvelope(workspace, generation string) (ID, Digest, error) {
	workspaceID, err := NewID(workspace)
	if err != nil {
		return ID{}, Digest{}, err
	}
	generationDigest, err := ParseDigest(generation)
	if err != nil {
		return ID{}, Digest{}, err
	}
	return workspaceID, generationDigest, nil
}

func providerIntentToWire(intent ProviderIntent) providerIntentWire {
	canonical := providerIntentCanonical{
		SchemaVersion: JournalSchemaVersion, IntentID: intent.intentID.String(), Kind: intent.kind,
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
	if !intent.pullRequest.IsZero() {
		canonical.PullRequest = &controlPlanePullRequestWire{
			Provider: intent.pullRequest.provider.String(), Repository: intent.pullRequest.repository.String(),
			Number: intent.pullRequest.number,
		}
	}
	return providerIntentWire{
		providerIntentCanonical: canonical, IdempotencyKey: intent.idempotencyKey.String(), Digest: intent.digest.String(),
	}
}

func providerIntentFromWire(wire providerIntentWire) (ProviderIntent, error) {
	if wire.SchemaVersion != JournalSchemaVersion {
		return ProviderIntent{}, fmt.Errorf("provider intent schema_version %d is not supported", wire.SchemaVersion)
	}
	workspaceID, err := NewID(wire.WorkspaceID)
	if err != nil {
		return ProviderIntent{}, err
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return ProviderIntent{}, err
	}
	attemptID, err := NewID(wire.AttemptID)
	if err != nil {
		return ProviderIntent{}, err
	}
	planID, err := NewID(wire.PlanID)
	if err != nil {
		return ProviderIntent{}, err
	}
	unitID, err := NewID(wire.MergeUnitID)
	if err != nil {
		return ProviderIntent{}, err
	}
	mergeUnit, _ := NewMergeUnitReference(planID, unitID)
	repository, err := NewRepositoryIdentity(wire.Repository)
	if err != nil {
		return ProviderIntent{}, err
	}
	segment, err := NewID(wire.SerialSegment)
	if err != nil {
		return ProviderIntent{}, err
	}
	base, err := ParseGitObjectID(wire.Base)
	if err != nil {
		return ProviderIntent{}, err
	}
	authorizedHead, err := ParseGitObjectID(wire.AuthorizedHead)
	if err != nil {
		return ProviderIntent{}, err
	}
	frontier, err := NewAuthorizationFrontier(base, authorizedHead)
	if err != nil {
		return ProviderIntent{}, err
	}
	var pullRequest PullRequestIdentity
	if wire.PullRequest != nil {
		provider, parseErr := NewID(wire.PullRequest.Provider)
		if parseErr != nil {
			return ProviderIntent{}, parseErr
		}
		prRepository, parseErr := NewRepositoryIdentity(wire.PullRequest.Repository)
		if parseErr != nil {
			return ProviderIntent{}, parseErr
		}
		pullRequest, parseErr = newPullRequestIdentity(provider, prRepository, wire.PullRequest.Number)
		if parseErr != nil {
			return ProviderIntent{}, parseErr
		}
	}
	scope := ProviderIntentScopeOptions{
		WorkspaceID: workspaceID, Generation: generation, AttemptID: attemptID, MergeUnit: mergeUnit,
		Repository: repository, Remote: wire.Remote, SerialSegment: segment, Frontier: frontier,
		PullRequest: pullRequest, Epoch: wire.AuthorizationEpoch,
	}
	head, err := ParseGitObjectID(wire.Head)
	if err != nil {
		return ProviderIntent{}, err
	}
	parseOptionalObject := func(value string) (GitObjectID, error) {
		if value == "" {
			return GitObjectID{}, nil
		}
		return ParseGitObjectID(value)
	}
	expectedRemote, err := parseOptionalObject(wire.ExpectedRemoteHead)
	if err != nil {
		return ProviderIntent{}, err
	}
	tree, err := parseOptionalObject(wire.Tree)
	if err != nil {
		return ProviderIntent{}, err
	}
	var intent ProviderIntent
	switch wire.Kind {
	case ProviderIntentPush:
		intent, err = NewProviderPushIntent(ProviderPushIntentOptions{
			Scope: scope, Branch: wire.Branch, ExpectedRemoteHead: expectedRemote, Head: head, Tree: tree,
		})
	case ProviderIntentOpenPullRequest:
		intent, err = NewProviderOpenPullRequestIntent(ProviderOpenPullRequestIntentOptions{
			Scope: scope, Branch: wire.Branch, BaseRef: wire.BaseRef, Head: head, Tree: tree,
			Title: wire.Title, Body: wire.Body,
		})
	case ProviderIntentMerge:
		intent, err = NewProviderMergeIntent(ProviderMergeIntentOptions{
			Scope: scope, Branch: wire.Branch, BaseRef: wire.BaseRef,
			Head: head, Tree: tree, Strategy: wire.MergeStrategy,
		})
	default:
		return ProviderIntent{}, fmt.Errorf("unsupported provider intent kind %q", wire.Kind)
	}
	if err != nil {
		return ProviderIntent{}, err
	}
	wireID, err := NewID(wire.IntentID)
	if err != nil {
		return ProviderIntent{}, err
	}
	wireKey, err := ParseDigest(wire.IdempotencyKey)
	if err != nil {
		return ProviderIntent{}, err
	}
	wireDigest, err := ParseDigest(wire.Digest)
	if err != nil {
		return ProviderIntent{}, err
	}
	authorizationDigest, err := ParseDigest(wire.Authorization)
	if err != nil {
		return ProviderIntent{}, err
	}
	if intent.intentID != wireID || intent.idempotencyKey != wireKey || intent.digest != wireDigest ||
		intent.authorization.digest != authorizationDigest {
		return ProviderIntent{}, fmt.Errorf("provider intent canonical identity mismatch")
	}
	return intent, nil
}

func providerMergePreflightToWire(preflight ProviderMergePreflight) providerMergePreflightWire {
	wire := providerMergePreflightWire{
		IntentID: preflight.intentID.String(), IntentDigest: preflight.intentDigest.String(),
		Repository: preflight.repository.String(), PullRequest: controlPlanePullRequestWire{
			Provider: preflight.pullRequest.provider.String(), Repository: preflight.pullRequest.repository.String(),
			Number: preflight.pullRequest.number,
		},
		BaseRef: preflight.baseRef, Branch: preflight.branch, BaseHead: preflight.baseHead.String(),
		Head: preflight.head.String(), HeadTree: preflight.headTree.String(), RemoteHead: preflight.remoteHead.String(),
		RequiredChecks:  make([]providerCheckStateWire, 0, len(preflight.requiredChecks)),
		RequiredReviews: make([]providerReviewStateWire, 0, len(preflight.requiredReviews)),
		Observation:     preflight.observation.String(), Digest: preflight.digest.String(),
	}
	for _, check := range preflight.requiredChecks {
		wire.RequiredChecks = append(wire.RequiredChecks, providerCheckStateWire{
			ID: check.id.String(), Required: check.required, Conclusion: check.conclusion, Evidence: check.evidence.String(),
		})
	}
	for _, review := range preflight.requiredReviews {
		wire.RequiredReviews = append(wire.RequiredReviews, providerReviewStateWire{
			ID: review.id.String(), Required: review.required, Conclusion: review.conclusion, Evidence: review.evidence.String(),
		})
	}
	return wire
}

func providerMergePreflightFromWire(wire providerMergePreflightWire) (ProviderMergePreflight, error) {
	intentID, err := NewID(wire.IntentID)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	intentDigest, err := ParseDigest(wire.IntentDigest)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	repository, err := NewRepositoryIdentity(wire.Repository)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	provider, err := NewID(wire.PullRequest.Provider)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	pullRepository, err := NewRepositoryIdentity(wire.PullRequest.Repository)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	pullRequest, err := newPullRequestIdentity(provider, pullRepository, wire.PullRequest.Number)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	baseHead, err := ParseGitObjectID(wire.BaseHead)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	head, err := ParseGitObjectID(wire.Head)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	headTree, err := ParseGitObjectID(wire.HeadTree)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	remoteHead, err := ParseGitObjectID(wire.RemoteHead)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	observation, err := ParseDigest(wire.Observation)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	checks := make([]ProviderCheckState, 0, len(wire.RequiredChecks))
	for _, value := range wire.RequiredChecks {
		id, parseErr := NewID(value.ID)
		if parseErr != nil {
			return ProviderMergePreflight{}, parseErr
		}
		evidence, parseErr := ParseDigest(value.Evidence)
		if parseErr != nil {
			return ProviderMergePreflight{}, parseErr
		}
		check, parseErr := NewProviderCheckState(id, value.Required, value.Conclusion, evidence)
		if parseErr != nil {
			return ProviderMergePreflight{}, parseErr
		}
		checks = append(checks, check)
	}
	if err := normalizeProviderChecks(&checks); err != nil {
		return ProviderMergePreflight{}, err
	}
	reviews := make([]ProviderReviewState, 0, len(wire.RequiredReviews))
	for _, value := range wire.RequiredReviews {
		id, parseErr := NewID(value.ID)
		if parseErr != nil {
			return ProviderMergePreflight{}, parseErr
		}
		evidence, parseErr := ParseDigest(value.Evidence)
		if parseErr != nil {
			return ProviderMergePreflight{}, parseErr
		}
		review, parseErr := NewProviderReviewState(id, value.Required, value.Conclusion, evidence)
		if parseErr != nil {
			return ProviderMergePreflight{}, parseErr
		}
		reviews = append(reviews, review)
	}
	if err := normalizeProviderReviews(&reviews); err != nil {
		return ProviderMergePreflight{}, err
	}
	preflight := ProviderMergePreflight{
		intentID: intentID, intentDigest: intentDigest, repository: repository,
		pullRequest: pullRequest, baseRef: wire.BaseRef, branch: wire.Branch,
		baseHead: baseHead, head: head, headTree: headTree, remoteHead: remoteHead,
		requiredChecks: checks, requiredReviews: reviews, observation: observation,
	}
	canonical, err := canonicalProviderMergePreflight(preflight)
	if err != nil {
		return ProviderMergePreflight{}, err
	}
	preflight.digest = DigestBytes(canonical)
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != preflight.digest {
		return ProviderMergePreflight{}, fmt.Errorf("provider merge preflight digest mismatch")
	}
	return preflight, nil
}

func providerResultToWire(result ProviderResult) providerResultWire {
	wire := providerResultWire{
		IntentID: result.intentID.String(), IntentDigest: result.intentDigest.String(), Kind: result.kind,
		Status: result.status, IdempotencyKey: result.idempotencyKey.String(), Provider: result.provider.String(),
		RequestMarker: result.requestMarker, RemoteHead: result.remoteHead.String(),
		PullRequestHead: result.pullRequestHead.String(), MergeCommit: result.mergeCommit.String(),
		FinalBaseHead: result.finalBaseHead.String(), Digest: result.digest.String(),
	}
	if !result.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: result.pullRequest.provider.String(), Repository: result.pullRequest.repository.String(),
			Number: result.pullRequest.number,
		}
	}
	return wire
}

func providerResultFromWire(wire providerResultWire) (ProviderResult, error) {
	intentID, err := NewID(wire.IntentID)
	if err != nil {
		return ProviderResult{}, err
	}
	intentDigest, err := ParseDigest(wire.IntentDigest)
	if err != nil {
		return ProviderResult{}, err
	}
	idempotency, err := ParseDigest(wire.IdempotencyKey)
	if err != nil {
		return ProviderResult{}, err
	}
	provider, err := NewID(wire.Provider)
	if err != nil {
		return ProviderResult{}, err
	}
	parseOptionalObject := func(value string) (GitObjectID, error) {
		if value == "" {
			return GitObjectID{}, nil
		}
		return ParseGitObjectID(value)
	}
	remoteHead, err := parseOptionalObject(wire.RemoteHead)
	if err != nil {
		return ProviderResult{}, err
	}
	pullRequestHead, err := parseOptionalObject(wire.PullRequestHead)
	if err != nil {
		return ProviderResult{}, err
	}
	mergeCommit, err := parseOptionalObject(wire.MergeCommit)
	if err != nil {
		return ProviderResult{}, err
	}
	finalBaseHead, err := parseOptionalObject(wire.FinalBaseHead)
	if err != nil {
		return ProviderResult{}, err
	}
	var pullRequest PullRequestIdentity
	if wire.PullRequest != nil {
		prProvider, parseErr := NewID(wire.PullRequest.Provider)
		if parseErr != nil {
			return ProviderResult{}, parseErr
		}
		repository, parseErr := NewRepositoryIdentity(wire.PullRequest.Repository)
		if parseErr != nil {
			return ProviderResult{}, parseErr
		}
		pullRequest, parseErr = newPullRequestIdentity(prProvider, repository, wire.PullRequest.Number)
		if parseErr != nil {
			return ProviderResult{}, parseErr
		}
	}
	result, err := newProviderResult(ProviderResult{
		intentID: intentID, intentDigest: intentDigest, kind: wire.Kind, status: wire.Status,
		idempotencyKey: idempotency, provider: provider, requestMarker: wire.RequestMarker,
		remoteHead: remoteHead, pullRequest: pullRequest, pullRequestHead: pullRequestHead,
		mergeCommit: mergeCommit, finalBaseHead: finalBaseHead,
	})
	if err != nil {
		return ProviderResult{}, err
	}
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != result.digest {
		return ProviderResult{}, fmt.Errorf("provider result digest mismatch")
	}
	return result, nil
}

func providerReconciliationToWire(result ProviderReconciliation) providerReconciliationWire {
	wire := providerReconciliationWire{
		IntentID: result.intentID.String(), IntentDigest: result.intentDigest.String(),
		PriorStatus: result.priorStatus, IdempotencyKey: result.idempotencyKey.String(),
		Provider: result.provider.String(), Observation: providerReconciliationObservationToWire(result.observation),
		EffectApplied: result.effectApplied, RemoteHead: result.remoteHead.String(),
		PullRequestHead: result.pullRequestHead.String(), MergeCommit: result.mergeCommit.String(),
		FinalBaseHead: result.finalBaseHead.String(), Digest: result.digest.String(),
	}
	if !result.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: result.pullRequest.provider.String(), Repository: result.pullRequest.repository.String(),
			Number: result.pullRequest.number,
		}
	}
	return wire
}

func providerReconciliationObservationToWire(observation ProviderReconciliationObservation) providerReconciliationObservationWire {
	return providerReconciliationObservationWire{
		Disposition: observation.disposition, RequestMarker: observation.requestMarker,
		RemoteHead: observation.remoteHead.String(), PullRequestNumber: observation.pullRequestNumber,
		PullRequestHead: observation.pullRequestHead.String(), MergeCommit: observation.mergeCommit.String(),
		FinalBaseHead: observation.finalBaseHead.String(), Digest: observation.digest.String(),
	}
}

func providerReconciliationObservationFromWire(
	wire providerReconciliationObservationWire,
) (ProviderReconciliationObservation, error) {
	parseOptionalObject := func(value string) (GitObjectID, error) {
		if value == "" {
			return GitObjectID{}, nil
		}
		return ParseGitObjectID(value)
	}
	remoteHead, err := parseOptionalObject(wire.RemoteHead)
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	pullRequestHead, err := parseOptionalObject(wire.PullRequestHead)
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	mergeCommit, err := parseOptionalObject(wire.MergeCommit)
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	finalBaseHead, err := parseOptionalObject(wire.FinalBaseHead)
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	observation, err := NewProviderReconciliationObservation(ProviderReconciliationObservationOptions{
		Disposition: wire.Disposition, RequestMarker: wire.RequestMarker, RemoteHead: remoteHead,
		PullRequestNumber: wire.PullRequestNumber, PullRequestHead: pullRequestHead,
		MergeCommit: mergeCommit, FinalBaseHead: finalBaseHead,
	})
	if err != nil {
		return ProviderReconciliationObservation{}, err
	}
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != observation.digest {
		return ProviderReconciliationObservation{}, fmt.Errorf("provider reconciliation observation digest mismatch")
	}
	return observation, nil
}

func providerReconciliationFromWire(wire providerReconciliationWire) (ProviderReconciliation, error) {
	intentID, err := NewID(wire.IntentID)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	intentDigest, err := ParseDigest(wire.IntentDigest)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	idempotency, err := ParseDigest(wire.IdempotencyKey)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	provider, err := NewID(wire.Provider)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	observation, err := providerReconciliationObservationFromWire(wire.Observation)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	parseOptionalObject := func(value string) (GitObjectID, error) {
		if value == "" {
			return GitObjectID{}, nil
		}
		return ParseGitObjectID(value)
	}
	remoteHead, err := parseOptionalObject(wire.RemoteHead)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	pullRequestHead, err := parseOptionalObject(wire.PullRequestHead)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	mergeCommit, err := parseOptionalObject(wire.MergeCommit)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	finalBaseHead, err := parseOptionalObject(wire.FinalBaseHead)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	var pullRequest PullRequestIdentity
	if wire.PullRequest != nil {
		prProvider, parseErr := NewID(wire.PullRequest.Provider)
		if parseErr != nil {
			return ProviderReconciliation{}, parseErr
		}
		repository, parseErr := NewRepositoryIdentity(wire.PullRequest.Repository)
		if parseErr != nil {
			return ProviderReconciliation{}, parseErr
		}
		pullRequest, parseErr = newPullRequestIdentity(prProvider, repository, wire.PullRequest.Number)
		if parseErr != nil {
			return ProviderReconciliation{}, parseErr
		}
	}
	result := ProviderReconciliation{
		intentID: intentID, intentDigest: intentDigest, priorStatus: wire.PriorStatus,
		idempotencyKey: idempotency, provider: provider, observation: observation,
		effectApplied: wire.EffectApplied, remoteHead: remoteHead, pullRequest: pullRequest,
		pullRequestHead: pullRequestHead, mergeCommit: mergeCommit, finalBaseHead: finalBaseHead,
	}
	canonical, err := canonicalProviderReconciliation(result)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	result.digest = DigestBytes(canonical)
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != result.digest {
		return ProviderReconciliation{}, fmt.Errorf("provider reconciliation digest mismatch")
	}
	return result, nil
}

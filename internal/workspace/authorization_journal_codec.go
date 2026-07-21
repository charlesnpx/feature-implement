package workspace

import (
	"encoding/json"
	"fmt"
	"time"
)

type standingGrantScopeWire struct {
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

type authorizationGrantPayloadWire struct {
	WorkspaceID   string                 `json:"workspace_id"`
	Generation    string                 `json:"generation"`
	Scope         standingGrantScopeWire `json:"scope"`
	GrantID       string                 `json:"grant_id"`
	ParentGrantID string                 `json:"parent_grant_id,omitempty"`
	RequestDigest string                 `json:"request_digest"`
	ReceiptDigest string                 `json:"receipt_digest"`
}

type authorizationRevocationPayloadWire struct {
	WorkspaceID   string `json:"workspace_id"`
	Generation    string `json:"generation"`
	Repository    string `json:"repository"`
	Remote        string `json:"remote"`
	TargetGrant   string `json:"target_grant,omitempty"`
	NextEpoch     uint64 `json:"next_epoch"`
	Reason        string `json:"reason"`
	RequestDigest string `json:"request_digest"`
	ReceiptDigest string `json:"receipt_digest"`
}

type authorizationSegmentPayloadWire struct {
	WorkspaceID string `json:"workspace_id"`
	Generation  string `json:"generation"`
	Segment     string `json:"segment"`
	Epoch       uint64 `json:"epoch"`
}

func marshalAuthorizationJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case AuthorizationGrantRecordedJournalEvent:
		value = authorizationGrantPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Scope: standingGrantScopeToWire(event.grant.scope), GrantID: event.grant.grantID.String(),
			ParentGrantID: event.grant.parentGrantID.String(),
			RequestDigest: event.grant.requestDigest.String(), ReceiptDigest: event.grant.receiptDigest.String(),
		}
	case AuthorizationRevokedJournalEvent:
		value = authorizationRevocationPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Repository: event.revocation.repository.String(), Remote: event.revocation.remote,
			TargetGrant: event.revocation.targetGrant.String(), NextEpoch: event.revocation.nextEpoch,
			Reason: event.revocation.reason.String(), RequestDigest: event.revocation.digest.String(),
			ReceiptDigest: event.revocation.receipt.String(),
		}
	case AuthorizationSegmentCompletedJournalEvent:
		value = authorizationSegmentPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Segment: event.segment.String(), Epoch: event.epoch,
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeAuthorizationJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventAuthorizationGrantRecorded:
		var wire authorizationGrantPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode authorization grant: %w", err)
		}
		workspaceID, generation, err := parseAuthorizationEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		scope, err := standingGrantScopeFromWire(wire.Scope)
		if err != nil {
			return nil, true, err
		}
		grantID, err := ParseDigest(wire.GrantID)
		if err != nil {
			return nil, true, err
		}
		var parentGrantID Digest
		if wire.ParentGrantID != "" {
			parentGrantID, err = ParseDigest(wire.ParentGrantID)
			if err != nil {
				return nil, true, err
			}
		}
		requestDigest, err := ParseDigest(wire.RequestDigest)
		if err != nil {
			return nil, true, err
		}
		receiptDigest, err := ParseDigest(wire.ReceiptDigest)
		if err != nil {
			return nil, true, err
		}
		grant := StandingGrant{
			scope: scope, grantID: grantID, parentGrantID: parentGrantID,
			requestDigest: requestDigest, receiptDigest: receiptDigest,
		}
		event, err := NewAuthorizationGrantRecordedJournalEvent(workspaceID, generation, grant)
		return event, true, err
	case JournalEventAuthorizationRevoked:
		var wire authorizationRevocationPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode authorization revocation: %w", err)
		}
		workspaceID, generation, err := parseAuthorizationEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		repository, err := NewRepositoryIdentity(wire.Repository)
		if err != nil {
			return nil, true, err
		}
		var target Digest
		if wire.TargetGrant != "" {
			target, err = ParseDigest(wire.TargetGrant)
			if err != nil {
				return nil, true, err
			}
		}
		reason, err := NewID(wire.Reason)
		if err != nil {
			return nil, true, err
		}
		revocation, err := newAuthorizationRevocation(AuthorizationRevocationOptions{
			WorkspaceID: workspaceID, Repository: repository, Remote: wire.Remote,
			Generation: generation, TargetGrant: target, NextEpoch: wire.NextEpoch, Reason: reason,
		})
		if err != nil {
			return nil, true, err
		}
		requestDigest, err := ParseDigest(wire.RequestDigest)
		if err != nil || requestDigest != revocation.digest {
			return nil, true, fmt.Errorf("authorization revocation request digest mismatch")
		}
		receiptDigest, err := ParseDigest(wire.ReceiptDigest)
		if err != nil {
			return nil, true, err
		}
		revocation.receipt = receiptDigest
		event, err := NewAuthorizationRevokedJournalEvent(workspaceID, generation, revocation)
		return event, true, err
	case JournalEventAuthorizationSegmentCompleted:
		var wire authorizationSegmentPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode authorization segment completion: %w", err)
		}
		workspaceID, generation, err := parseAuthorizationEnvelope(wire.WorkspaceID, wire.Generation)
		if err != nil {
			return nil, true, err
		}
		segment, err := NewID(wire.Segment)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAuthorizationSegmentCompletedJournalEvent(workspaceID, generation, segment, wire.Epoch)
		return event, true, err
	default:
		return nil, false, nil
	}
}

func standingGrantScopeToWire(scope StandingGrantScope) standingGrantScopeWire {
	wire := standingGrantScopeWire{
		WorkspaceID: scope.workspaceID.String(), Repository: scope.repository.String(), Remote: scope.remote,
		Generation: scope.generation.String(), SerialSegment: scope.serialSegment.String(),
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
	return wire
}

func standingGrantScopeFromWire(wire standingGrantScopeWire) (StandingGrantScope, error) {
	workspaceID, generation, err := parseAuthorizationEnvelope(wire.WorkspaceID, wire.Generation)
	if err != nil {
		return StandingGrantScope{}, err
	}
	repository, err := NewRepositoryIdentity(wire.Repository)
	if err != nil {
		return StandingGrantScope{}, err
	}
	segment, err := NewID(wire.SerialSegment)
	if err != nil {
		return StandingGrantScope{}, err
	}
	base, err := ParseGitObjectID(wire.Base)
	if err != nil {
		return StandingGrantScope{}, err
	}
	head, err := ParseGitObjectID(wire.Head)
	if err != nil {
		return StandingGrantScope{}, err
	}
	frontier, err := NewAuthorizationFrontier(base, head)
	if err != nil {
		return StandingGrantScope{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil || wire.ExpiresAt != expiresAt.UTC().Format(time.RFC3339Nano) {
		return StandingGrantScope{}, fmt.Errorf("standing grant expiry must be canonical UTC RFC3339Nano")
	}
	var pullRequest PullRequestIdentity
	if wire.PullRequest != nil {
		provider, err := NewID(wire.PullRequest.Provider)
		if err != nil {
			return StandingGrantScope{}, err
		}
		prRepository, err := NewRepositoryIdentity(wire.PullRequest.Repository)
		if err != nil {
			return StandingGrantScope{}, err
		}
		pullRequest, err = NewPullRequestIdentity(provider, prRepository, wire.PullRequest.Number)
		if err != nil {
			return StandingGrantScope{}, err
		}
	}
	return NewStandingGrantScope(StandingGrantScopeOptions{
		WorkspaceID: workspaceID, Repository: repository, Remote: wire.Remote, Generation: generation,
		SerialSegment: segment, Frontier: frontier, Actions: wire.Actions,
		ExpiresAt: expiresAt, Epoch: wire.Epoch, PullRequest: pullRequest,
	})
}

func parseAuthorizationEnvelope(workspaceText, generationText string) (ID, Digest, error) {
	workspaceID, err := NewID(workspaceText)
	if err != nil {
		return ID{}, Digest{}, err
	}
	generation, err := ParseDigest(generationText)
	if err != nil {
		return ID{}, Digest{}, err
	}
	return workspaceID, generation, nil
}

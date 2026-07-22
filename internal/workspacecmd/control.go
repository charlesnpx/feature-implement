package workspacecmd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type controlPlaneAuthorityDocument struct {
	SchemaVersion int                    `json:"schema_version"`
	AdapterID     string                 `json:"adapter_id"`
	Keys          []controlPlaneKeyInput `json:"keys"`
}

type controlPlaneKeyInput struct {
	ID        string   `json:"id"`
	PublicKey string   `json:"public_key"`
	Kinds     []string `json:"kinds"`
}

type staticTrustAnchors struct {
	values map[string]workspace.ControlPlaneTrustAnchor
}

func (anchors staticTrustAnchors) ResolveControlPlaneTrustAnchor(_ context.Context, id workspace.ID) (workspace.ControlPlaneTrustAnchor, error) {
	anchor, exists := anchors.values[id.String()]
	if !exists {
		return workspace.ControlPlaneTrustAnchor{}, fmt.Errorf("unknown control-plane key %s", id)
	}
	return anchor, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type durableReplayStore struct {
	directory string
}

func (store durableReplayStore) ClaimControlPlaneNonce(_ context.Context, claim workspace.ControlPlaneReplayClaim) error {
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		return err
	}
	type canonicalClaim struct {
		SchemaVersion int    `json:"schema_version"`
		KeyID         string `json:"key_id"`
		Nonce         string `json:"nonce"`
		PayloadDigest string `json:"payload_digest"`
		ReceiptDigest string `json:"receipt_digest"`
		ExpiresAt     string `json:"expires_at"`
	}
	content, err := json.Marshal(canonicalClaim{
		SchemaVersion: requestSchemaVersion, KeyID: claim.KeyID().String(), Nonce: claim.Nonce(),
		PayloadDigest: claim.PayloadDigest().String(), ReceiptDigest: claim.ReceiptDigest().String(),
		ExpiresAt: claim.ExpiresAt().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(workspace.DigestBytes([]byte(claim.KeyID().String()+"\x00"+claim.Nonce())).String(), "sha256:") + ".json"
	path := filepath.Join(store.directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return fmt.Errorf("control-plane nonce %q for key %s was already claimed", claim.Nonce(), claim.KeyID())
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(append(content, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	directory, err := os.Open(store.directory)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type controlGrantInput struct {
	SchemaVersion               int             `json:"schema_version"`
	OccurredAt                  string          `json:"occurred_at"`
	SerialSegment               string          `json:"serial_segment"`
	Base                        string          `json:"base"`
	Head                        string          `json:"head"`
	Actions                     []string        `json:"actions"`
	ExpiresAt                   string          `json:"expires_at"`
	Epoch                       uint64          `json:"epoch"`
	RequiresProviderPullRequest bool            `json:"requires_provider_pull_request"`
	Receipt                     json.RawMessage `json:"receipt"`
}

type controlRevokeInput struct {
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	TargetGrant   string          `json:"target_grant,omitempty"`
	NextEpoch     uint64          `json:"next_epoch"`
	Reason        string          `json:"reason"`
	Receipt       json.RawMessage `json:"receipt"`
}

type controlSafetyInput struct {
	SchemaVersion         int             `json:"schema_version"`
	OccurredAt            string          `json:"occurred_at"`
	GatesBlocked          bool            `json:"gates_blocked"`
	ReconciliationPending bool            `json:"reconciliation_pending"`
	DriftDetected         bool            `json:"drift_detected"`
	AmbiguousEffect       bool            `json:"ambiguous_effect"`
	Receipt               json.RawMessage `json:"receipt"`
}

type controlSegmentInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	SerialSegment string `json:"serial_segment"`
}

type ControlCommandResult struct {
	SchemaVersion int                       `json:"schema_version"`
	Status        string                    `json:"status"`
	Action        string                    `json:"action"`
	Detail        any                       `json:"detail,omitempty"`
	Report        workspace.WorkspaceReport `json:"report"`
}

func executeControl(ctx context.Context, bundle workspace.WorkspaceBundle, options Options) (any, error) {
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return nil, err
	}
	defer journal.Close()
	definition := bundle.Definition()
	switch options.Subaction {
	case "grant":
		var input controlGrantInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		segment, err := parseID(input.SerialSegment, "serial_segment")
		if err != nil {
			return nil, err
		}
		base, err := parseGitObject(input.Base, "base")
		if err != nil {
			return nil, err
		}
		head, err := parseGitObject(input.Head, "head")
		if err != nil {
			return nil, err
		}
		frontier, err := workspace.NewAuthorizationFrontier(base, head)
		if err != nil {
			return nil, err
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, input.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("expires_at must be RFC3339Nano")
		}
		actions := make([]workspace.StandingAuthorizationAction, 0, len(input.Actions))
		for _, action := range input.Actions {
			actions = append(actions, workspace.StandingAuthorizationAction(action))
		}
		scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
			WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(), Remote: definition.Workspace().Remote(),
			Generation: definition.Generation(), SerialSegment: segment, Frontier: frontier, Actions: actions,
			ExpiresAt: expiresAt.UTC(), Epoch: input.Epoch, RequiresProviderPullRequest: input.RequiresProviderPullRequest,
		})
		if err != nil {
			return nil, err
		}
		receipt, verifier, err := controlPlaneInputs(bundle, options.WorkspaceDir, input.Receipt)
		if err != nil {
			return nil, err
		}
		grant, _, err := workspace.RecordStandingGrant(ctx, journal, definition, verifier, scope, receipt, occurredAt)
		if err != nil {
			return nil, err
		}
		return controlCommandResult("control.grant", map[string]any{
			"grant_id": grant.GrantID().String(), "receipt_digest": grant.ReceiptDigest().String(), "request_digest": grant.RequestDigest().String(),
		}, journal, definition)
	case "revoke":
		var input controlRevokeInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		reason, err := parseID(input.Reason, "reason")
		if err != nil {
			return nil, err
		}
		target := workspace.Digest{}
		if input.TargetGrant != "" {
			target, err = parseDigest(input.TargetGrant, "target_grant")
			if err != nil {
				return nil, err
			}
		}
		receipt, verifier, err := controlPlaneInputs(bundle, options.WorkspaceDir, input.Receipt)
		if err != nil {
			return nil, err
		}
		revocation, _, err := workspace.RecordAuthorizationRevocation(ctx, journal, definition, verifier, workspace.AuthorizationRevocationOptions{
			WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(), Remote: definition.Workspace().Remote(),
			Generation: definition.Generation(), TargetGrant: target, NextEpoch: input.NextEpoch, Reason: reason,
		}, receipt, occurredAt)
		if err != nil {
			return nil, err
		}
		return controlCommandResult("control.revoke", map[string]any{
			"revocation_digest": revocation.Digest().String(), "receipt_digest": revocation.ReceiptDigest().String(),
		}, journal, definition)
	case "safety":
		var input controlSafetyInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		receipt, verifier, err := controlPlaneInputs(bundle, options.WorkspaceDir, input.Receipt)
		if err != nil {
			return nil, err
		}
		safety := workspace.NewAuthorizationSafetyState(
			input.GatesBlocked, input.ReconciliationPending, input.DriftDetected, input.AmbiguousEffect,
		)
		if _, err := workspace.RecordAuthorizationSafetyChange(ctx, journal, definition, verifier, safety, receipt, occurredAt); err != nil {
			return nil, err
		}
		return controlCommandResult("control.safety", nil, journal, definition)
	case "segment-complete":
		var input controlSegmentInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		segment, err := parseID(input.SerialSegment, "serial_segment")
		if err != nil {
			return nil, err
		}
		if _, err := workspace.RecordAuthorizationSegmentCompletion(journal, definition, segment, occurredAt); err != nil {
			return nil, err
		}
		return controlCommandResult("control.segment-complete", nil, journal, definition)
	case "inspect-receipt":
		var input struct {
			SchemaVersion int             `json:"schema_version"`
			Receipt       json.RawMessage `json:"receipt"`
		}
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		if input.SchemaVersion != requestSchemaVersion {
			return nil, fmt.Errorf("workspace command schema_version must be %d", requestSchemaVersion)
		}
		receipt, err := workspace.DecodeControlPlaneReceiptV2(input.Receipt)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"schema_version": requestSchemaVersion, "status": "canonical", "key_id": receipt.KeyID().String(),
			"adapter_id": receipt.AdapterID().String(), "nonce": receipt.Nonce(), "expires_at": receipt.ExpiresAt(),
			"payload_digest": receipt.PayloadDigest().String(), "receipt_digest": receipt.ReceiptDigest().String(),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported workspace control action %q", options.Subaction)
	}
}

func controlPlaneInputs(
	bundle workspace.WorkspaceBundle,
	workspaceDir string,
	receiptBytes json.RawMessage,
) (workspace.ControlPlaneReceiptV2, workspace.ControlPlaneVerifierPort, error) {
	receipt, err := workspace.DecodeControlPlaneReceiptV2(receiptBytes)
	if err != nil {
		return workspace.ControlPlaneReceiptV2{}, nil, err
	}
	verifier, err := newControlPlaneVerifier(bundle, workspaceDir)
	if err != nil {
		return workspace.ControlPlaneReceiptV2{}, nil, err
	}
	return receipt, verifier, nil
}

func newControlPlaneVerifier(bundle workspace.WorkspaceBundle, workspaceDir string) (*workspace.Ed25519ControlPlaneVerifier, error) {
	authorityID := bundle.ControlPlaneAuthorityID()
	if authorityID.IsZero() {
		return nil, fmt.Errorf("workspace bundle has no control_plane_authority")
	}
	var content []byte
	for _, material := range bundle.Sources().Authorities {
		if material.ID == authorityID.String() {
			content = material.Content
			break
		}
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("control-plane authority %s has no material", authorityID)
	}
	var document controlPlaneAuthorityDocument
	if err := decodeRequest(content, &document); err != nil {
		return nil, fmt.Errorf("decode control-plane authority %s: %w", authorityID, err)
	}
	if document.SchemaVersion != requestSchemaVersion {
		return nil, fmt.Errorf("control-plane authority schema_version must be %d", requestSchemaVersion)
	}
	adapterID, err := parseID(document.AdapterID, "control-plane adapter_id")
	if err != nil {
		return nil, err
	}
	anchors := staticTrustAnchors{values: make(map[string]workspace.ControlPlaneTrustAnchor, len(document.Keys))}
	byKind := make(map[workspace.ControlPlaneReceiptKind][]workspace.ID)
	for index, item := range document.Keys {
		keyID, err := parseID(item.ID, fmt.Sprintf("control-plane keys[%d].id", index))
		if err != nil {
			return nil, err
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(item.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("control-plane key %s must be a base64 Ed25519 public key", keyID)
		}
		anchor, err := workspace.NewControlPlaneTrustAnchor(keyID, ed25519.PublicKey(decoded))
		if err != nil {
			return nil, err
		}
		if _, exists := anchors.values[keyID.String()]; exists {
			return nil, fmt.Errorf("duplicate control-plane key %s", keyID)
		}
		anchors.values[keyID.String()] = anchor
		for _, rawKind := range item.Kinds {
			kind := workspace.ControlPlaneReceiptKind(rawKind)
			byKind[kind] = append(byKind[kind], keyID)
		}
	}
	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	rules := make([]workspace.ProtectedControlPlaneKeyRule, 0, len(kinds))
	for _, rawKind := range kinds {
		kind := workspace.ControlPlaneReceiptKind(rawKind)
		rules = append(rules, workspace.ProtectedControlPlaneKeyRule{Kind: kind, KeyIDs: byKind[kind]})
	}
	policy, err := workspace.NewProtectedControlPlaneKeyPolicy(rules)
	if err != nil {
		return nil, err
	}
	directory, err := absoluteDirectory(workspaceDir, "workspace")
	if err != nil {
		return nil, err
	}
	replay := durableReplayStore{directory: filepath.Join(workspace.WorkspaceStateDirectory(directory), "control-plane-replay")}
	return workspace.NewEd25519ControlPlaneVerifier(adapterID, policy, anchors, replay, systemClock{})
}

func controlCommandResult(action string, detail any, journal *workspace.WorkspaceJournal, definition workspace.EffectiveWorkspaceDefinition) (ControlCommandResult, error) {
	base, err := mutationResult(action, journal, definition, nil)
	if err != nil {
		return ControlCommandResult{}, err
	}
	return ControlCommandResult{
		SchemaVersion: requestSchemaVersion, Status: base.Status, Action: action, Detail: detail, Report: base.Report,
	}, nil
}

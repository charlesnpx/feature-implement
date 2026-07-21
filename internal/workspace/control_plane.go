package workspace

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ControlPlaneReceiptKind is a closed set of coordinator attestations. New
// protected transitions must add a distinct kind instead of accepting a
// caller-supplied label.
type ControlPlaneReceiptKind string

const (
	ControlPlaneReceiptOwnerDecision      ControlPlaneReceiptKind = "owner_decision"
	ControlPlaneReceiptStandingGrant      ControlPlaneReceiptKind = "standing_grant"
	ControlPlaneReceiptRevocation         ControlPlaneReceiptKind = "revocation"
	ControlPlaneReceiptReviewEvidence     ControlPlaneReceiptKind = "review_evidence"
	ControlPlaneReceiptGoalAcknowledgment ControlPlaneReceiptKind = "goal_acknowledgment"
	ControlPlaneReceiptReconciliation     ControlPlaneReceiptKind = "reconciliation"
)

func (kind ControlPlaneReceiptKind) valid() bool {
	switch kind {
	case ControlPlaneReceiptOwnerDecision, ControlPlaneReceiptStandingGrant,
		ControlPlaneReceiptRevocation, ControlPlaneReceiptReviewEvidence,
		ControlPlaneReceiptGoalAcknowledgment, ControlPlaneReceiptReconciliation:
		return true
	default:
		return false
	}
}

// PullRequestIdentity is provider-derived identity. It cannot be represented
// by a free-form string and always remains bound to its repository.
type PullRequestIdentity struct {
	provider   ID
	repository RepositoryIdentity
	number     uint64
}

func NewPullRequestIdentity(provider ID, repository RepositoryIdentity, number uint64) (PullRequestIdentity, error) {
	if provider.IsZero() || repository.String() == "" || number == 0 {
		return PullRequestIdentity{}, fmt.Errorf("pull request identity requires provider, repository, and positive number")
	}
	return PullRequestIdentity{provider: provider, repository: repository, number: number}, nil
}

func (identity PullRequestIdentity) Provider() ID                   { return identity.provider }
func (identity PullRequestIdentity) Repository() RepositoryIdentity { return identity.repository }
func (identity PullRequestIdentity) Number() uint64                 { return identity.number }
func (identity PullRequestIdentity) IsZero() bool {
	return identity.provider.IsZero() && identity.repository.String() == "" && identity.number == 0
}

// ControlPlaneBindingOptions contains only typed, transition-derived values.
// The implementing agent never supplies an identity string that can satisfy a
// protected gate.
type ControlPlaneBindingOptions struct {
	Kind            ControlPlaneReceiptKind
	WorkspaceID     ID
	Generation      Digest
	RequestDigest   Digest
	DirectiveDigest Digest
	Repository      RepositoryIdentity
	Remote          string
	Base            GitObjectID
	Head            GitObjectID
	Tree            GitObjectID
	PullRequest     PullRequestIdentity
	Epoch           uint64
}

// ControlPlaneBinding is the expected semantic payload for one protected
// transition. It is comparable, immutable, and safe to match exactly.
type ControlPlaneBinding struct {
	kind            ControlPlaneReceiptKind
	workspaceID     ID
	generation      Digest
	requestDigest   Digest
	directiveDigest Digest
	repository      RepositoryIdentity
	remote          string
	base            GitObjectID
	head            GitObjectID
	tree            GitObjectID
	pullRequest     PullRequestIdentity
	epoch           uint64
}

func NewControlPlaneBinding(options ControlPlaneBindingOptions) (ControlPlaneBinding, error) {
	remote := strings.TrimSpace(options.Remote)
	if !options.Kind.valid() || options.WorkspaceID.IsZero() || options.Generation.IsZero() ||
		options.RequestDigest.IsZero() || options.Repository.String() == "" {
		return ControlPlaneBinding{}, fmt.Errorf("control-plane binding requires kind, workspace, generation, request, and repository")
	}
	if err := validateBoundedText("control-plane remote", remote, 512); err != nil {
		return ControlPlaneBinding{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") {
		return ControlPlaneBinding{}, fmt.Errorf("control-plane remote must be a single token")
	}
	if !options.PullRequest.IsZero() && options.PullRequest.repository != options.Repository {
		return ControlPlaneBinding{}, fmt.Errorf("control-plane pull request belongs to a different repository")
	}
	objects := []GitObjectID{options.Base, options.Head, options.Tree}
	var algorithm GitHashAlgorithm
	for _, object := range objects {
		if object.IsZero() {
			continue
		}
		if algorithm == "" {
			algorithm = object.Algorithm()
		} else if object.Algorithm() != algorithm {
			return ControlPlaneBinding{}, fmt.Errorf("control-plane Git identities use different object formats")
		}
	}
	switch options.Kind {
	case ControlPlaneReceiptOwnerDecision, ControlPlaneReceiptGoalAcknowledgment:
		if options.DirectiveDigest.IsZero() || options.Base.IsZero() || options.Head.IsZero() || options.Epoch != 0 {
			return ControlPlaneBinding{}, fmt.Errorf("%s receipt requires directive, base, and head bindings without an authorization epoch", options.Kind)
		}
	case ControlPlaneReceiptReviewEvidence:
		if options.Head.IsZero() || options.Tree.IsZero() || options.Epoch != 0 {
			return ControlPlaneBinding{}, fmt.Errorf("review evidence requires exact head and tree bindings without an authorization epoch")
		}
	case ControlPlaneReceiptStandingGrant:
		if options.Head.IsZero() || options.Epoch == 0 {
			return ControlPlaneBinding{}, fmt.Errorf("standing grant requires a frontier head and positive authorization epoch")
		}
	case ControlPlaneReceiptRevocation:
		if options.Epoch == 0 {
			return ControlPlaneBinding{}, fmt.Errorf("revocation requires a positive authorization epoch")
		}
	case ControlPlaneReceiptReconciliation:
		if !options.DirectiveDigest.IsZero() || options.Epoch != 0 || !options.PullRequest.IsZero() {
			return ControlPlaneBinding{}, fmt.Errorf("reconciliation cannot carry directive, authorization epoch, or pull request identity")
		}
	}
	return ControlPlaneBinding{
		kind: options.Kind, workspaceID: options.WorkspaceID, generation: options.Generation,
		requestDigest: options.RequestDigest, directiveDigest: options.DirectiveDigest,
		repository: options.Repository, remote: remote, base: options.Base, head: options.Head,
		tree: options.Tree, pullRequest: options.PullRequest, epoch: options.Epoch,
	}, nil
}

func (binding ControlPlaneBinding) Kind() ControlPlaneReceiptKind  { return binding.kind }
func (binding ControlPlaneBinding) WorkspaceID() ID                { return binding.workspaceID }
func (binding ControlPlaneBinding) Generation() Digest             { return binding.generation }
func (binding ControlPlaneBinding) RequestDigest() Digest          { return binding.requestDigest }
func (binding ControlPlaneBinding) DirectiveDigest() Digest        { return binding.directiveDigest }
func (binding ControlPlaneBinding) Repository() RepositoryIdentity { return binding.repository }
func (binding ControlPlaneBinding) Remote() string                 { return binding.remote }
func (binding ControlPlaneBinding) Base() GitObjectID              { return binding.base }
func (binding ControlPlaneBinding) Head() GitObjectID              { return binding.head }
func (binding ControlPlaneBinding) Tree() GitObjectID              { return binding.tree }
func (binding ControlPlaneBinding) PullRequest() (PullRequestIdentity, bool) {
	return binding.pullRequest, !binding.pullRequest.IsZero()
}
func (binding ControlPlaneBinding) Epoch() uint64 { return binding.epoch }

// ControlPlaneEnvelopeV2 is the complete canonical signed payload. Key ID,
// nonce, expiry, and adapter are inside the digest rather than unsigned
// envelope metadata.
type ControlPlaneEnvelopeV2 struct {
	binding   ControlPlaneBinding
	keyID     ID
	nonce     string
	expiresAt time.Time
	adapterID ID
}

func NewControlPlaneEnvelopeV2(
	binding ControlPlaneBinding,
	keyID ID,
	nonce string,
	expiresAt time.Time,
	adapterID ID,
) (ControlPlaneEnvelopeV2, error) {
	nonce = strings.TrimSpace(nonce)
	if !binding.kind.valid() || keyID.IsZero() || adapterID.IsZero() || expiresAt.IsZero() {
		return ControlPlaneEnvelopeV2{}, fmt.Errorf("control-plane envelope requires binding, key, expiry, and adapter")
	}
	if err := validateBoundedText("control-plane nonce", nonce, 512); err != nil {
		return ControlPlaneEnvelopeV2{}, err
	}
	return ControlPlaneEnvelopeV2{
		binding: binding, keyID: keyID, nonce: nonce,
		expiresAt: expiresAt.UTC(), adapterID: adapterID,
	}, nil
}

func (envelope ControlPlaneEnvelopeV2) Binding() ControlPlaneBinding { return envelope.binding }
func (envelope ControlPlaneEnvelopeV2) KeyID() ID                    { return envelope.keyID }
func (envelope ControlPlaneEnvelopeV2) Nonce() string                { return envelope.nonce }
func (envelope ControlPlaneEnvelopeV2) ExpiresAt() time.Time         { return envelope.expiresAt }
func (envelope ControlPlaneEnvelopeV2) AdapterID() ID                { return envelope.adapterID }
func (envelope ControlPlaneEnvelopeV2) PayloadDigest() Digest {
	canonical, err := canonicalControlPlaneEnvelope(envelope)
	if err != nil {
		return Digest{}
	}
	return DigestBytes(canonical)
}

// ControlPlaneReceiptV2 is signed coordinator evidence. Its signature and all
// byte-returning accessors are defensively copied.
type ControlPlaneReceiptV2 struct {
	envelope      ControlPlaneEnvelopeV2
	payloadDigest Digest
	signature     []byte
	receiptDigest Digest
}

func NewControlPlaneReceiptV2(envelope ControlPlaneEnvelopeV2, signature []byte) (ControlPlaneReceiptV2, error) {
	canonical, err := canonicalControlPlaneEnvelope(envelope)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	if len(signature) != ed25519.SignatureSize {
		return ControlPlaneReceiptV2{}, fmt.Errorf("control-plane receipt requires an Ed25519 signature")
	}
	receipt := ControlPlaneReceiptV2{
		envelope: envelope, payloadDigest: DigestBytes(canonical),
		signature: append([]byte(nil), signature...),
	}
	receiptBytes, err := marshalControlPlaneReceipt(receipt)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	receipt.receiptDigest = DigestBytes(receiptBytes)
	return receipt, nil
}

func (receipt ControlPlaneReceiptV2) Binding() ControlPlaneBinding { return receipt.envelope.binding }
func (receipt ControlPlaneReceiptV2) KeyID() ID                    { return receipt.envelope.keyID }
func (receipt ControlPlaneReceiptV2) Nonce() string                { return receipt.envelope.nonce }
func (receipt ControlPlaneReceiptV2) ExpiresAt() time.Time         { return receipt.envelope.expiresAt }
func (receipt ControlPlaneReceiptV2) AdapterID() ID                { return receipt.envelope.adapterID }
func (receipt ControlPlaneReceiptV2) PayloadDigest() Digest        { return receipt.payloadDigest }
func (receipt ControlPlaneReceiptV2) ReceiptDigest() Digest        { return receipt.receiptDigest }
func (receipt ControlPlaneReceiptV2) Signature() []byte {
	return append([]byte(nil), receipt.signature...)
}

func (receipt ControlPlaneReceiptV2) MarshalJSON() ([]byte, error) {
	return marshalControlPlaneReceipt(receipt)
}

func DecodeControlPlaneReceiptV2(source []byte) (ControlPlaneReceiptV2, error) {
	var wire controlPlaneReceiptWire
	if err := decodeStrictJSON(source, &wire); err != nil {
		return ControlPlaneReceiptV2{}, fmt.Errorf("decode control-plane receipt: %w", err)
	}
	if wire.SchemaVersion != 2 {
		return ControlPlaneReceiptV2{}, fmt.Errorf("control-plane receipt schema_version %d is not supported", wire.SchemaVersion)
	}
	binding, err := bindingFromControlPlaneWire(wire.controlPlanePayloadWire)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	keyID, err := NewID(wire.KeyID)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	adapterID, err := NewID(wire.AdapterID)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil || wire.ExpiresAt != expiresAt.UTC().Format(time.RFC3339Nano) {
		return ControlPlaneReceiptV2{}, fmt.Errorf("control-plane receipt expiry must be canonical UTC RFC3339Nano")
	}
	envelope, err := NewControlPlaneEnvelopeV2(binding, keyID, wire.Nonce, expiresAt, adapterID)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	payloadDigest, err := ParseDigest(wire.PayloadDigest)
	if err != nil || payloadDigest != envelope.PayloadDigest() {
		return ControlPlaneReceiptV2{}, fmt.Errorf("control-plane receipt payload digest mismatch")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(wire.Signature)
	if err != nil {
		return ControlPlaneReceiptV2{}, fmt.Errorf("control-plane receipt signature is not canonical base64")
	}
	receipt, err := NewControlPlaneReceiptV2(envelope, signature)
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	canonical, err := receipt.MarshalJSON()
	if err != nil {
		return ControlPlaneReceiptV2{}, err
	}
	if !bytes.Equal(canonical, source) {
		return ControlPlaneReceiptV2{}, fmt.Errorf("control-plane receipt is not canonical JSON")
	}
	return receipt, nil
}

type controlPlanePullRequestWire struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Number     uint64 `json:"number"`
}

type controlPlanePayloadWire struct {
	SchemaVersion   int                          `json:"schema_version"`
	Kind            ControlPlaneReceiptKind      `json:"kind"`
	WorkspaceID     string                       `json:"workspace_id"`
	Generation      string                       `json:"generation"`
	RequestDigest   string                       `json:"request_digest"`
	DirectiveDigest string                       `json:"directive_digest,omitempty"`
	KeyID           string                       `json:"key_id"`
	Nonce           string                       `json:"nonce"`
	ExpiresAt       string                       `json:"expires_at"`
	AdapterID       string                       `json:"adapter_id"`
	Repository      string                       `json:"repository"`
	Remote          string                       `json:"remote"`
	Base            string                       `json:"base,omitempty"`
	Head            string                       `json:"head,omitempty"`
	Tree            string                       `json:"tree,omitempty"`
	PullRequest     *controlPlanePullRequestWire `json:"pull_request,omitempty"`
	Epoch           uint64                       `json:"epoch,omitempty"`
}

type controlPlaneReceiptWire struct {
	controlPlanePayloadWire
	PayloadDigest string `json:"payload_digest"`
	Signature     string `json:"signature"`
}

func controlPlaneWireFromEnvelope(envelope ControlPlaneEnvelopeV2) controlPlanePayloadWire {
	binding := envelope.binding
	wire := controlPlanePayloadWire{
		SchemaVersion: 2, Kind: binding.kind, WorkspaceID: binding.workspaceID.String(),
		Generation: binding.generation.String(), RequestDigest: binding.requestDigest.String(),
		DirectiveDigest: binding.directiveDigest.String(), KeyID: envelope.keyID.String(),
		Nonce: envelope.nonce, ExpiresAt: envelope.expiresAt.UTC().Format(time.RFC3339Nano),
		AdapterID: envelope.adapterID.String(), Repository: binding.repository.String(), Remote: binding.remote,
		Base: binding.base.String(), Head: binding.head.String(), Tree: binding.tree.String(), Epoch: binding.epoch,
	}
	if !binding.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: binding.pullRequest.provider.String(), Repository: binding.pullRequest.repository.String(),
			Number: binding.pullRequest.number,
		}
	}
	return wire
}

func canonicalControlPlaneEnvelope(envelope ControlPlaneEnvelopeV2) ([]byte, error) {
	if !envelope.binding.kind.valid() || envelope.keyID.IsZero() || envelope.adapterID.IsZero() ||
		envelope.nonce == "" || envelope.expiresAt.IsZero() {
		return nil, fmt.Errorf("control-plane envelope is incomplete")
	}
	return json.Marshal(controlPlaneWireFromEnvelope(envelope))
}

func marshalControlPlaneReceipt(receipt ControlPlaneReceiptV2) ([]byte, error) {
	if receipt.payloadDigest.IsZero() || len(receipt.signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("control-plane receipt is incomplete")
	}
	wire := controlPlaneReceiptWire{
		controlPlanePayloadWire: controlPlaneWireFromEnvelope(receipt.envelope),
		PayloadDigest:           receipt.payloadDigest.String(),
		Signature:               base64.StdEncoding.EncodeToString(receipt.signature),
	}
	return json.Marshal(wire)
}

func bindingFromControlPlaneWire(wire controlPlanePayloadWire) (ControlPlaneBinding, error) {
	workspaceID, err := NewID(wire.WorkspaceID)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	request, err := ParseDigest(wire.RequestDigest)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	var directive Digest
	if wire.DirectiveDigest != "" {
		directive, err = ParseDigest(wire.DirectiveDigest)
		if err != nil {
			return ControlPlaneBinding{}, err
		}
	}
	repository, err := NewRepositoryIdentity(wire.Repository)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	parseObject := func(value string) (GitObjectID, error) {
		if value == "" {
			return GitObjectID{}, nil
		}
		return ParseGitObjectID(value)
	}
	base, err := parseObject(wire.Base)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	head, err := parseObject(wire.Head)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	tree, err := parseObject(wire.Tree)
	if err != nil {
		return ControlPlaneBinding{}, err
	}
	var pullRequest PullRequestIdentity
	if wire.PullRequest != nil {
		provider, err := NewID(wire.PullRequest.Provider)
		if err != nil {
			return ControlPlaneBinding{}, err
		}
		prRepository, err := NewRepositoryIdentity(wire.PullRequest.Repository)
		if err != nil {
			return ControlPlaneBinding{}, err
		}
		pullRequest, err = NewPullRequestIdentity(provider, prRepository, wire.PullRequest.Number)
		if err != nil {
			return ControlPlaneBinding{}, err
		}
	}
	return NewControlPlaneBinding(ControlPlaneBindingOptions{
		Kind: wire.Kind, WorkspaceID: workspaceID, Generation: generation,
		RequestDigest: request, DirectiveDigest: directive, Repository: repository, Remote: wire.Remote,
		Base: base, Head: head, Tree: tree, PullRequest: pullRequest, Epoch: wire.Epoch,
	})
}

type ControlPlaneVerification struct {
	binding ControlPlaneBinding
}

func NewControlPlaneVerification(binding ControlPlaneBinding) (ControlPlaneVerification, error) {
	if !binding.kind.valid() || binding.workspaceID.IsZero() || binding.generation.IsZero() || binding.requestDigest.IsZero() {
		return ControlPlaneVerification{}, fmt.Errorf("control-plane verification requires a complete binding")
	}
	return ControlPlaneVerification{binding: binding}, nil
}

func (verification ControlPlaneVerification) Binding() ControlPlaneBinding {
	return verification.binding
}
func (verification ControlPlaneVerification) WorkspaceID() ID {
	return verification.binding.workspaceID
}
func (verification ControlPlaneVerification) Generation() Digest {
	return verification.binding.generation
}
func (verification ControlPlaneVerification) RequestDigest() Digest {
	return verification.binding.requestDigest
}

type ControlPlaneVerifierPort interface {
	Verify(context.Context, ControlPlaneVerification, ControlPlaneReceiptV2) error
}

type ProtectedControlPlaneKeyRule struct {
	Kind   ControlPlaneReceiptKind
	KeyIDs []ID
}

type protectedControlPlaneKeySet struct {
	kind ControlPlaneReceiptKind
	keys []ID
}

// ProtectedControlPlaneKeyPolicy is immutable and deliberately stores key IDs,
// never trust-anchor bytes or private signing material.
type ProtectedControlPlaneKeyPolicy struct {
	rules []protectedControlPlaneKeySet
}

func NewProtectedControlPlaneKeyPolicy(rules []ProtectedControlPlaneKeyRule) (ProtectedControlPlaneKeyPolicy, error) {
	if len(rules) == 0 {
		return ProtectedControlPlaneKeyPolicy{}, fmt.Errorf("protected control-plane key policy requires rules")
	}
	result := ProtectedControlPlaneKeyPolicy{rules: make([]protectedControlPlaneKeySet, 0, len(rules))}
	seenKinds := make(map[ControlPlaneReceiptKind]struct{}, len(rules))
	for _, rule := range rules {
		if !rule.Kind.valid() || len(rule.KeyIDs) == 0 {
			return ProtectedControlPlaneKeyPolicy{}, fmt.Errorf("protected key rule requires a supported kind and keys")
		}
		if _, exists := seenKinds[rule.Kind]; exists {
			return ProtectedControlPlaneKeyPolicy{}, fmt.Errorf("duplicate protected key rule for %s", rule.Kind)
		}
		seenKinds[rule.Kind] = struct{}{}
		keys := append([]ID(nil), rule.KeyIDs...)
		seenKeys := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			if key.IsZero() {
				return ProtectedControlPlaneKeyPolicy{}, fmt.Errorf("protected key rule %s contains an empty key", rule.Kind)
			}
			if _, exists := seenKeys[key.String()]; exists {
				return ProtectedControlPlaneKeyPolicy{}, fmt.Errorf("protected key rule %s contains duplicate key %s", rule.Kind, key)
			}
			seenKeys[key.String()] = struct{}{}
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		result.rules = append(result.rules, protectedControlPlaneKeySet{kind: rule.Kind, keys: keys})
	}
	sort.Slice(result.rules, func(i, j int) bool { return result.rules[i].kind < result.rules[j].kind })
	return result, nil
}

func (policy ProtectedControlPlaneKeyPolicy) Allows(kind ControlPlaneReceiptKind, keyID ID) bool {
	for _, rule := range policy.rules {
		if rule.kind != kind {
			continue
		}
		for _, key := range rule.keys {
			if key == keyID {
				return true
			}
		}
	}
	return false
}

type ControlPlaneTrustAnchor struct {
	keyID     ID
	publicKey [ed25519.PublicKeySize]byte
}

func NewControlPlaneTrustAnchor(keyID ID, publicKey ed25519.PublicKey) (ControlPlaneTrustAnchor, error) {
	if keyID.IsZero() || len(publicKey) != ed25519.PublicKeySize {
		return ControlPlaneTrustAnchor{}, fmt.Errorf("control-plane trust anchor requires key ID and Ed25519 public key")
	}
	anchor := ControlPlaneTrustAnchor{keyID: keyID}
	copy(anchor.publicKey[:], publicKey)
	return anchor, nil
}

func (anchor ControlPlaneTrustAnchor) KeyID() ID { return anchor.keyID }
func (anchor ControlPlaneTrustAnchor) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), anchor.publicKey[:]...)
}

// ControlPlaneTrustAnchorPort is implemented by the protected coordinator
// environment. Effective definitions and workspace files never contain these
// key bytes.
type ControlPlaneTrustAnchorPort interface {
	ResolveControlPlaneTrustAnchor(context.Context, ID) (ControlPlaneTrustAnchor, error)
}

type ControlPlaneReplayClaim struct {
	keyID         ID
	nonce         string
	payloadDigest Digest
	receiptDigest Digest
	expiresAt     time.Time
}

func NewControlPlaneReplayClaim(receipt ControlPlaneReceiptV2) (ControlPlaneReplayClaim, error) {
	if receipt.KeyID().IsZero() || receipt.Nonce() == "" || receipt.PayloadDigest().IsZero() ||
		receipt.ReceiptDigest().IsZero() || receipt.ExpiresAt().IsZero() {
		return ControlPlaneReplayClaim{}, fmt.Errorf("control-plane replay claim requires a complete receipt")
	}
	return ControlPlaneReplayClaim{
		keyID: receipt.KeyID(), nonce: receipt.Nonce(), payloadDigest: receipt.PayloadDigest(),
		receiptDigest: receipt.ReceiptDigest(), expiresAt: receipt.ExpiresAt(),
	}, nil
}

func (claim ControlPlaneReplayClaim) KeyID() ID             { return claim.keyID }
func (claim ControlPlaneReplayClaim) Nonce() string         { return claim.nonce }
func (claim ControlPlaneReplayClaim) PayloadDigest() Digest { return claim.payloadDigest }
func (claim ControlPlaneReplayClaim) ReceiptDigest() Digest { return claim.receiptDigest }
func (claim ControlPlaneReplayClaim) ExpiresAt() time.Time  { return claim.expiresAt }

// ControlPlaneReplayPort atomically claims key/nonce pairs. Implementations may
// treat the exact same receipt digest as an idempotent retry, but must reject a
// nonce already bound to any different payload or receipt.
type ControlPlaneReplayPort interface {
	ClaimControlPlaneNonce(context.Context, ControlPlaneReplayClaim) error
}

// Ed25519ControlPlaneVerifier contains no key bytes. It resolves anchors and
// atomically claims nonces through coordinator-owned ports for every call.
type Ed25519ControlPlaneVerifier struct {
	adapterID ID
	policy    ProtectedControlPlaneKeyPolicy
	anchors   ControlPlaneTrustAnchorPort
	replay    ControlPlaneReplayPort
	clock     ClockPort
}

func NewEd25519ControlPlaneVerifier(
	adapterID ID,
	policy ProtectedControlPlaneKeyPolicy,
	anchors ControlPlaneTrustAnchorPort,
	replay ControlPlaneReplayPort,
	clock ClockPort,
) (*Ed25519ControlPlaneVerifier, error) {
	if adapterID.IsZero() || len(policy.rules) == 0 || anchors == nil || replay == nil || clock == nil {
		return nil, fmt.Errorf("Ed25519 control-plane verifier requires adapter, key policy, anchors, replay guard, and clock")
	}
	return &Ed25519ControlPlaneVerifier{
		adapterID: adapterID, policy: policy, anchors: anchors, replay: replay, clock: clock,
	}, nil
}

func (verifier *Ed25519ControlPlaneVerifier) Verify(
	ctx context.Context,
	verification ControlPlaneVerification,
	receipt ControlPlaneReceiptV2,
) error {
	if verifier == nil || verifier.anchors == nil || verifier.replay == nil || verifier.clock == nil {
		return fmt.Errorf("protected coordinator is unavailable")
	}
	if !verification.binding.kind.valid() || receipt.PayloadDigest().IsZero() || receipt.ReceiptDigest().IsZero() {
		return fmt.Errorf("control-plane verification and receipt are required")
	}
	if receipt.Binding() != verification.binding {
		return fmt.Errorf("control-plane receipt does not match the exact protected transition binding")
	}
	if receipt.AdapterID() != verifier.adapterID {
		return fmt.Errorf("control-plane receipt adapter %s is not trusted", receipt.AdapterID())
	}
	now := verifier.clock.Now().UTC()
	if now.IsZero() {
		return fmt.Errorf("protected coordinator clock is unavailable")
	}
	if !now.Before(receipt.ExpiresAt()) {
		return fmt.Errorf("control-plane receipt expired at %s", receipt.ExpiresAt().Format(time.RFC3339Nano))
	}
	if !verifier.policy.Allows(receipt.Binding().kind, receipt.KeyID()) {
		return fmt.Errorf("control-plane key %s is not allowed for %s", receipt.KeyID(), receipt.Binding().kind)
	}
	anchor, err := verifier.anchors.ResolveControlPlaneTrustAnchor(ctx, receipt.KeyID())
	if err != nil {
		return fmt.Errorf("resolve protected trust anchor: %w", err)
	}
	if anchor.KeyID() != receipt.KeyID() || len(anchor.PublicKey()) != ed25519.PublicKeySize {
		return fmt.Errorf("protected trust anchor does not match receipt key %s", receipt.KeyID())
	}
	if !ed25519.Verify(anchor.PublicKey(), receipt.PayloadDigest().Bytes(), receipt.Signature()) {
		return fmt.Errorf("control-plane receipt signature is invalid")
	}
	claim, err := NewControlPlaneReplayClaim(receipt)
	if err != nil {
		return err
	}
	if err := verifier.replay.ClaimControlPlaneNonce(ctx, claim); err != nil {
		return fmt.Errorf("claim control-plane nonce: %w", err)
	}
	return nil
}

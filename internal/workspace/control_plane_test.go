package workspace_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type controlPlaneTestClock struct{ now time.Time }

func (clock controlPlaneTestClock) Now() time.Time { return clock.now }

type controlPlaneTestAnchors struct {
	anchors map[string]workspace.ControlPlaneTrustAnchor
	err     error
}

func (store *controlPlaneTestAnchors) ResolveControlPlaneTrustAnchor(
	_ context.Context,
	keyID workspace.ID,
) (workspace.ControlPlaneTrustAnchor, error) {
	if store.err != nil {
		return workspace.ControlPlaneTrustAnchor{}, store.err
	}
	anchor, exists := store.anchors[keyID.String()]
	if !exists {
		return workspace.ControlPlaneTrustAnchor{}, fmt.Errorf("key %s unavailable", keyID)
	}
	return anchor, nil
}

type controlPlaneTestReplay struct {
	claims map[string]workspace.Digest
	err    error
}

func (guard *controlPlaneTestReplay) ClaimControlPlaneNonce(
	_ context.Context,
	claim workspace.ControlPlaneReplayClaim,
) error {
	if guard.err != nil {
		return guard.err
	}
	if guard.claims == nil {
		guard.claims = make(map[string]workspace.Digest)
	}
	key := claim.KeyID().String() + "\x00" + claim.Nonce()
	if existing, claimed := guard.claims[key]; claimed {
		if existing == claim.ReceiptDigest() {
			return nil
		}
		return errors.New("nonce replayed with different receipt")
	}
	guard.claims[key] = claim.ReceiptDigest()
	return nil
}

func TestControlPlaneReceiptV2VerifiesCanonicalEd25519Bindings(t *testing.T) {
	keyID := workspace.MustID("owner-key")
	adapterID := workspace.MustID("coordinator-v2")
	privateKey := ed25519.NewKeyFromSeed(bytesOf(7, ed25519.SeedSize))
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	binding := ownerControlPlaneBinding(t, workspace.DigestBytes([]byte("generation-one")), mustGitObject(t, 'b'), mustGitObject(t, 'c'), workspace.DigestBytes([]byte("directive-one")))
	envelope, err := workspace.NewControlPlaneEnvelopeV2(
		binding, keyID, "nonce-one", mustTime(t, "2026-07-21T18:00:00Z"), adapterID,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt := signedControlPlaneReceipt(t, envelope, privateKey)
	verification, err := workspace.NewControlPlaneVerification(binding)
	if err != nil {
		t.Fatal(err)
	}
	verifier := realControlPlaneVerifier(
		t, adapterID, keyID, publicKey, workspace.ControlPlaneReceiptOwnerDecision,
		mustTime(t, "2026-07-21T17:00:00Z"), &controlPlaneTestReplay{},
	)
	if err := verifier.Verify(context.Background(), verification, receipt); err != nil {
		t.Fatal(err)
	}

	encoded, err := receipt.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"schema_version":2`, `"kind":"owner_decision"`, `"workspace_id":"workspace-one"`,
		`"generation":"` + binding.Generation().String() + `"`, `"request_digest":"` + binding.RequestDigest().String() + `"`,
		`"directive_digest":"` + binding.DirectiveDigest().String() + `"`, `"key_id":"owner-key"`,
		`"nonce":"nonce-one"`, `"expires_at":"2026-07-21T18:00:00Z"`, `"adapter_id":"coordinator-v2"`,
		`"repository":"https://github.com/example/repository.git"`, `"remote":"origin"`,
		`"base":"` + binding.Base().String() + `"`, `"head":"` + binding.Head().String() + `"`,
	} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("canonical receipt does not contain %s: %s", required, encoded)
		}
	}
	decoded, err := workspace.DecodeControlPlaneReceiptV2(encoded)
	if err != nil || decoded.ReceiptDigest() != receipt.ReceiptDigest() || decoded.Binding() != receipt.Binding() {
		t.Fatalf("decoded receipt = %#v, %v", decoded, err)
	}
	if strings.Contains(string(encoded), privateKeySeedMarker(privateKey)) {
		t.Fatal("private signing material leaked into the receipt")
	}
}

func TestControlPlaneVerifierRejectsForgedExpiredReplayedAndMisboundReceipts(t *testing.T) {
	keyID := workspace.MustID("owner-key")
	wrongKeyID := workspace.MustID("wrong-key")
	adapterID := workspace.MustID("coordinator-v2")
	privateKey := ed25519.NewKeyFromSeed(bytesOf(11, ed25519.SeedSize))
	wrongPrivateKey := ed25519.NewKeyFromSeed(bytesOf(12, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	base := mustGitObject(t, 'b')
	head := mustGitObject(t, 'c')
	directive := workspace.DigestBytes([]byte("directive"))
	binding := ownerControlPlaneBinding(t, workspace.DigestBytes([]byte("generation")), base, head, directive)
	verification, _ := workspace.NewControlPlaneVerification(binding)

	newReceipt := func(t *testing.T, candidate workspace.ControlPlaneBinding, candidateKey workspace.ID, nonce, expiry string, signer ed25519.PrivateKey, adapter workspace.ID) workspace.ControlPlaneReceiptV2 {
		t.Helper()
		envelope, err := workspace.NewControlPlaneEnvelopeV2(candidate, candidateKey, nonce, mustTime(t, expiry), adapter)
		if err != nil {
			t.Fatal(err)
		}
		return signedControlPlaneReceipt(t, envelope, signer)
	}
	newVerifier := func(t *testing.T, now string, replay *controlPlaneTestReplay) *workspace.Ed25519ControlPlaneVerifier {
		t.Helper()
		return realControlPlaneVerifier(
			t, adapterID, keyID, publicKey, workspace.ControlPlaneReceiptOwnerDecision, mustTime(t, now), replay,
		)
	}

	t.Run("forged", func(t *testing.T) {
		receipt := newReceipt(t, binding, keyID, "forged", "2026-07-21T18:00:00Z", wrongPrivateKey, adapterID)
		if err := newVerifier(t, "2026-07-21T17:00:00Z", &controlPlaneTestReplay{}).Verify(context.Background(), verification, receipt); err == nil || !strings.Contains(err.Error(), "signature") {
			t.Fatalf("forged receipt error = %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		receipt := newReceipt(t, binding, keyID, "expired", "2026-07-21T17:00:00Z", privateKey, adapterID)
		if err := newVerifier(t, "2026-07-21T17:00:00Z", &controlPlaneTestReplay{}).Verify(context.Background(), verification, receipt); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired receipt error = %v", err)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		receipt := newReceipt(t, binding, wrongKeyID, "wrong-key", "2026-07-21T18:00:00Z", privateKey, adapterID)
		if err := newVerifier(t, "2026-07-21T17:00:00Z", &controlPlaneTestReplay{}).Verify(context.Background(), verification, receipt); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("wrong-key receipt error = %v", err)
		}
	})
	t.Run("wrong adapter", func(t *testing.T) {
		receipt := newReceipt(t, binding, keyID, "wrong-adapter", "2026-07-21T18:00:00Z", privateKey, workspace.MustID("other-adapter"))
		if err := newVerifier(t, "2026-07-21T17:00:00Z", &controlPlaneTestReplay{}).Verify(context.Background(), verification, receipt); err == nil || !strings.Contains(err.Error(), "adapter") {
			t.Fatalf("wrong-adapter receipt error = %v", err)
		}
	})

	bindings := map[string]workspace.ControlPlaneBinding{
		"wrong generation": ownerControlPlaneBinding(t, workspace.DigestBytes([]byte("other-generation")), base, head, directive),
		"wrong head":       ownerControlPlaneBinding(t, binding.Generation(), base, mustGitObject(t, 'd'), directive),
		"wrong directive":  ownerControlPlaneBinding(t, binding.Generation(), base, head, workspace.DigestBytes([]byte("other-directive"))),
	}
	for name, candidate := range bindings {
		t.Run(name, func(t *testing.T) {
			receipt := newReceipt(t, candidate, keyID, strings.ReplaceAll(name, " ", "-"), "2026-07-21T18:00:00Z", privateKey, adapterID)
			if err := newVerifier(t, "2026-07-21T17:00:00Z", &controlPlaneTestReplay{}).Verify(context.Background(), verification, receipt); err == nil || !strings.Contains(err.Error(), "exact protected transition") {
				t.Fatalf("misbound receipt error = %v", err)
			}
		})
	}

	t.Run("nonce replay", func(t *testing.T) {
		replay := &controlPlaneTestReplay{}
		verifier := newVerifier(t, "2026-07-21T17:00:00Z", replay)
		first := newReceipt(t, binding, keyID, "same-nonce", "2026-07-21T18:00:00Z", privateKey, adapterID)
		if err := verifier.Verify(context.Background(), verification, first); err != nil {
			t.Fatal(err)
		}
		second := newReceipt(t, binding, keyID, "same-nonce", "2026-07-21T18:30:00Z", privateKey, adapterID)
		if err := verifier.Verify(context.Background(), verification, second); err == nil || !strings.Contains(err.Error(), "replay") {
			t.Fatalf("nonce replay error = %v", err)
		}
		if err := verifier.Verify(context.Background(), verification, first); err != nil {
			t.Fatalf("exact crash-retry was not idempotent: %v", err)
		}
	})
}

func TestControlPlaneFailsClosedAndRejectsUnsignedOrNoncanonicalJSON(t *testing.T) {
	keyID := workspace.MustID("owner-key")
	adapterID := workspace.MustID("coordinator-v2")
	privateKey := ed25519.NewKeyFromSeed(bytesOf(21, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	binding := ownerControlPlaneBinding(
		t, workspace.DigestBytes([]byte("generation")), mustGitObject(t, 'b'), mustGitObject(t, 'c'),
		workspace.DigestBytes([]byte("directive")),
	)
	envelope, _ := workspace.NewControlPlaneEnvelopeV2(
		binding, keyID, "nonce", mustTime(t, "2026-07-21T18:00:00Z"), adapterID,
	)
	receipt := signedControlPlaneReceipt(t, envelope, privateKey)
	verification, _ := workspace.NewControlPlaneVerification(binding)

	policy, _ := workspace.NewProtectedControlPlaneKeyPolicy([]workspace.ProtectedControlPlaneKeyRule{{
		Kind: workspace.ControlPlaneReceiptOwnerDecision, KeyIDs: []workspace.ID{keyID},
	}})
	if _, err := workspace.NewEd25519ControlPlaneVerifier(adapterID, policy, nil, &controlPlaneTestReplay{}, controlPlaneTestClock{now: mustTime(t, "2026-07-21T17:00:00Z")}); err == nil {
		t.Fatal("missing coordinator trust-anchor port was accepted")
	}
	anchor, _ := workspace.NewControlPlaneTrustAnchor(keyID, publicKey)
	unavailable := &controlPlaneTestAnchors{anchors: map[string]workspace.ControlPlaneTrustAnchor{keyID.String(): anchor}, err: errors.New("coordinator offline")}
	verifier, _ := workspace.NewEd25519ControlPlaneVerifier(
		adapterID, policy, unavailable, &controlPlaneTestReplay{}, controlPlaneTestClock{now: mustTime(t, "2026-07-21T17:00:00Z")},
	)
	if err := verifier.Verify(context.Background(), verification, receipt); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("unavailable coordinator error = %v", err)
	}

	encoded, _ := receipt.MarshalJSON()
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "signature")
	unsigned, _ := json.Marshal(document)
	if _, err := workspace.DecodeControlPlaneReceiptV2(unsigned); err == nil {
		t.Fatal("unsigned JSON decoded as a control-plane receipt")
	}
	document["signature"] = "not-a-signature"
	document["unknown"] = true
	unknown, _ := json.Marshal(document)
	if _, err := workspace.DecodeControlPlaneReceiptV2(unknown); err == nil {
		t.Fatal("unknown-field receipt JSON was accepted")
	}
	if _, err := workspace.DecodeControlPlaneReceiptV2(append([]byte(" "), encoded...)); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("noncanonical receipt JSON error = %v", err)
	}
}

func realControlPlaneVerifier(
	t *testing.T,
	adapterID, keyID workspace.ID,
	publicKey ed25519.PublicKey,
	kind workspace.ControlPlaneReceiptKind,
	now time.Time,
	replay *controlPlaneTestReplay,
) *workspace.Ed25519ControlPlaneVerifier {
	t.Helper()
	anchor, err := workspace.NewControlPlaneTrustAnchor(keyID, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := workspace.NewProtectedControlPlaneKeyPolicy([]workspace.ProtectedControlPlaneKeyRule{{
		Kind: kind, KeyIDs: []workspace.ID{keyID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := workspace.NewEd25519ControlPlaneVerifier(
		adapterID, policy,
		&controlPlaneTestAnchors{anchors: map[string]workspace.ControlPlaneTrustAnchor{keyID.String(): anchor}},
		replay, controlPlaneTestClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func ownerControlPlaneBinding(
	t *testing.T,
	generation workspace.Digest,
	base, head workspace.GitObjectID,
	directive workspace.Digest,
) workspace.ControlPlaneBinding {
	t.Helper()
	repository, err := workspace.NewRepositoryIdentity("https://github.com/example/repository.git")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.NewControlPlaneBinding(workspace.ControlPlaneBindingOptions{
		Kind: workspace.ControlPlaneReceiptOwnerDecision, WorkspaceID: workspace.MustID("workspace-one"),
		Generation: generation, RequestDigest: workspace.DigestBytes([]byte("request")), DirectiveDigest: directive,
		Repository: repository, Remote: "origin", Base: base, Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func signedControlPlaneReceipt(
	t *testing.T,
	envelope workspace.ControlPlaneEnvelopeV2,
	privateKey ed25519.PrivateKey,
) workspace.ControlPlaneReceiptV2 {
	t.Helper()
	signature := ed25519.Sign(privateKey, envelope.PayloadDigest().Bytes())
	receipt, err := workspace.NewControlPlaneReceiptV2(envelope, signature)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func privateKeySeedMarker(privateKey ed25519.PrivateKey) string {
	// The raw key is binary; this marker is intentionally just a stable slice
	// unlikely to occur in canonical JSON. The structural guarantee is that the
	// receipt wire has no private-key field.
	return string(privateKey.Seed()[:8])
}

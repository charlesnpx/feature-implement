package workspacecmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestDurableReplayStorePublishesOnceAndAcceptsExactRetry(t *testing.T) {
	store := durableReplayStore{runtimeDirectory: filepath.Join(t.TempDir(), "runtime")}
	claim := testControlPlaneReplayClaim(t, "same-nonce", "2026-07-23T12:00:00Z", 1)

	const workers = 8
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errors <- store.ClaimControlPlaneNonce(context.Background(), claim)
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("exact concurrent replay claim: %v", err)
		}
	}
	if err := store.ClaimControlPlaneNonce(context.Background(), claim); err != nil {
		t.Fatalf("exact crash retry: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(
		workspace.WorkspaceStateDirectory(store.runtimeDirectory),
		"control-plane-replay",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("durable replay entries = %#v", entries)
	}
}

func TestDurableReplayStoreRejectsNonceBoundToDifferentTuple(t *testing.T) {
	store := durableReplayStore{runtimeDirectory: filepath.Join(t.TempDir(), "runtime")}
	first := testControlPlaneReplayClaim(t, "bound-nonce", "2026-07-23T12:00:00Z", 2)
	different := testControlPlaneReplayClaim(t, "bound-nonce", "2026-07-23T13:00:00Z", 3)
	if err := store.ClaimControlPlaneNonce(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimControlPlaneNonce(context.Background(), different); err == nil ||
		!strings.Contains(err.Error(), "different or invalid evidence") {
		t.Fatalf("different replay tuple error = %v", err)
	}
}

func testControlPlaneReplayClaim(
	t *testing.T,
	nonce, expiry string,
	signatureByte byte,
) workspace.ControlPlaneReplayClaim {
	t.Helper()
	repository, err := workspace.NewRepositoryIdentity("https://github.com/example/project.git")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.NewControlPlaneBinding(workspace.ControlPlaneBindingOptions{
		Kind: workspace.ControlPlaneReceiptOwnerDecision, WorkspaceID: workspace.MustID("workspace-one"),
		Generation: workspace.DigestBytes([]byte("generation")), RequestDigest: workspace.DigestBytes([]byte("request")),
		DirectiveDigest: workspace.DigestBytes([]byte("directive")), Repository: repository, Remote: "origin",
		Base: testGitObject(t, "1"), Head: testGitObject(t, "2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := workspace.NewControlPlaneEnvelopeV2(
		binding, workspace.MustID("owner-key"), nonce, expiresAt, workspace.MustID("coordinator-v2"),
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := workspace.NewControlPlaneReceiptV2(
		envelope, bytes.Repeat([]byte{signatureByte}, ed25519.SignatureSize),
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := workspace.NewControlPlaneReplayClaim(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

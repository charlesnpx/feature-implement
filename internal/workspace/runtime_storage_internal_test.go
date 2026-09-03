package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeStorageCreatesV7MarkerAndRejectsLegacyWithoutMutation(t *testing.T) {
	parent := canonicalRuntimeTestTempDir(t)
	legacy := filepath.Join(parent, "legacy")
	if err := os.MkdirAll(filepath.Join(legacy, WorkspaceStateDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyJournal := filepath.Join(
		legacy,
		WorkspaceStateDirectoryName,
		"journal.v2.jsonl",
	)
	legacyContent := []byte("provider-oriented-v2-state\n")
	if err := os.WriteFile(legacyJournal, legacyContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntimeStorage(legacy, true); err == nil ||
		err.Error() != "runtime format is incompatible; regenerate from committed sources" {
		t.Fatalf("legacy runtime error = %v", err)
	}
	if content, err := os.ReadFile(legacyJournal); err != nil || !bytes.Equal(content, legacyContent) {
		t.Fatalf("legacy runtime changed: %q, %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(legacy, RuntimeFormatFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy runtime acquired v8 marker: %v", err)
	}

	fresh := filepath.Join(parent, "fresh")
	storage, err := OpenRuntimeStorage(fresh, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		filepath.Join(fresh, RuntimeFormatFileName),
		filepath.Join(fresh, WorkspaceStateDirectoryName),
	} {
		if info, err := os.Lstat(candidate); err != nil {
			t.Fatalf("runtime path %s = %v, %v", candidate, info, err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(fresh, RuntimeFormatFileName))
	if err != nil {
		t.Fatalf("read v8 runtime marker: %v", err)
	}
	var wire runtimeFormatMarkerWire
	if err := json.Unmarshal(marker, &wire); err != nil {
		t.Fatalf("decode v8 runtime marker: %v", err)
	}
	if wire.SchemaVersion != RuntimeFormatSchemaVersion {
		t.Fatalf(
			"runtime marker schema version = %d, want %d",
			wire.SchemaVersion,
			RuntimeFormatSchemaVersion,
		)
	}
	if _, err := os.Lstat(filepath.Join(fresh, WorkspaceStateDirectoryName, "runtime-state.identity.v3.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh runtime acquired removed identity marker: %v", err)
	}
}

func TestRuntimeStorageRejectsV4MarkerAtFormatGate(t *testing.T) {
	runtimePath := filepath.Join(canonicalRuntimeTestTempDir(t), "runtime")
	if err := os.MkdirAll(filepath.Join(runtimePath, WorkspaceStateDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(runtimeFormatMarkerWire{
		SchemaVersion: 4,
		Kind:          localRuntimeFormatKind,
		StateRoot:     WorkspaceStateDirectoryName,
		Capabilities:  append([]string(nil), requiredRuntimeCapabilities...),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimePath, "feature.runtime.v4.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRuntimeStorage(runtimePath, false); err == nil ||
		err.Error() != "runtime format is incompatible; regenerate from committed sources" {
		t.Fatalf("v4 runtime format gate error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runtimePath, RuntimeFormatFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v4 runtime acquired v8 marker: %v", err)
	}
}

func TestRuntimeStorageFailsClosedOnOpenRootAndStateReplacement(t *testing.T) {
	parent := canonicalRuntimeTestTempDir(t)

	t.Run("runtime root", func(t *testing.T) {
		runtimePath := filepath.Join(parent, "root-replacement")
		storage, err := OpenRuntimeStorage(runtimePath, true)
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		moved := runtimePath + "-moved"
		if err := os.Rename(runtimePath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(runtimePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := storage.Verify(); err == nil || !strings.Contains(err.Error(), "was replaced") {
			t.Fatalf("runtime root replacement error = %v", err)
		}
	})

	t.Run("state root", func(t *testing.T) {
		runtimePath := filepath.Join(parent, "state-replacement")
		storage, err := OpenRuntimeStorage(runtimePath, true)
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		state := filepath.Join(runtimePath, WorkspaceStateDirectoryName)
		moved := state + "-moved"
		if err := os.Rename(state, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := storage.Verify(); err == nil || !strings.Contains(err.Error(), "was replaced") {
			t.Fatalf("runtime state replacement error = %v", err)
		}
	})
}

func TestRuntimeInitializationRejectsUnknownNonEmptyState(t *testing.T) {
	runtimePath := filepath.Join(canonicalRuntimeTestTempDir(t), "runtime")
	statePath := filepath.Join(runtimePath, WorkspaceStateDirectoryName)
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statePath, "unknown"), []byte("debris"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntimeStorage(runtimePath, true); err == nil ||
		err.Error() != "runtime format is incompatible; regenerate from committed sources" {
		t.Fatalf("unknown state error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runtimePath, RuntimeFormatFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown state acquired v8 marker: %v", err)
	}
}

func TestV7RuntimeMarkerRefusesAdmissionWithoutMutation(t *testing.T) {
	runtimePath := filepath.Join(canonicalRuntimeTestTempDir(t), "v7-runtime")
	v7MarkerName := "feature.runtime.v7.json"
	v7Marker := []byte("v7 incompatible runtime marker\n")
	if err := os.MkdirAll(runtimePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimePath, v7MarkerName), v7Marker, 0o600); err != nil {
		t.Fatal(err)
	}
	definition := EffectiveWorkspaceDefinition{
		workspace: WorkspaceManifest{target: LocalTarget{
			root: canonicalRuntimeTestTempDir(t),
		}},
		generation: DigestBytes([]byte("v7 incompatible runtime generation")),
	}
	before := snapshotRuntimeTree(t, runtimePath)

	if _, err := ValidateLocalTargetForWorkspaceRuntime(
		context.Background(), runtimePath, definition,
	); err == nil || err.Error() != "runtime format is incompatible; regenerate from committed sources" {
		t.Fatalf("v7 runtime validation error = %v", err)
	}
	assertRuntimeTreeUnchanged(t, runtimePath, before)

	if _, err := InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimePath,
		definition,
		time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC),
		WorkspaceInitializationOptions{},
	); err == nil || err.Error() != "runtime format is incompatible; regenerate from committed sources" {
		t.Fatalf("v7 runtime initialization error = %v", err)
	}
	assertRuntimeTreeUnchanged(t, runtimePath, before)
	if content, err := os.ReadFile(filepath.Join(runtimePath, v7MarkerName)); err != nil || !bytes.Equal(content, v7Marker) {
		t.Fatalf("v7 marker changed: %q, %v", content, err)
	}
	for _, path := range []string{
		RuntimeFormatFileName,
		RuntimeInitializationLockName,
	} {
		if _, err := os.Lstat(filepath.Join(runtimePath, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("v7 runtime acquired v8 artifact %s: %v", path, err)
		}
	}
}

type runtimeTreeEntry struct {
	mode    fs.FileMode
	content []byte
}

func snapshotRuntimeTree(t *testing.T, root string) map[string]runtimeTreeEntry {
	t.Helper()
	entries := make(map[string]runtimeTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot := runtimeTreeEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			snapshot.content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		entries[filepath.ToSlash(relative)] = snapshot
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertRuntimeTreeUnchanged(t *testing.T, root string, before map[string]runtimeTreeEntry) {
	t.Helper()
	after := snapshotRuntimeTree(t, root)
	if len(before) != len(after) {
		t.Fatalf("runtime tree entry count changed: before=%d after=%d", len(before), len(after))
	}
	for path, expected := range before {
		actual, exists := after[path]
		if !exists || actual.mode != expected.mode || !bytes.Equal(actual.content, expected.content) {
			t.Fatalf("runtime path changed: %s before=%#v after=%#v exists=%t", path, expected, actual, exists)
		}
	}
}

func TestConcurrentRuntimeInitializationPublishesOneV7Runtime(t *testing.T) {
	runtimePath := filepath.Join(canonicalRuntimeTestTempDir(t), "runtime")
	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			storage, err := OpenRuntimeStorage(runtimePath, true)
			if err != nil {
				results <- err
				return
			}
			if err := storage.Verify(); err != nil {
				_ = storage.Close()
				results <- err
				return
			}
			results <- storage.Close()
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent runtime initialization: %v", err)
		}
	}
	storage, err := OpenRuntimeStorage(runtimePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceablePublicationRecoversEveryDurableBoundary(t *testing.T) {
	for _, point := range []PublicationFaultPoint{
		PublicationFaultAfterIntent,
		PublicationFaultAfterPending,
		PublicationFaultAfterQuarantine,
		PublicationFaultAfterPublish,
	} {
		t.Run(string(point), func(t *testing.T) {
			rootPath := filepath.Join(canonicalRuntimeTestTempDir(t), "root")
			root, err := OpenVerifiedRoot(RootRoleRuntime, rootPath, true)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			target := "projection.json"
			oldContent := []byte("old projection\n")
			newContent := []byte("new projection\n")
			if err := root.PublishReplaceable(
				target, oldContent, 0o600, 1024, PublicationOptions{},
			); err != nil {
				t.Fatal(err)
			}
			err = root.PublishReplaceable(
				target,
				newContent,
				0o600,
				1024,
				PublicationOptions{FaultInjector: func(observed PublicationFaultPoint) error {
					if observed == point {
						return errors.New("simulated crash")
					}
					return nil
				}},
			)
			if err == nil || !strings.Contains(err.Error(), "simulated crash") {
				t.Fatalf("publication fault = %v", err)
			}
			stable, err := root.ReadReplaceable(target, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if point == PublicationFaultAfterIntent {
				if string(stable) != string(oldContent) {
					t.Fatalf("intent-only recovery = %q", stable)
				}
			} else if string(stable) != string(newContent) {
				t.Fatalf("completed recovery = %q", stable)
			}
			if err := root.PublishReplaceable(
				target, newContent, 0o600, 1024, PublicationOptions{},
			); err != nil {
				t.Fatal(err)
			}
			entries, err := root.adapter.readDirectory(".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.name, "runtime-publication-") {
					t.Fatalf("publication recovery left control file %s", entry.name)
				}
			}
		})
	}
}

func TestReplaceablePublicationRaceLeavesOneCompleteStableObject(t *testing.T) {
	rootPath := filepath.Join(canonicalRuntimeTestTempDir(t), "root")
	root, err := OpenVerifiedRoot(RootRoleRuntime, rootPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.PublishReplaceable(
		"projection.json",
		[]byte("initial\n"),
		0o600,
		1024,
		PublicationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	candidates := [][]byte{[]byte("candidate-a\n"), []byte("candidate-b\n")}
	start := make(chan struct{})
	results := make(chan error, len(candidates))
	var group sync.WaitGroup
	for _, candidate := range candidates {
		candidate := append([]byte(nil), candidate...)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- root.PublishReplaceable(
				"projection.json",
				candidate,
				0o600,
				1024,
				PublicationOptions{},
			)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatal("concurrent publications produced no stable winner")
	}
	content, err := root.ReadReplaceable("projection.json", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(candidates[0]) && string(content) != string(candidates[1]) {
		t.Fatalf("concurrent publication content = %q", content)
	}
}

func TestWorkspaceJournalDetectsLockPathReplacement(t *testing.T) {
	runtimePath := filepath.Join(canonicalRuntimeTestTempDir(t), "runtime")
	journal, err := OpenWorkspaceJournal(runtimePath, JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	lockPath := WorkspaceJournalLockPath(runtimePath)
	if err := os.Rename(lockPath, lockPath+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ReadSnapshot(); err == nil ||
		!strings.Contains(err.Error(), "lock was replaced") {
		t.Fatalf("lock replacement error = %v", err)
	}
}

func canonicalRuntimeTestTempDir(t *testing.T) string {
	t.Helper()
	canonical, err := canonicalizeTrustedRootPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const aggregateGitStreamTestRecords = 74000

func TestInspectRawTreeEntriesStreamsPastAggregateOutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	root := t.TempDir()
	suffix := strings.Repeat("x", 100)
	script := writeAggregateGitStreamScript(
		t, root,
		fmt.Sprintf(
			`100644 blob %s\tpath-%%06d-%s%%c`,
			strings.Repeat("a", 40), suffix,
		),
	)
	git, err := NewLocalAttemptGitAdapter(script, nil)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := ParseGitObjectID("sha1:" + strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (LocalCommitGitAdapter{git: git}).inspectRawTreeEntries(
		t.Context(), root, tree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != aggregateGitStreamTestRecords {
		t.Fatalf(
			"streamed tree entries = %d, want %d",
			len(entries), aggregateGitStreamTestRecords,
		)
	}
}

func TestHiddenIndexInspectionStreamsPastAggregateOutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	root := t.TempDir()
	suffix := strings.Repeat("x", 100)
	script := writeAggregateGitStreamScript(
		t, root,
		fmt.Sprintf(`H path-%%06d-%s%%c`, suffix),
	)
	git, err := NewLocalAttemptGitAdapter(script, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (LocalCommitGitAdapter{git: git}).rejectHiddenIndexEntries(
		t.Context(), root,
	); err != nil {
		t.Fatal(err)
	}
}

func writeAggregateGitStreamScript(
	t *testing.T,
	root string,
	recordFormat string,
) string {
	t.Helper()
	script := filepath.Join(root, "git-stream")
	content := fmt.Sprintf(
		"#!/bin/sh\nawk 'BEGIN { for (i = 0; i < %d; i++) printf \"%s\", i, 0 }'\n",
		aggregateGitStreamTestRecords, recordFormat,
	)
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

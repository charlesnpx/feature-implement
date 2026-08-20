package workspace

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateAttemptTreeEntriesAcceptsSupportedModes(t *testing.T) {
	directories, err := validateAttemptTreeEntries([]rawGitTreeEntry{
		{path: "README.md", mode: GitModeRegular},
		{path: "bin/run", mode: GitModeExecutable},
		{path: "links/run", mode: GitModeSymlink},
		{path: "modules/tool", mode: GitModeSubmodule, kind: "commit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bin", "links", "modules"}; !slices.Equal(directories, want) {
		t.Fatalf("attempt tree directories = %q, want %q", directories, want)
	}
}

func TestValidateAttemptTreeEntriesRejectsUnsupportedAndCollidingPaths(t *testing.T) {
	tests := []struct {
		name    string
		entries []rawGitTreeEntry
		want    string
	}{
		{
			name: "gitlink with blob type",
			entries: []rawGitTreeEntry{
				{path: "modules/tool", mode: GitModeSubmodule, kind: "blob"},
			},
			want: "inconsistent gitlink type",
		},
		{
			name: "Git administration",
			entries: []rawGitTreeEntry{
				{path: "nested/.GIT/config", mode: GitModeRegular},
			},
			want: "collides with Git administration",
		},
		{
			name: "case collision",
			entries: []rawGitTreeEntry{
				{path: "README.md", mode: GitModeRegular},
				{path: "readme.md", mode: GitModeRegular},
			},
			want: "collide",
		},
		{
			name: "Unicode normalization collision",
			entries: []rawGitTreeEntry{
				{path: "docs/\u00e9.txt", mode: GitModeRegular},
				{path: "docs/e\u0301.txt", mode: GitModeRegular},
			},
			want: "collide",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateAttemptTreeEntries(test.entries)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate attempt tree entries error = %v", err)
			}
		})
	}
}

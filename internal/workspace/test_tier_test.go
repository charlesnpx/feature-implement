package workspace_test

import "testing"

const fullSuiteSkipPrefix = "full suite only: "

func requireFullSuite(t *testing.T, coverage string) {
	t.Helper()
	if testing.Short() {
		t.Skip(fullSuiteSkipPrefix + coverage)
	}
}

func requireFullSuiteCase(t *testing.T, keepInShort bool, coverage string) {
	t.Helper()
	if !keepInShort {
		requireFullSuite(t, coverage)
	}
}

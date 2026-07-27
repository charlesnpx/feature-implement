package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishedPublicContractIncludesLicenseOperationsAndNotices(t *testing.T) {
	root := repositoryRoot(t)
	readText := func(relative string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(content)
	}

	license := readText("LICENSE")
	for _, required := range []string{
		"MIT License",
		"Copyright (c) 2026 Charles Anderson",
		"Permission is hereby granted, free of charge",
	} {
		if !strings.Contains(license, required) {
			t.Fatalf("LICENSE omits %q", required)
		}
	}

	readme := readText("README.md")
	for _, required := range []string{
		"## Public contract",
		"### Operations and migration",
		"### Supported repository profile",
		"### Stable-base policy",
		"### Threat model",
		"### Deferred GitHub design",
		"### License and third-party notices",
		"local completion is not an external attestation",
		"runtime without the local v3 marker must",
		"be regenerated from the locked bundle",
		"The target must be a local non-bare Git repository",
	} {
		if !strings.Contains(readme, required) {
			t.Fatalf("README public contract omits %q", required)
		}
	}

	notices := readText("THIRD_PARTY_NOTICES.md")
	for _, required := range []string{
		"does not vendor third-party source or assets",
		"golang.org/x/sys",
		"golang.org/x/text",
		"gopkg.in/yaml.v3",
		"Copyright 2011-2016 Canonical Ltd.",
	} {
		if !strings.Contains(notices, required) {
			t.Fatalf("THIRD_PARTY_NOTICES.md omits %q", required)
		}
	}
	for _, directory := range []string{"vendor", "third_party", "third-party"} {
		if _, err := os.Stat(filepath.Join(root, directory)); !os.IsNotExist(err) {
			t.Fatalf("unexpected vendored material directory %s: %v", directory, err)
		}
	}
}

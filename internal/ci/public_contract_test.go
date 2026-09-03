package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
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
		"Local completion is not",
		"runtime without the local v8 marker must",
		"committed plan and current lock",
		"The target must be a primary local non-bare Git worktree",
		"pinned base commit object to remain present; the base ref's movement or deletion",
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
		"github.com/charlesnpx/witness",
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

func TestREADMEExecutionConfigurationSamplesDecode(t *testing.T) {
	for _, sample := range executionConfigurationSamples(
		t,
		string(readRepositoryFile(t, "README.md")),
	) {
		if _, err := workspace.DecodeExecutionConfig([]byte(sample.source)); err != nil {
			t.Errorf(
				"README execution-configuration YAML sample starting at line %d no longer parses: %v",
				sample.startLine,
				err,
			)
		}
	}
}

func TestREADMEPlanSourceSamplesDecode(t *testing.T) {
	for _, sample := range planSourceSamples(t, string(readRepositoryFile(t, "README.md"))) {
		if _, err := workspace.DecodePlan([]byte(sample.source)); err != nil {
			t.Errorf(
				"README plan-source YAML sample starting at line %d no longer parses: %v",
				sample.startLine,
				err,
			)
		}
	}
}

func TestREADMEExecutionConfigurationSampleGuardRejectsLegacyBoundaryMode(t *testing.T) {
	readme := string(readRepositoryFile(t, "README.md"))
	invalidReadme := strings.Replace(readme, "checkpoint: pause_only", "mode: pause_only", 1)
	if invalidReadme == readme {
		t.Fatal("README fixture is missing the checkpoint: pause_only sample value")
	}

	for _, sample := range executionConfigurationSamples(t, invalidReadme) {
		if _, err := workspace.DecodeExecutionConfig([]byte(sample.source)); err != nil {
			t.Logf("in-memory invalid README execution-configuration sample was rejected: %v", err)
			return
		}
	}
	t.Fatal("README execution-configuration sample guard accepted an in-memory legacy boundary mode")
}

func TestREADMEExecutionConfigurationSampleGuardRejectsMisspelledBoundary(t *testing.T) {
	readme := string(readRepositoryFile(t, "README.md"))
	invalidReadme := strings.ReplaceAll(readme, "\n    boundary:", "\n    boundry:")
	if invalidReadme == readme {
		t.Fatal("README fixture is missing the boundary mappings in the execution-configuration sample")
	}

	for _, sample := range executionConfigurationSamples(t, invalidReadme) {
		if !strings.Contains(sample.source, "boundry:") {
			continue
		}
		if _, err := workspace.DecodeExecutionConfig([]byte(sample.source)); err != nil {
			t.Logf("in-memory invalid README execution-configuration sample was rejected: %v", err)
			return
		}
		t.Fatal("README execution-configuration sample guard accepted an in-memory misspelled boundary")
	}
	t.Fatal("README execution-configuration sample guard skipped an in-memory misspelled boundary")
}

type readmeYAMLBlock struct {
	startLine int
	source    string
}

func executionConfigurationSamples(t *testing.T, readme string) []readmeYAMLBlock {
	t.Helper()
	var samples []readmeYAMLBlock
	for _, block := range readmeYAMLBlocks(t, readme) {
		if hasYAMLScalar(block.source, "schema_version", "2") &&
			hasYAMLMapping(block.source, "merge_units") &&
			(hasYAMLMapping(block.source, "policy") || hasYAMLMapping(block.source, "profiles")) {
			samples = append(samples, block)
		}
	}
	if len(samples) == 0 {
		t.Fatal("README has no fenced execution-configuration YAML sample with schema_version, merge_units, and a policy or profiles mapping")
	}
	return samples
}

func planSourceSamples(t *testing.T, readme string) []readmeYAMLBlock {
	t.Helper()
	var samples []readmeYAMLBlock
	for _, block := range readmeYAMLBlocks(t, readme) {
		if hasYAMLScalar(block.source, "schema_version", "2") &&
			hasYAMLMapping(block.source, "stories") &&
			hasYAMLMapping(block.source, "merge_units") &&
			!hasYAMLMapping(block.source, "policy") {
			samples = append(samples, block)
		}
	}
	if len(samples) == 0 {
		t.Fatal("README has no fenced plan-source YAML sample with stories and merge_units but no policy mapping")
	}
	return samples
}

func readmeYAMLBlocks(t *testing.T, readme string) []readmeYAMLBlock {
	t.Helper()
	var blocks []readmeYAMLBlock
	var source []string
	inYAMLBlock := false
	fenceLine := 0
	for index, line := range strings.Split(readme, "\n") {
		if !inYAMLBlock {
			if strings.TrimSpace(line) == "```yaml" {
				inYAMLBlock = true
				fenceLine = index + 1
				source = nil
			}
			continue
		}
		if strings.TrimSpace(line) == "```" {
			blocks = append(blocks, readmeYAMLBlock{
				startLine: fenceLine + 1,
				source:    strings.Join(source, "\n"),
			})
			inYAMLBlock = false
			continue
		}
		source = append(source, line)
	}
	if inYAMLBlock {
		t.Fatalf("README has an unterminated yaml code fence beginning at line %d", fenceLine)
	}
	return blocks
}

func hasYAMLScalar(source, field, value string) bool {
	for _, line := range strings.Split(source, "\n") {
		content, _, _ := strings.Cut(strings.TrimSpace(line), "#")
		if strings.TrimSpace(content) == field+": "+value {
			return true
		}
	}
	return false
}

func hasYAMLMapping(source, field string) bool {
	for _, line := range strings.Split(source, "\n") {
		content, _, _ := strings.Cut(strings.TrimSpace(line), "#")
		if strings.TrimSpace(content) == field+":" {
			return true
		}
	}
	return false
}

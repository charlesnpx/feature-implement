package workspace

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// LocalProviderCompletionGitAdapter is the credential-scrubbed, read-only Git
// verifier used after provider merge. It deliberately reuses the local Git
// runtime rather than the credential-bearing provider adapter and exposes no
// mutation method.
type LocalProviderCompletionGitAdapter struct {
	git LocalAttemptGitAdapter
}

func NewLocalProviderCompletionGitAdapter(
	executable string,
	environment []EnvironmentVariable,
) (LocalProviderCompletionGitAdapter, error) {
	git, err := NewLocalAttemptGitAdapter(executable, environment)
	if err != nil {
		return LocalProviderCompletionGitAdapter{}, err
	}
	return LocalProviderCompletionGitAdapter{git: git}, nil
}

func DefaultLocalProviderCompletionGitAdapter() LocalProviderCompletionGitAdapter {
	adapter, _ := NewLocalProviderCompletionGitAdapter("git", nil)
	return adapter
}

func (adapter LocalProviderCompletionGitAdapter) InspectRemoteBranch(
	ctx context.Context,
	repositoryRoot, remote, branch string,
) (GitObjectID, error) {
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return GitObjectID{}, fmt.Errorf("inspect provider remote branch: %w", err)
	}
	return adapter.inspectRemoteRef(ctx, repositoryRoot, remote, branch)
}

func (adapter LocalProviderCompletionGitAdapter) InspectRemoteBase(
	ctx context.Context,
	repositoryRoot, remote, baseRef string,
) (GitObjectID, error) {
	baseRef = strings.TrimSpace(baseRef)
	if err := validateBoundedText("provider completion base ref", baseRef, 1024); err != nil {
		return GitObjectID{}, err
	}
	if strings.ContainsAny(baseRef, "\t\r\n ") {
		return GitObjectID{}, fmt.Errorf("provider completion base ref must be a single token")
	}
	_, exitCode, err := adapter.git.run(ctx, repositoryRoot, "check-ref-format", "--branch", baseRef)
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError("validate provider completion base ref", exitCode, err)
	}
	return adapter.inspectRemoteRef(ctx, repositoryRoot, remote, baseRef)
}

func (adapter LocalProviderCompletionGitAdapter) inspectRemoteRef(
	ctx context.Context,
	repositoryRoot, remote, ref string,
) (GitObjectID, error) {
	remote = strings.TrimSpace(remote)
	if err := validateBoundedText("provider completion remote", remote, 512); err != nil {
		return GitObjectID{}, err
	}
	if strings.ContainsAny(remote, "\t\r\n ") || strings.HasPrefix(remote, "-") {
		return GitObjectID{}, fmt.Errorf("provider completion remote must be a non-option token")
	}
	algorithm, err := adapter.git.objectFormat(ctx, repositoryRoot)
	if err != nil {
		return GitObjectID{}, err
	}
	if err := adapter.validateCredentialFreeRemote(ctx, repositoryRoot, remote); err != nil {
		return GitObjectID{}, err
	}
	expectedRef := "refs/heads/" + ref
	output, exitCode, err := adapter.git.run(
		ctx, repositoryRoot, "ls-remote", "--exit-code", "--heads", "--refs", "--", remote, expectedRef,
	)
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError("inspect provider remote ref", exitCode, err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 1 {
		return GitObjectID{}, fmt.Errorf("provider remote ref %s did not resolve exactly once", expectedRef)
	}
	objectText, observedRef, found := strings.Cut(lines[0], "\t")
	if !found || observedRef != expectedRef || strings.Contains(objectText, " ") {
		return GitObjectID{}, fmt.Errorf("provider remote ref %s returned malformed identity", expectedRef)
	}
	return qualifyGitObjectID(algorithm, objectText)
}

func (adapter LocalProviderCompletionGitAdapter) validateCredentialFreeRemote(
	ctx context.Context,
	repositoryRoot, remote string,
) error {
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, "remote", "get-url", "--all", remote)
	if err != nil || exitCode != 0 {
		return gitExitError("inspect provider remote URL", exitCode, err)
	}
	urls := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(urls) == 0 || urls[0] == "" {
		return fmt.Errorf("provider completion remote has no URL")
	}
	for _, remoteURL := range urls {
		if !strings.Contains(remoteURL, "://") {
			continue
		}
		parsed, parseErr := url.Parse(remoteURL)
		if parseErr != nil {
			return fmt.Errorf("parse provider remote URL: %w", parseErr)
		}
		if parsed.User != nil {
			return fmt.Errorf("provider completion remote URL cannot contain embedded credentials")
		}
	}
	return nil
}

func (adapter LocalProviderCompletionGitAdapter) InspectCommit(
	ctx context.Context,
	repositoryRoot string,
	object GitObjectID,
) (ProviderGitCommit, error) {
	if object.IsZero() {
		return ProviderGitCommit{}, fmt.Errorf("provider completion commit identity is required")
	}
	algorithm, err := adapter.git.objectFormat(ctx, repositoryRoot)
	if err != nil {
		return ProviderGitCommit{}, err
	}
	if object.Algorithm() != algorithm {
		return ProviderGitCommit{}, fmt.Errorf("provider completion commit uses a different repository object format")
	}
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, "cat-file", "commit", objectHex(object))
	if err != nil || exitCode != 0 {
		return ProviderGitCommit{}, gitExitError("inspect provider completion commit", exitCode, err)
	}
	var tree GitObjectID
	parents := make([]GitObjectID, 0, 2)
	for _, line := range strings.Split(string(output), "\n") {
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch name {
		case "tree":
			if !tree.IsZero() {
				return ProviderGitCommit{}, fmt.Errorf("provider completion commit has multiple tree headers")
			}
			tree, err = qualifyGitObjectID(algorithm, value)
		case "parent":
			var parent GitObjectID
			parent, err = qualifyGitObjectID(algorithm, value)
			if err == nil {
				parents = append(parents, parent)
			}
		}
		if err != nil {
			return ProviderGitCommit{}, fmt.Errorf("parse provider completion commit: %w", err)
		}
	}
	if tree.IsZero() {
		return ProviderGitCommit{}, fmt.Errorf("provider completion commit has no tree header")
	}
	return NewProviderGitCommit(object, tree, parents)
}

func (adapter LocalProviderCompletionGitAdapter) IsAncestor(
	ctx context.Context,
	repositoryRoot string,
	ancestor, descendant GitObjectID,
) (bool, error) {
	if ancestor.IsZero() || descendant.IsZero() || ancestor.Algorithm() != descendant.Algorithm() {
		return false, fmt.Errorf("provider ancestry requires matching Git object identities")
	}
	algorithm, err := adapter.git.objectFormat(ctx, repositoryRoot)
	if err != nil {
		return false, err
	}
	if ancestor.Algorithm() != algorithm {
		return false, fmt.Errorf("provider ancestry uses a different repository object format")
	}
	_, exitCode, err := adapter.git.run(
		ctx, repositoryRoot, "merge-base", "--is-ancestor", objectHex(ancestor), objectHex(descendant),
	)
	if err != nil {
		return false, err
	}
	switch exitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("inspect provider ancestry: Git exited with status %d", exitCode)
	}
}

var _ ProviderCompletionGitPort = LocalProviderCompletionGitAdapter{}

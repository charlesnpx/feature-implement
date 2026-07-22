package workspace

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

func (adapter LocalProviderCompletionGitAdapter) InspectRemoteTopology(
	ctx context.Context,
	repositoryRoot, remote, branch, baseRef string,
	expectedHead, expectedMerge, expectedBase GitObjectID,
) (ProviderCompletionGitInspection, error) {
	if expectedHead.IsZero() || expectedMerge.IsZero() || expectedBase.IsZero() ||
		expectedHead.Algorithm() != expectedMerge.Algorithm() ||
		expectedHead.Algorithm() != expectedBase.Algorithm() {
		return ProviderCompletionGitInspection{}, fmt.Errorf("provider completion topology requires matching expected Git identities")
	}
	remoteBranch, err := adapter.InspectRemoteBranch(ctx, repositoryRoot, remote, branch)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	remoteBase, err := adapter.InspectRemoteBase(ctx, repositoryRoot, remote, baseRef)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	if remoteBranch != expectedHead || remoteBase != expectedMerge {
		return ProviderCompletionGitInspection{}, fmt.Errorf("provider remote refs do not match the expected reviewed head and merge")
	}
	algorithm, err := adapter.git.objectFormat(ctx, repositoryRoot)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	if algorithm != expectedHead.Algorithm() {
		return ProviderCompletionGitInspection{}, fmt.Errorf("provider completion objects use a different repository object format")
	}
	remoteURL, err := adapter.credentialFreeRemoteURL(ctx, repositoryRoot, remote)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	objectStore, err := os.MkdirTemp("", "feature-provider-completion-")
	if err != nil {
		return ProviderCompletionGitInspection{}, fmt.Errorf("create isolated provider object store: %w", err)
	}
	defer func() { _ = os.RemoveAll(objectStore) }()
	_, exitCode, err := adapter.git.run(
		ctx, objectStore, "init", "--bare", "--object-format="+string(algorithm),
	)
	if err != nil || exitCode != 0 {
		return ProviderCompletionGitInspection{}, gitExitError("initialize isolated provider object store", exitCode, err)
	}
	_, exitCode, err = adapter.runCredentialFreeRemoteGit(
		ctx, objectStore, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--force", "--",
		remoteURL, "refs/heads/"+branch, "refs/heads/"+baseRef,
	)
	if err != nil || exitCode != 0 {
		return ProviderCompletionGitInspection{}, gitExitError("fetch isolated provider completion objects", exitCode, err)
	}
	refs, exitCode, err := adapter.git.run(ctx, objectStore, "for-each-ref", "--format=%(refname)")
	if err != nil || exitCode != 0 {
		return ProviderCompletionGitInspection{}, gitExitError("inspect isolated provider refs", exitCode, err)
	}
	if strings.TrimSpace(string(refs)) != "" {
		return ProviderCompletionGitInspection{}, fmt.Errorf("isolated provider fetch unexpectedly moved a ref")
	}
	remoteBranchAfter, err := adapter.InspectRemoteBranch(ctx, repositoryRoot, remote, branch)
	if err != nil || remoteBranchAfter != remoteBranch {
		return ProviderCompletionGitInspection{}, fmt.Errorf("provider remote branch changed during isolated inspection")
	}
	remoteBaseAfter, err := adapter.InspectRemoteBase(ctx, repositoryRoot, remote, baseRef)
	if err != nil || remoteBaseAfter != remoteBase {
		return ProviderCompletionGitInspection{}, fmt.Errorf("provider remote base changed during isolated inspection")
	}
	headCommit, err := adapter.InspectCommit(ctx, objectStore, expectedHead)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	mergeCommit, err := adapter.InspectCommit(ctx, objectStore, expectedMerge)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	baseAncestor, err := adapter.IsAncestor(ctx, objectStore, expectedBase, expectedMerge)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	headAncestor, err := adapter.IsAncestor(ctx, objectStore, expectedHead, expectedMerge)
	if err != nil {
		return ProviderCompletionGitInspection{}, err
	}
	return NewProviderCompletionGitInspection(
		remoteBranch, remoteBase, headCommit, mergeCommit, baseAncestor, headAncestor,
	)
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
	remoteURL, err := adapter.credentialFreeRemoteURL(ctx, repositoryRoot, remote)
	if err != nil {
		return GitObjectID{}, err
	}
	expectedRef := "refs/heads/" + ref
	isolatedRepository, err := os.MkdirTemp("", "feature-provider-remote-")
	if err != nil {
		return GitObjectID{}, fmt.Errorf("create isolated provider remote inspection: %w", err)
	}
	defer func() { _ = os.RemoveAll(isolatedRepository) }()
	_, exitCode, err := adapter.git.run(ctx, isolatedRepository, "init", "--bare", "--object-format="+string(algorithm))
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError("initialize isolated provider remote inspection", exitCode, err)
	}
	output, exitCode, err := adapter.runCredentialFreeRemoteGit(
		ctx, isolatedRepository, "ls-remote", "--exit-code", "--heads", "--refs", "--", remoteURL, expectedRef,
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

func (adapter LocalProviderCompletionGitAdapter) credentialFreeRemoteURL(
	ctx context.Context,
	repositoryRoot, remote string,
) (string, error) {
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, "remote", "get-url", "--all", remote)
	if err != nil || exitCode != 0 {
		return "", gitExitError("inspect provider fetch URL", exitCode, err)
	}
	urls := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(urls) != 1 || strings.TrimSpace(urls[0]) == "" {
		return "", fmt.Errorf("provider completion remote must resolve to exactly one fetch URL")
	}
	return sanitizeProviderCompletionRemoteURL(strings.TrimSpace(urls[0]))
}

func sanitizeProviderCompletionRemoteURL(remoteURL string) (string, error) {
	if remoteURL == "" || strings.ContainsAny(remoteURL, "\r\n\x00") {
		return "", fmt.Errorf("provider completion remote URL is empty or malformed")
	}
	if !strings.Contains(remoteURL, "://") {
		if !filepath.IsAbs(remoteURL) {
			return "", fmt.Errorf("provider completion remote URL must use an explicit allowed protocol or absolute local path")
		}
		clean := filepath.Clean(remoteURL)
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(clean)}).String(), nil
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return "", fmt.Errorf("parse provider remote URL: %w", err)
	}
	if parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("provider completion remote URL cannot be opaque or contain embedded credentials, query, or fragment data")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.Hostname() == "" {
			return "", fmt.Errorf("provider completion HTTPS remote requires a host")
		}
	case "file":
		if (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) || !filepath.IsAbs(filepath.FromSlash(parsed.Path)) {
			return "", fmt.Errorf("provider completion file remote must be an absolute local path")
		}
	default:
		return "", fmt.Errorf("provider completion remote protocol %q is not allowed", parsed.Scheme)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed.String(), nil
}

func (adapter LocalProviderCompletionGitAdapter) runCredentialFreeRemoteGit(
	ctx context.Context,
	repositoryRoot string,
	arguments ...string,
) ([]byte, int, error) {
	protocols := []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=always",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.http.allow=never",
		"-c", "protocol.ssh.allow=never",
		"-c", "protocol.git.allow=never",
	}
	return adapter.git.run(ctx, repositoryRoot, append(protocols, arguments...)...)
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

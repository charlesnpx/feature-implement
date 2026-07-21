package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// RootedPath is an adapter input that keeps a normalized relative path bound to
// an explicit absolute root.
type RootedPath struct {
	root     string
	relative string
}

func NewRootedPath(root, relative string) (RootedPath, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return RootedPath{}, fmt.Errorf("rooted path requires an absolute root")
	}
	relative, err := normalizeSourcePath(relative)
	if err != nil {
		return RootedPath{}, err
	}
	if !filepath.IsLocal(filepath.FromSlash(relative)) || !isPortableRelativePath(relative) {
		return RootedPath{}, fmt.Errorf("rooted path %q is not a portable relative path", relative)
	}
	return RootedPath{root: root, relative: relative}, nil
}

func (path RootedPath) Root() string     { return path.root }
func (path RootedPath) Relative() string { return path.relative }

func isPortableRelativePath(relative string) bool {
	if strings.Contains(relative, ":") {
		return false
	}
	for _, component := range strings.Split(relative, "/") {
		trimmed := strings.TrimRight(component, ". ")
		if trimmed == "" || trimmed != component {
			return false
		}
		stem, _, _ := strings.Cut(trimmed, ".")
		upper := strings.ToUpper(stem)
		switch upper {
		case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
			return false
		}
		if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
			return false
		}
	}
	return true
}

type FileInfo struct {
	size       int64
	regular    bool
	symlink    bool
	permission uint32
}

func NewFileInfo(size int64, regular, symlink bool, permission uint32) (FileInfo, error) {
	if size < 0 || (regular && symlink) {
		return FileInfo{}, fmt.Errorf("invalid file information")
	}
	return FileInfo{size: size, regular: regular, symlink: symlink, permission: permission}, nil
}

func (info FileInfo) Size() int64        { return info.size }
func (info FileInfo) Regular() bool      { return info.regular }
func (info FileInfo) Symlink() bool      { return info.symlink }
func (info FileInfo) Permission() uint32 { return info.permission }

type GitPort interface {
	ResolveRevision(context.Context, RepositoryIdentity, string) (GitObjectID, error)
	ReadBlob(context.Context, RepositoryIdentity, GitObjectID, string) ([]byte, GitObjectID, error)
}

type FilesystemPort interface {
	ReadFile(context.Context, RootedPath) ([]byte, error)
	Inspect(context.Context, RootedPath) (FileInfo, error)
}

type ProcessResult struct {
	exitCode     int
	stdoutDigest Digest
	stderrDigest Digest
}

func NewProcessResult(exitCode int, stdout, stderr []byte) ProcessResult {
	return ProcessResult{exitCode: exitCode, stdoutDigest: DigestBytes(stdout), stderrDigest: DigestBytes(stderr)}
}

func (result ProcessResult) ExitCode() int        { return result.exitCode }
func (result ProcessResult) StdoutDigest() Digest { return result.stdoutDigest }
func (result ProcessResult) StderrDigest() Digest { return result.stderrDigest }

type ProcessPort interface {
	Run(context.Context, Command) (ProcessResult, error)
}

type ProviderQueryKind uint8

const (
	ProviderQueryRepository ProviderQueryKind = iota + 1
	ProviderQueryPullRequest
	ProviderQueryChecks
)

type ProviderQuery struct {
	kind       ProviderQueryKind
	repository RepositoryIdentity
	identity   string
}

func NewProviderQuery(kind ProviderQueryKind, repository RepositoryIdentity, identity string) (ProviderQuery, error) {
	if kind < ProviderQueryRepository || kind > ProviderQueryChecks || repository.String() == "" {
		return ProviderQuery{}, fmt.Errorf("invalid provider query")
	}
	if kind == ProviderQueryRepository && identity != "" {
		return ProviderQuery{}, fmt.Errorf("repository query cannot carry a secondary identity")
	}
	if kind != ProviderQueryRepository {
		if err := validateBoundedText("provider query identity", identity, 2048); err != nil {
			return ProviderQuery{}, err
		}
	}
	return ProviderQuery{kind: kind, repository: repository, identity: identity}, nil
}

func (query ProviderQuery) Kind() ProviderQueryKind        { return query.kind }
func (query ProviderQuery) Repository() RepositoryIdentity { return query.repository }
func (query ProviderQuery) Identity() string               { return query.identity }

type ProviderObservation struct {
	digest Digest
}

func NewProviderObservation(canonical []byte) ProviderObservation {
	return ProviderObservation{digest: DigestBytes(canonical)}
}

func (observation ProviderObservation) Digest() Digest { return observation.digest }

// ProviderPort is deliberately read-only. A credential-bearing broker and its
// closed dispatch effects are introduced by the provider-lifecycle protocol.
type ProviderPort interface {
	Query(context.Context, ProviderQuery) (ProviderObservation, error)
}

type ClockPort interface {
	Now() time.Time
}

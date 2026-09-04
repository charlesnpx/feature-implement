package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

type FilesystemPort interface {
	ReadFile(context.Context, RootedPath) ([]byte, error)
	Inspect(context.Context, RootedPath) (FileInfo, error)
}

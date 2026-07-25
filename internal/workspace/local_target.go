package workspace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// LocalTargetBinding is the durable admission result for the selected target
// worktree. It binds both semantic Git state and the opened filesystem
// objects that were used to establish it.
type LocalTargetBinding struct {
	root                 string
	rootIdentity         PlatformFileIdentity
	gitDirectory         string
	gitDirectoryIdentity PlatformFileIdentity
	commonDirectory      string
	commonIdentity       PlatformFileIdentity
	repositoryFormat     uint64
	objectFormat         GitHashAlgorithm
	linkedWorktree       bool
	baseRef              string
	baseCommit           GitObjectID
	featureBranch        string
	featureRef           string
	digest               Digest
}

type LocalTargetBindingOptions struct {
	Root                 string
	RootIdentity         PlatformFileIdentity
	GitDirectory         string
	GitDirectoryIdentity PlatformFileIdentity
	CommonDirectory      string
	CommonIdentity       PlatformFileIdentity
	RepositoryFormat     uint64
	ObjectFormat         GitHashAlgorithm
	LinkedWorktree       bool
	BaseRef              string
	BaseCommit           GitObjectID
	FeatureBranch        string
}

func NewLocalTargetBinding(options LocalTargetBindingOptions) (LocalTargetBinding, error) {
	root := filepath.Clean(strings.TrimSpace(options.Root))
	gitDirectory := filepath.Clean(strings.TrimSpace(options.GitDirectory))
	commonDirectory := filepath.Clean(strings.TrimSpace(options.CommonDirectory))
	if !filepath.IsAbs(root) || !filepath.IsAbs(gitDirectory) ||
		!filepath.IsAbs(commonDirectory) {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target binding requires absolute root, Git directory, and common directory",
		)
	}
	if zeroPlatformFileIdentity(options.RootIdentity) ||
		zeroPlatformFileIdentity(options.GitDirectoryIdentity) ||
		zeroPlatformFileIdentity(options.CommonIdentity) {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target binding requires target, Git-directory, and common-directory identities",
		)
	}
	if options.RepositoryFormat > 1 {
		return LocalTargetBinding{}, fmt.Errorf(
			"unsupported Git repository format version %d",
			options.RepositoryFormat,
		)
	}
	if options.ObjectFormat != GitHashSHA1 &&
		options.ObjectFormat != GitHashSHA256 {
		return LocalTargetBinding{}, fmt.Errorf(
			"unsupported repository Git object format %q",
			options.ObjectFormat,
		)
	}
	if options.ObjectFormat == GitHashSHA256 &&
		options.RepositoryFormat != 1 {
		return LocalTargetBinding{}, fmt.Errorf(
			"SHA-256 repositories require Git repository format version 1",
		)
	}
	baseRef, err := normalizeFullyQualifiedBaseRef(options.BaseRef)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if options.BaseCommit.IsZero() ||
		options.BaseCommit.Algorithm() != options.ObjectFormat {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target base commit must use the repository object format",
		)
	}
	featureBranch, err := normalizeFeatureBranch(options.FeatureBranch)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	featureRef := "refs/heads/" + featureBranch
	if options.LinkedWorktree == (gitDirectory == commonDirectory) {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target linked-worktree flag does not match its Git directories",
		)
	}
	binding := LocalTargetBinding{
		root: root, rootIdentity: options.RootIdentity,
		gitDirectory: gitDirectory, gitDirectoryIdentity: options.GitDirectoryIdentity,
		commonDirectory: commonDirectory, commonIdentity: options.CommonIdentity,
		repositoryFormat: options.RepositoryFormat, objectFormat: options.ObjectFormat,
		linkedWorktree: options.LinkedWorktree,
		baseRef:        baseRef, baseCommit: options.BaseCommit,
		featureBranch: featureBranch, featureRef: featureRef,
	}
	digest, err := digestLocalTargetBinding(binding)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	binding.digest = digest
	return binding, nil
}

func (binding LocalTargetBinding) Root() string {
	return binding.root
}
func (binding LocalTargetBinding) RootIdentity() PlatformFileIdentity {
	return binding.rootIdentity
}
func (binding LocalTargetBinding) GitDirectory() string {
	return binding.gitDirectory
}
func (binding LocalTargetBinding) GitDirectoryIdentity() PlatformFileIdentity {
	return binding.gitDirectoryIdentity
}
func (binding LocalTargetBinding) CommonDirectory() string {
	return binding.commonDirectory
}
func (binding LocalTargetBinding) CommonIdentity() PlatformFileIdentity {
	return binding.commonIdentity
}
func (binding LocalTargetBinding) RepositoryFormat() uint64 {
	return binding.repositoryFormat
}
func (binding LocalTargetBinding) ObjectFormat() GitHashAlgorithm {
	return binding.objectFormat
}
func (binding LocalTargetBinding) LinkedWorktree() bool {
	return binding.linkedWorktree
}
func (binding LocalTargetBinding) BaseRef() string {
	return binding.baseRef
}
func (binding LocalTargetBinding) BaseCommit() GitObjectID {
	return binding.baseCommit
}
func (binding LocalTargetBinding) FeatureBranch() string {
	return binding.featureBranch
}
func (binding LocalTargetBinding) FeatureRef() string {
	return binding.featureRef
}
func (binding LocalTargetBinding) Digest() Digest {
	return binding.digest
}
func (binding LocalTargetBinding) IsZero() bool {
	return binding.digest.IsZero()
}

type localTargetBindingWire struct {
	Root                 string               `json:"root"`
	RootIdentity         PlatformFileIdentity `json:"root_identity"`
	GitDirectory         string               `json:"git_directory"`
	GitDirectoryIdentity PlatformFileIdentity `json:"git_directory_identity"`
	CommonDirectory      string               `json:"common_directory"`
	CommonIdentity       PlatformFileIdentity `json:"common_directory_identity"`
	RepositoryFormat     uint64               `json:"repository_format"`
	ObjectFormat         GitHashAlgorithm     `json:"object_format"`
	LinkedWorktree       bool                 `json:"linked_worktree"`
	BaseRef              string               `json:"base_ref"`
	BaseCommit           string               `json:"base_commit"`
	FeatureBranch        string               `json:"feature_branch"`
	FeatureRef           string               `json:"feature_ref"`
}

func localTargetBindingToWire(binding LocalTargetBinding) localTargetBindingWire {
	return localTargetBindingWire{
		Root: binding.root, RootIdentity: binding.rootIdentity,
		GitDirectory:         binding.gitDirectory,
		GitDirectoryIdentity: binding.gitDirectoryIdentity,
		CommonDirectory:      binding.commonDirectory,
		CommonIdentity:       binding.commonIdentity,
		RepositoryFormat:     binding.repositoryFormat,
		ObjectFormat:         binding.objectFormat, LinkedWorktree: binding.linkedWorktree,
		BaseRef: binding.baseRef, BaseCommit: binding.baseCommit.String(),
		FeatureBranch: binding.featureBranch, FeatureRef: binding.featureRef,
	}
}

func localTargetBindingFromWire(wire localTargetBindingWire) (LocalTargetBinding, error) {
	baseCommit, err := ParseGitObjectID(wire.BaseCommit)
	if err != nil {
		return LocalTargetBinding{}, fmt.Errorf("local target base commit: %w", err)
	}
	binding, err := NewLocalTargetBinding(LocalTargetBindingOptions{
		Root: wire.Root, RootIdentity: wire.RootIdentity,
		GitDirectory:         wire.GitDirectory,
		GitDirectoryIdentity: wire.GitDirectoryIdentity,
		CommonDirectory:      wire.CommonDirectory,
		CommonIdentity:       wire.CommonIdentity,
		RepositoryFormat:     wire.RepositoryFormat,
		ObjectFormat:         wire.ObjectFormat, LinkedWorktree: wire.LinkedWorktree,
		BaseRef: wire.BaseRef, BaseCommit: baseCommit,
		FeatureBranch: wire.FeatureBranch,
	})
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if wire.FeatureRef != binding.featureRef {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target feature ref does not match its feature branch",
		)
	}
	return binding, nil
}

func digestLocalTargetBinding(binding LocalTargetBinding) (Digest, error) {
	content, err := json.Marshal(localTargetBindingToWire(binding))
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func zeroPlatformFileIdentity(identity PlatformFileIdentity) bool {
	return identity.Device == 0 && identity.Inode == 0 && identity.Owner == 0
}

package workspace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// AttemptWorktreeGitBinding captures the exact filesystem and Git
// administration objects used to inspect an attempt worktree. Integration
// persists this value before publication so a path replacement, worktree
// relocation, or repository substitution cannot be accepted on retry.
type AttemptWorktreeGitBinding struct {
	worktree                string
	worktreeIdentity        PlatformFileIdentity
	gitDirectory            string
	gitDirectoryIdentity    PlatformFileIdentity
	commonDirectory         string
	commonDirectoryIdentity PlatformFileIdentity
	administrationDigest    Digest
	configurationDigest     Digest
	digest                  Digest
}

type AttemptWorktreeGitBindingOptions struct {
	Worktree                string
	WorktreeIdentity        PlatformFileIdentity
	GitDirectory            string
	GitDirectoryIdentity    PlatformFileIdentity
	CommonDirectory         string
	CommonDirectoryIdentity PlatformFileIdentity
	AdministrationDigest    Digest
	ConfigurationDigest     Digest
}

func NewAttemptWorktreeGitBinding(
	options AttemptWorktreeGitBindingOptions,
) (AttemptWorktreeGitBinding, error) {
	worktree := filepath.Clean(strings.TrimSpace(options.Worktree))
	gitDirectory := filepath.Clean(strings.TrimSpace(options.GitDirectory))
	commonDirectory := filepath.Clean(strings.TrimSpace(options.CommonDirectory))
	if !filepath.IsAbs(worktree) || !filepath.IsAbs(gitDirectory) ||
		!filepath.IsAbs(commonDirectory) {
		return AttemptWorktreeGitBinding{}, fmt.Errorf(
			"attempt worktree Git binding requires absolute worktree and Git directories",
		)
	}
	if zeroPlatformFileIdentity(options.WorktreeIdentity) ||
		zeroPlatformFileIdentity(options.GitDirectoryIdentity) ||
		zeroPlatformFileIdentity(options.CommonDirectoryIdentity) ||
		options.AdministrationDigest.IsZero() ||
		options.ConfigurationDigest.IsZero() {
		return AttemptWorktreeGitBinding{}, fmt.Errorf(
			"attempt worktree Git binding requires exact directory identities and Git state digests",
		)
	}
	binding := AttemptWorktreeGitBinding{
		worktree: worktree, worktreeIdentity: options.WorktreeIdentity,
		gitDirectory:            gitDirectory,
		gitDirectoryIdentity:    options.GitDirectoryIdentity,
		commonDirectory:         commonDirectory,
		commonDirectoryIdentity: options.CommonDirectoryIdentity,
		administrationDigest:    options.AdministrationDigest,
		configurationDigest:     options.ConfigurationDigest,
	}
	digest, err := digestAttemptWorktreeGitBinding(binding)
	if err != nil {
		return AttemptWorktreeGitBinding{}, err
	}
	binding.digest = digest
	if err := binding.validate(); err != nil {
		return AttemptWorktreeGitBinding{}, err
	}
	return binding, nil
}

func (binding AttemptWorktreeGitBinding) Worktree() string {
	return binding.worktree
}
func (binding AttemptWorktreeGitBinding) WorktreeIdentity() PlatformFileIdentity {
	return binding.worktreeIdentity
}
func (binding AttemptWorktreeGitBinding) GitDirectory() string {
	return binding.gitDirectory
}
func (binding AttemptWorktreeGitBinding) GitDirectoryIdentity() PlatformFileIdentity {
	return binding.gitDirectoryIdentity
}
func (binding AttemptWorktreeGitBinding) CommonDirectory() string {
	return binding.commonDirectory
}
func (binding AttemptWorktreeGitBinding) CommonDirectoryIdentity() PlatformFileIdentity {
	return binding.commonDirectoryIdentity
}
func (binding AttemptWorktreeGitBinding) AdministrationDigest() Digest {
	return binding.administrationDigest
}
func (binding AttemptWorktreeGitBinding) ConfigurationDigest() Digest {
	return binding.configurationDigest
}
func (binding AttemptWorktreeGitBinding) Digest() Digest {
	return binding.digest
}
func (binding AttemptWorktreeGitBinding) IsZero() bool {
	return binding.digest.IsZero()
}

func (binding AttemptWorktreeGitBinding) validate() error {
	if !filepath.IsAbs(binding.worktree) ||
		filepath.Clean(strings.TrimSpace(binding.worktree)) !=
			binding.worktree ||
		!filepath.IsAbs(binding.gitDirectory) ||
		filepath.Clean(strings.TrimSpace(binding.gitDirectory)) !=
			binding.gitDirectory ||
		!filepath.IsAbs(binding.commonDirectory) ||
		filepath.Clean(strings.TrimSpace(binding.commonDirectory)) !=
			binding.commonDirectory ||
		zeroPlatformFileIdentity(binding.worktreeIdentity) ||
		zeroPlatformFileIdentity(binding.gitDirectoryIdentity) ||
		zeroPlatformFileIdentity(binding.commonDirectoryIdentity) ||
		binding.administrationDigest.IsZero() ||
		binding.configurationDigest.IsZero() ||
		binding.digest.IsZero() {
		return fmt.Errorf(
			"attempt worktree Git binding has incomplete immutable bindings",
		)
	}
	digest, err := digestAttemptWorktreeGitBinding(binding)
	if err != nil {
		return err
	}
	if digest != binding.digest {
		return fmt.Errorf("attempt worktree Git binding digest mismatch")
	}
	return nil
}

type attemptWorktreeGitBindingWire struct {
	SchemaVersion           int                  `json:"schema_version"`
	Worktree                string               `json:"worktree"`
	WorktreeIdentity        PlatformFileIdentity `json:"worktree_identity"`
	GitDirectory            string               `json:"git_directory"`
	GitDirectoryIdentity    PlatformFileIdentity `json:"git_directory_identity"`
	CommonDirectory         string               `json:"common_directory"`
	CommonDirectoryIdentity PlatformFileIdentity `json:"common_directory_identity"`
	AdministrationDigest    string               `json:"administration_digest"`
	ConfigurationDigest     string               `json:"configuration_digest"`
}

func attemptWorktreeGitBindingToWire(
	binding AttemptWorktreeGitBinding,
) attemptWorktreeGitBindingWire {
	return attemptWorktreeGitBindingWire{
		SchemaVersion:           JournalSchemaVersion,
		Worktree:                binding.worktree,
		WorktreeIdentity:        binding.worktreeIdentity,
		GitDirectory:            binding.gitDirectory,
		GitDirectoryIdentity:    binding.gitDirectoryIdentity,
		CommonDirectory:         binding.commonDirectory,
		CommonDirectoryIdentity: binding.commonDirectoryIdentity,
		AdministrationDigest:    binding.administrationDigest.String(),
		ConfigurationDigest:     binding.configurationDigest.String(),
	}
}

func attemptWorktreeGitBindingFromWire(
	wire attemptWorktreeGitBindingWire,
) (AttemptWorktreeGitBinding, error) {
	if wire.SchemaVersion != JournalSchemaVersion {
		return AttemptWorktreeGitBinding{}, fmt.Errorf(
			"attempt worktree Git binding schema_version must be %d",
			JournalSchemaVersion,
		)
	}
	administrationDigest, err := ParseDigest(wire.AdministrationDigest)
	if err != nil {
		return AttemptWorktreeGitBinding{}, fmt.Errorf(
			"attempt worktree administration digest: %w", err,
		)
	}
	configurationDigest, err := ParseDigest(wire.ConfigurationDigest)
	if err != nil {
		return AttemptWorktreeGitBinding{}, fmt.Errorf(
			"attempt worktree configuration digest: %w", err,
		)
	}
	return NewAttemptWorktreeGitBinding(
		AttemptWorktreeGitBindingOptions{
			Worktree:                wire.Worktree,
			WorktreeIdentity:        wire.WorktreeIdentity,
			GitDirectory:            wire.GitDirectory,
			GitDirectoryIdentity:    wire.GitDirectoryIdentity,
			CommonDirectory:         wire.CommonDirectory,
			CommonDirectoryIdentity: wire.CommonDirectoryIdentity,
			AdministrationDigest:    administrationDigest,
			ConfigurationDigest:     configurationDigest,
		},
	)
}

func digestAttemptWorktreeGitBinding(
	binding AttemptWorktreeGitBinding,
) (Digest, error) {
	content, err := json.Marshal(attemptWorktreeGitBindingToWire(binding))
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

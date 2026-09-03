package workspace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// LocalTargetBinding records the stable semantic identity of the admitted
// primary repository. Git administration paths are discovered for each
// operation instead of becoming durable protocol inputs.
type LocalTargetBinding struct {
	root          string
	objectFormat  GitHashAlgorithm
	baseRef       string
	baseCommit    GitObjectID
	featureBranch string
	featureRef    string
	digest        Digest
}

type LocalTargetBindingOptions struct {
	Root          string
	ObjectFormat  GitHashAlgorithm
	BaseRef       string
	BaseCommit    GitObjectID
	FeatureBranch string
}

func NewLocalTargetBinding(options LocalTargetBindingOptions) (LocalTargetBinding, error) {
	root := filepath.Clean(strings.TrimSpace(options.Root))
	if !filepath.IsAbs(root) {
		return LocalTargetBinding{}, fmt.Errorf("local target binding requires an absolute root")
	}
	if options.ObjectFormat != GitHashSHA1 && options.ObjectFormat != GitHashSHA256 {
		return LocalTargetBinding{}, fmt.Errorf("unsupported repository Git object format %q", options.ObjectFormat)
	}
	baseRef, err := normalizeFullyQualifiedBaseRef(options.BaseRef)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if options.BaseCommit.IsZero() || options.BaseCommit.Algorithm() != options.ObjectFormat {
		return LocalTargetBinding{}, fmt.Errorf("local target base commit must use the repository object format")
	}
	featureBranch, err := normalizeFeatureBranch(options.FeatureBranch)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	binding := LocalTargetBinding{
		root: root, objectFormat: options.ObjectFormat,
		baseRef: baseRef, baseCommit: options.BaseCommit,
		featureBranch: featureBranch, featureRef: "refs/heads/" + featureBranch,
	}
	digest, err := digestLocalTargetBinding(binding)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	binding.digest = digest
	return binding, nil
}

func (binding LocalTargetBinding) Root() string { return binding.root }
func (binding LocalTargetBinding) ObjectFormat() GitHashAlgorithm {
	return binding.objectFormat
}
func (binding LocalTargetBinding) BaseRef() string { return binding.baseRef }
func (binding LocalTargetBinding) BaseCommit() GitObjectID {
	return binding.baseCommit
}
func (binding LocalTargetBinding) FeatureBranch() string {
	return binding.featureBranch
}
func (binding LocalTargetBinding) FeatureRef() string { return binding.featureRef }
func (binding LocalTargetBinding) Digest() Digest     { return binding.digest }
func (binding LocalTargetBinding) IsZero() bool       { return binding.digest.IsZero() }

type localTargetBindingWire struct {
	Root          string           `json:"root"`
	ObjectFormat  GitHashAlgorithm `json:"object_format"`
	BaseRef       string           `json:"base_ref"`
	BaseCommit    string           `json:"base_commit"`
	FeatureBranch string           `json:"feature_branch"`
	FeatureRef    string           `json:"feature_ref"`
}

func localTargetBindingToWire(binding LocalTargetBinding) localTargetBindingWire {
	return localTargetBindingWire{
		Root: binding.root, ObjectFormat: binding.objectFormat,
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
		Root: wire.Root, ObjectFormat: wire.ObjectFormat,
		BaseRef: wire.BaseRef, BaseCommit: baseCommit,
		FeatureBranch: wire.FeatureBranch,
	})
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if wire.FeatureRef != binding.featureRef {
		return LocalTargetBinding{}, fmt.Errorf("local target feature ref does not match its feature branch")
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

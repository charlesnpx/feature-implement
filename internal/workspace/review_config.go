package workspace

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// ReviewGateConfig names an adapter and recipe and binds them to exactly one
// policy source. The policy is opaque prose to this package; it is retained so
// the adapter, rather than this tool, is the authority that interprets it.
type ReviewGateConfig struct {
	adapter      ID
	recipe       ID
	policyPath   string
	policy       []byte
	policyDigest Digest
}

func newReviewGateConfig(wire *reviewGateWire, location string) (*ReviewGateConfig, error) {
	if wire == nil {
		return nil, nil
	}
	if wire.Adapter == nil || wire.Recipe == nil || wire.PolicyFile == nil {
		return nil, fmt.Errorf("%s must name adapter, recipe, and policy_file together", location)
	}
	adapter, err := NewID(*wire.Adapter)
	if err != nil {
		return nil, fmt.Errorf("%s adapter: %w", location, err)
	}
	recipe, err := NewID(*wire.Recipe)
	if err != nil {
		return nil, fmt.Errorf("%s recipe: %w", location, err)
	}
	policyPath, err := normalizeSourcePath(*wire.PolicyFile)
	if err != nil {
		return nil, fmt.Errorf("%s policy_file: %w", location, err)
	}
	return &ReviewGateConfig{adapter: adapter, recipe: recipe, policyPath: policyPath}, nil
}

func (config ReviewGateConfig) Adapter() ID        { return config.adapter }
func (config ReviewGateConfig) Recipe() ID         { return config.recipe }
func (config ReviewGateConfig) PolicyPath() string { return config.policyPath }
func (config ReviewGateConfig) PolicyDigest() Digest {
	return config.policyDigest
}
func (config ReviewGateConfig) Policy() []byte {
	return append([]byte(nil), config.policy...)
}

func (config ReviewGateConfig) complete() bool {
	return !config.adapter.IsZero() && !config.recipe.IsZero() && config.policyPath != ""
}

func (config ReviewGateConfig) bound() bool {
	return config.complete() && len(config.policy) != 0 && !config.policyDigest.IsZero()
}

func cloneReviewGateConfig(config ReviewGateConfig) ReviewGateConfig {
	config.policy = append([]byte(nil), config.policy...)
	return config
}

func bindReviewGateConfigPolicy(config ReviewGateConfig, policy SourceArtifact) (ReviewGateConfig, error) {
	path, err := normalizeSourcePath(policy.Path)
	if err != nil {
		return ReviewGateConfig{}, fmt.Errorf("review gate policy source path: %w", err)
	}
	if !config.complete() || path != config.policyPath {
		return ReviewGateConfig{}, fmt.Errorf("review gate policy source does not match configured policy_file")
	}
	if len(policy.Bytes) == 0 || len(policy.Bytes) > MaxArtifactBytes || !utf8.Valid(policy.Bytes) {
		return ReviewGateConfig{}, fmt.Errorf("review gate policy source must be non-empty valid UTF-8 within the artifact limit")
	}
	config.policy = append([]byte(nil), policy.Bytes...)
	config.policyDigest = DigestBytes(config.policy)
	return config, nil
}

func reviewGatePolicyPaths(config ExecutionConfig) []string {
	paths := make(map[string]struct{})
	if config.reviewGate != nil {
		paths[config.reviewGate.policyPath] = struct{}{}
	}
	for _, unit := range config.mergeUnits {
		if unit.reviewGate != nil {
			paths[unit.reviewGate.policyPath] = struct{}{}
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func bindExecutionReviewGatePolicies(config ExecutionConfig, policies []SourceArtifact) (ExecutionConfig, error) {
	required := reviewGatePolicyPaths(config)
	if len(required) == 0 {
		if len(policies) != 0 {
			return ExecutionConfig{}, fmt.Errorf("review gate policy sources were supplied without a configured review gate")
		}
		return config, nil
	}
	sources := make(map[string]SourceArtifact, len(policies))
	for index, policy := range policies {
		path, err := normalizeSourcePath(policy.Path)
		if err != nil {
			return ExecutionConfig{}, fmt.Errorf("review gate policy source %d: %w", index, err)
		}
		if _, exists := sources[path]; exists {
			return ExecutionConfig{}, fmt.Errorf("duplicate review gate policy source %s", path)
		}
		policy.Path = path
		policy.Bytes = append([]byte(nil), policy.Bytes...)
		sources[path] = policy
	}
	if len(sources) != len(required) {
		return ExecutionConfig{}, fmt.Errorf("configured review gate policies and supplied policy sources do not match")
	}
	bind := func(gate *ReviewGateConfig) (*ReviewGateConfig, error) {
		if gate == nil {
			return nil, nil
		}
		source, exists := sources[gate.policyPath]
		if !exists {
			return nil, fmt.Errorf("configured review gate policy %s was not supplied", gate.policyPath)
		}
		bound, err := bindReviewGateConfigPolicy(*gate, source)
		if err != nil {
			return nil, err
		}
		return &bound, nil
	}
	var err error
	if config.reviewGate, err = bind(config.reviewGate); err != nil {
		return ExecutionConfig{}, err
	}
	for index := range config.mergeUnits {
		if config.mergeUnits[index].reviewGate, err = bind(config.mergeUnits[index].reviewGate); err != nil {
			return ExecutionConfig{}, err
		}
	}
	return config, nil
}

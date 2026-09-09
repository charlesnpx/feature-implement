package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BundledDefaultReviewConfiguration is the opaque marker used when a plan
// was created without an operator review configuration file. Its meaning is
// owned by the review tooling, not by feature-implement.
const BundledDefaultReviewConfiguration = "bundled-default"

// ReviewConfiguration is the frozen review configuration selected when a
// workspace bundle was created. The bytes are intentionally opaque here. A
// marker identifies the review tooling's bundled default without importing or
// reproducing that tooling's configuration reader.
type ReviewConfiguration struct {
	source string
	bytes  []byte
}

func bundledDefaultReviewConfiguration() ReviewConfiguration {
	return ReviewConfiguration{source: BundledDefaultReviewConfiguration}
}

func newReviewConfiguration(source SourceArtifact) (ReviewConfiguration, *NormalizedArtifact, error) {
	path := strings.TrimSpace(source.Path)
	if path == "" || path == BundledDefaultReviewConfiguration {
		if len(source.Bytes) != 0 {
			return ReviewConfiguration{}, nil, fmt.Errorf("bundled default review configuration must not carry source bytes")
		}
		return bundledDefaultReviewConfiguration(), nil, nil
	}
	path, err := normalizeSourcePath(path)
	if err != nil {
		return ReviewConfiguration{}, nil, fmt.Errorf("review configuration source path: %w", err)
	}
	if len(source.Bytes) == 0 || len(source.Bytes) > MaxArtifactBytes {
		return ReviewConfiguration{}, nil, fmt.Errorf("review configuration source must be non-empty within the artifact limit")
	}
	canonical, err := json.Marshal(string(source.Bytes))
	if err != nil {
		return ReviewConfiguration{}, nil, fmt.Errorf("canonicalize review configuration source: %w", err)
	}
	configuration := ReviewConfiguration{source: path, bytes: append([]byte(nil), source.Bytes...)}
	artifact := newArtifact(ArtifactReviewConfiguration, ID{}, path, source.Bytes, canonical)
	return configuration, &artifact, nil
}

// Source identifies the frozen source path, or BundledDefaultReviewConfiguration
// when the bundle carries the review tooling's default marker.
func (configuration ReviewConfiguration) Source() string {
	if configuration.source == "" {
		return BundledDefaultReviewConfiguration
	}
	return configuration.source
}

// BundledDefault reports whether the bundle carries the default marker.
func (configuration ReviewConfiguration) BundledDefault() bool {
	return configuration.Source() == BundledDefaultReviewConfiguration
}

// Bytes returns the exact frozen configuration bytes. It is empty for the
// bundled-default marker.
func (configuration ReviewConfiguration) Bytes() []byte {
	return append([]byte(nil), configuration.bytes...)
}

// Digest identifies the frozen bytes or marker supplied to review tooling.
func (configuration ReviewConfiguration) Digest() Digest {
	if configuration.BundledDefault() {
		return DigestBytes([]byte(BundledDefaultReviewConfiguration))
	}
	return DigestBytes(configuration.bytes)
}

func cloneReviewConfiguration(configuration ReviewConfiguration) ReviewConfiguration {
	configuration.bytes = append([]byte(nil), configuration.bytes...)
	return configuration
}

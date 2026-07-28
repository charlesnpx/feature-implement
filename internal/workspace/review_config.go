package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

const (
	maxReviewProfiles              = 32
	maxReviewInfrastructureRetries = ^uint16(0) - 1
)

type ReviewReviewerPolicy string

const (
	ReviewReviewerRetain              ReviewReviewerPolicy = "retain"
	ReviewReviewerFreshEachInvocation ReviewReviewerPolicy = "fresh_each_invocation"
)

func (policy ReviewReviewerPolicy) valid() bool {
	return policy == ReviewReviewerRetain || policy == ReviewReviewerFreshEachInvocation
}

type ReviewProfile struct {
	id             ID
	runner         ID
	reviewerPolicy ReviewReviewerPolicy
}

func (profile ReviewProfile) ID() ID                               { return profile.id }
func (profile ReviewProfile) Runner() ID                           { return profile.runner }
func (profile ReviewProfile) ReviewerPolicy() ReviewReviewerPolicy { return profile.reviewerPolicy }

type ReviewLoop struct {
	profiles                 []ReviewProfile
	maxRounds                uint16
	maxFixes                 uint16
	maxInfrastructureRetries uint16
	digest                   Digest
}

func (loop ReviewLoop) Profiles() []ReviewProfile {
	return append([]ReviewProfile(nil), loop.profiles...)
}
func (loop ReviewLoop) MaxRounds() uint16                { return loop.maxRounds }
func (loop ReviewLoop) MaxFixes() uint16                 { return loop.maxFixes }
func (loop ReviewLoop) MaxInfrastructureRetries() uint16 { return loop.maxInfrastructureRetries }
func (loop ReviewLoop) Digest() Digest                   { return loop.digest }

func normalizeReviewProfiles(wire []reviewProfileWire) ([]ReviewProfile, map[string]ReviewProfile, error) {
	profiles := make([]ReviewProfile, 0, len(wire))
	byID := make(map[string]ReviewProfile, len(wire))
	for index, item := range wire {
		id, err := NewID(item.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("review_profiles[%d].id: %w", index, err)
		}
		runner, err := NewID(item.Runner)
		if err != nil {
			return nil, nil, fmt.Errorf("review profile %s runner: %w", id, err)
		}
		policy := ReviewReviewerPolicy(item.ReviewerPolicy)
		if !policy.valid() {
			return nil, nil, fmt.Errorf("review profile %s reviewer_policy %q is unsupported", id, item.ReviewerPolicy)
		}
		if _, exists := byID[id.String()]; exists {
			return nil, nil, fmt.Errorf("duplicate review profile %s", id)
		}
		profile := ReviewProfile{id: id, runner: runner, reviewerPolicy: policy}
		profiles = append(profiles, profile)
		byID[id.String()] = profile
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].id.String() < profiles[j].id.String() })
	return profiles, byID, nil
}

func normalizeReviewLoop(
	wire *reviewLoopWire,
	profiles map[string]ReviewProfile,
	policy ExecutionPolicy,
	location string,
) (*ReviewLoop, error) {
	if wire == nil {
		return nil, nil
	}
	if len(wire.Profiles) == 0 || len(wire.Profiles) > maxReviewProfiles ||
		wire.MaxInfrastructureRetries == nil || *wire.MaxInfrastructureRetries == 0 ||
		*wire.MaxInfrastructureRetries > maxReviewInfrastructureRetries {
		return nil, fmt.Errorf("%s requires ordered profiles and a positive infrastructure retry budget below 65535", location)
	}
	loop := ReviewLoop{
		profiles:  make([]ReviewProfile, 0, len(wire.Profiles)),
		maxRounds: policy.maxReviewRounds, maxFixes: policy.maxReviewFixes,
		maxInfrastructureRetries: *wire.MaxInfrastructureRetries,
	}
	seen := make(map[string]struct{}, len(wire.Profiles))
	for index, raw := range wire.Profiles {
		id, err := NewID(raw)
		if err != nil {
			return nil, fmt.Errorf("%s profiles[%d]: %w", location, index, err)
		}
		profile, exists := profiles[id.String()]
		if !exists {
			return nil, fmt.Errorf("%s references unknown review profile %s", location, id)
		}
		if _, duplicate := seen[id.String()]; duplicate {
			return nil, fmt.Errorf("%s duplicates review profile %s", location, id)
		}
		seen[id.String()] = struct{}{}
		loop.profiles = append(loop.profiles, profile)
	}
	canonical, err := canonicalReviewLoopBytes(loop)
	if err != nil {
		return nil, err
	}
	loop.digest = DigestBytes(canonical)
	return &loop, nil
}

func canonicalReviewLoopBytes(loop ReviewLoop) ([]byte, error) {
	if len(loop.profiles) == 0 || loop.maxRounds == 0 || loop.maxFixes == 0 ||
		loop.maxInfrastructureRetries == 0 || loop.maxInfrastructureRetries > maxReviewInfrastructureRetries {
		return nil, fmt.Errorf("review loop is incomplete")
	}
	type profileJSON struct {
		ID             string               `json:"id"`
		Runner         string               `json:"runner"`
		ReviewerPolicy ReviewReviewerPolicy `json:"reviewer_policy"`
	}
	type loopJSON struct {
		SchemaVersion            int           `json:"schema_version"`
		Profiles                 []profileJSON `json:"profiles"`
		MaxReviewRounds          uint16        `json:"max_review_rounds"`
		MaxReviewFixes           uint16        `json:"max_review_fixes"`
		MaxInfrastructureRetries uint16        `json:"max_infrastructure_retries"`
	}
	value := loopJSON{
		SchemaVersion: 2, Profiles: make([]profileJSON, 0, len(loop.profiles)),
		MaxReviewRounds: loop.maxRounds, MaxReviewFixes: loop.maxFixes,
		MaxInfrastructureRetries: loop.maxInfrastructureRetries,
	}
	for _, profile := range loop.profiles {
		if profile.id.IsZero() || profile.runner.IsZero() || !profile.reviewerPolicy.valid() {
			return nil, fmt.Errorf("review loop contains an invalid profile")
		}
		value.Profiles = append(value.Profiles, profileJSON{
			ID: profile.id.String(), Runner: profile.runner.String(), ReviewerPolicy: profile.reviewerPolicy,
		})
	}
	return json.Marshal(value)
}

func cloneReviewLoop(loop ReviewLoop) ReviewLoop {
	loop.profiles = append([]ReviewProfile(nil), loop.profiles...)
	return loop
}

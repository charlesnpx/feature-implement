package workspace

import (
	"fmt"
	"sort"
)

type AttemptBoundaryMode string

const (
	AttemptBoundaryPauseOnly           AttemptBoundaryMode = "pause_only"
	AttemptBoundaryCompleteGoalAndWait AttemptBoundaryMode = "complete_goal_and_wait"
)

func (mode AttemptBoundaryMode) valid() bool {
	return mode == AttemptBoundaryPauseOnly || mode == AttemptBoundaryCompleteGoalAndWait
}

type AttemptBoundaryPolicy struct {
	mode          AttemptBoundaryMode
	serialSegment ID
}

func newAttemptBoundaryPolicy(wire *attemptBoundaryPolicyWire, location string) (AttemptBoundaryPolicy, error) {
	if wire == nil {
		return AttemptBoundaryPolicy{}, fmt.Errorf("%s boundary policy must be explicit", location)
	}
	mode := AttemptBoundaryMode(wire.Mode)
	if !mode.valid() {
		return AttemptBoundaryPolicy{}, fmt.Errorf("%s boundary mode %q is unsupported", location, wire.Mode)
	}
	var serialSegment ID
	if wire.SerialSegment != nil {
		parsed, err := NewID(*wire.SerialSegment)
		if err != nil {
			return AttemptBoundaryPolicy{}, fmt.Errorf("%s serial_segment: %w", location, err)
		}
		serialSegment = parsed
	}
	return AttemptBoundaryPolicy{mode: mode, serialSegment: serialSegment}, nil
}

func (policy AttemptBoundaryPolicy) Mode() AttemptBoundaryMode { return policy.mode }
func (policy AttemptBoundaryPolicy) SerialSegment() ID         { return policy.serialSegment }

// ExecutionPolicy uses explicit fields whose strengthening directions are
// defined below. Required controls may only become true, permissions may only
// become false, and positive budgets may only decrease.
type ExecutionPolicy struct {
	requirePassingChecks  bool
	requireSignedReceipts bool
	allowWriteNetwork     bool
	maxAttempts           uint16
	maxReviewRounds       uint16
	maxReviewFixes        uint16
}

func newExecutionPolicy(wire executionPolicyWire, location string) (ExecutionPolicy, error) {
	if wire.RequirePassingChecks == nil || wire.RequireSignedReceipts == nil || wire.AllowWriteNetwork == nil ||
		wire.MaxAttempts == nil || wire.MaxReviewRounds == nil || wire.MaxReviewFixes == nil {
		return ExecutionPolicy{}, fmt.Errorf("%s must explicitly define every policy field", location)
	}
	if *wire.MaxAttempts == 0 || *wire.MaxReviewRounds == 0 || *wire.MaxReviewFixes == 0 {
		return ExecutionPolicy{}, fmt.Errorf("%s budgets must be positive", location)
	}
	return ExecutionPolicy{
		requirePassingChecks:  *wire.RequirePassingChecks,
		requireSignedReceipts: *wire.RequireSignedReceipts,
		allowWriteNetwork:     *wire.AllowWriteNetwork,
		maxAttempts:           *wire.MaxAttempts,
		maxReviewRounds:       *wire.MaxReviewRounds,
		maxReviewFixes:        *wire.MaxReviewFixes,
	}, nil
}

func (policy ExecutionPolicy) RequirePassingChecks() bool  { return policy.requirePassingChecks }
func (policy ExecutionPolicy) RequireSignedReceipts() bool { return policy.requireSignedReceipts }
func (policy ExecutionPolicy) AllowWriteNetwork() bool     { return policy.allowWriteNetwork }
func (policy ExecutionPolicy) MaxAttempts() uint16         { return policy.maxAttempts }
func (policy ExecutionPolicy) MaxReviewRounds() uint16     { return policy.maxReviewRounds }
func (policy ExecutionPolicy) MaxReviewFixes() uint16      { return policy.maxReviewFixes }

func (policy ExecutionPolicy) validateStrengthens(base ExecutionPolicy, location string) error {
	if base.requirePassingChecks && !policy.requirePassingChecks {
		return fmt.Errorf("%s weakens require_passing_checks", location)
	}
	if base.requireSignedReceipts && !policy.requireSignedReceipts {
		return fmt.Errorf("%s weakens require_signed_receipts", location)
	}
	if !base.allowWriteNetwork && policy.allowWriteNetwork {
		return fmt.Errorf("%s weakens allow_write_network", location)
	}
	if policy.maxAttempts > base.maxAttempts {
		return fmt.Errorf("%s weakens max_attempts", location)
	}
	if policy.maxReviewRounds > base.maxReviewRounds {
		return fmt.Errorf("%s weakens max_review_rounds", location)
	}
	if policy.maxReviewFixes > base.maxReviewFixes {
		return fmt.Errorf("%s weakens max_review_fixes", location)
	}
	return nil
}

type ExecutionProfile struct {
	id     ID
	runner ID
	policy ExecutionPolicy
}

func (profile ExecutionProfile) ID() ID                  { return profile.id }
func (profile ExecutionProfile) Runner() ID              { return profile.runner }
func (profile ExecutionProfile) Policy() ExecutionPolicy { return profile.policy }

type UnitExecution struct {
	planID            ID
	mergeUnitID       ID
	profileID         ID
	policy            ExecutionPolicy
	boundary          AttemptBoundaryPolicy
	commitProtocol    *CommitProtocol
	reviewFixProtocol *ReviewFixProtocol
	reviewLoop        *ReviewLoop
}

func (unit UnitExecution) PlanID() ID              { return unit.planID }
func (unit UnitExecution) MergeUnitID() ID         { return unit.mergeUnitID }
func (unit UnitExecution) ProfileID() ID           { return unit.profileID }
func (unit UnitExecution) Policy() ExecutionPolicy { return unit.policy }
func (unit UnitExecution) Boundary() AttemptBoundaryPolicy {
	return unit.boundary
}
func (unit UnitExecution) CommitProtocol() (CommitProtocol, bool) {
	if unit.commitProtocol == nil {
		return CommitProtocol{}, false
	}
	return *cloneCommitProtocol(unit.commitProtocol), true
}
func (unit UnitExecution) ReviewFixProtocol() (ReviewFixProtocol, bool) {
	if unit.reviewFixProtocol == nil {
		return ReviewFixProtocol{}, false
	}
	return *cloneReviewFixProtocol(unit.reviewFixProtocol), true
}
func (unit UnitExecution) ReviewLoop() (ReviewLoop, bool) {
	if unit.reviewLoop == nil {
		return ReviewLoop{}, false
	}
	return cloneReviewLoop(*unit.reviewLoop), true
}

// ExecutionConfig is the one policy authority for all workspace merge units.
type ExecutionConfig struct {
	policy         ExecutionPolicy
	profiles       []ExecutionProfile
	reviewProfiles []ReviewProfile
	mergeUnits     []UnitExecution
}

func DecodeExecutionConfig(source []byte) (ExecutionConfig, error) {
	var wire executionConfigWire
	if err := decodeStrictV2("execution config", source, &wire); err != nil {
		return ExecutionConfig{}, err
	}
	return normalizeExecutionConfig(wire)
}

func normalizeExecutionConfig(wire executionConfigWire) (ExecutionConfig, error) {
	policy, err := newExecutionPolicy(wire.Policy, "policy")
	if err != nil {
		return ExecutionConfig{}, err
	}
	if len(wire.Profiles) == 0 || len(wire.MergeUnits) == 0 {
		return ExecutionConfig{}, fmt.Errorf("execution config requires profiles and merge_units")
	}
	profiles := make([]ExecutionProfile, 0, len(wire.Profiles))
	profileByID := make(map[string]ExecutionProfile, len(wire.Profiles))
	for index, item := range wire.Profiles {
		profileID, err := NewID(item.ID)
		if err != nil {
			return ExecutionConfig{}, fmt.Errorf("profiles[%d].id: %w", index, err)
		}
		runnerID, err := NewID(item.Runner)
		if err != nil {
			return ExecutionConfig{}, fmt.Errorf("profile %s runner: %w", profileID, err)
		}
		profilePolicy, err := newExecutionPolicy(item.Policy, fmt.Sprintf("profile %s policy", profileID))
		if err != nil {
			return ExecutionConfig{}, err
		}
		if err := profilePolicy.validateStrengthens(policy, fmt.Sprintf("profile %s policy", profileID)); err != nil {
			return ExecutionConfig{}, err
		}
		if _, exists := profileByID[profileID.String()]; exists {
			return ExecutionConfig{}, fmt.Errorf("duplicate execution profile %s", profileID)
		}
		profile := ExecutionProfile{id: profileID, runner: runnerID, policy: profilePolicy}
		profileByID[profileID.String()] = profile
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].id.String() < profiles[j].id.String() })
	reviewProfiles, reviewProfileByID, err := normalizeReviewProfiles(wire.ReviewProfiles)
	if err != nil {
		return ExecutionConfig{}, err
	}

	units := make([]UnitExecution, 0, len(wire.MergeUnits))
	unitKeys := make(map[string]struct{}, len(wire.MergeUnits))
	for index, item := range wire.MergeUnits {
		planID, err := NewID(item.PlanID)
		if err != nil {
			return ExecutionConfig{}, fmt.Errorf("merge_units[%d].plan_id: %w", index, err)
		}
		mergeUnitID, err := NewID(item.MergeUnitID)
		if err != nil {
			return ExecutionConfig{}, fmt.Errorf("merge_units[%d].merge_unit_id: %w", index, err)
		}
		profileID, err := NewID(item.Profile)
		if err != nil {
			return ExecutionConfig{}, fmt.Errorf("merge unit %s/%s profile: %w", planID, mergeUnitID, err)
		}
		profile, exists := profileByID[profileID.String()]
		if !exists {
			return ExecutionConfig{}, fmt.Errorf("merge unit %s/%s references unknown profile %s", planID, mergeUnitID, profileID)
		}
		unitPolicy, err := newExecutionPolicy(item.Policy, fmt.Sprintf("merge unit %s/%s policy", planID, mergeUnitID))
		if err != nil {
			return ExecutionConfig{}, err
		}
		if err := unitPolicy.validateStrengthens(profile.policy, fmt.Sprintf("merge unit %s/%s policy", planID, mergeUnitID)); err != nil {
			return ExecutionConfig{}, err
		}
		boundary, err := newAttemptBoundaryPolicy(item.Boundary, fmt.Sprintf("merge unit %s/%s", planID, mergeUnitID))
		if err != nil {
			return ExecutionConfig{}, err
		}
		commitProtocol, err := normalizeCommitProtocol(
			item.CommitProtocol, profile.runner, fmt.Sprintf("merge unit %s/%s commit_protocol", planID, mergeUnitID),
		)
		if err != nil {
			return ExecutionConfig{}, err
		}
		reviewFixProtocol, err := normalizeReviewFixProtocol(
			item.ReviewFixProtocol, profile.runner, fmt.Sprintf("merge unit %s/%s review_fix_protocol", planID, mergeUnitID),
		)
		if err != nil {
			return ExecutionConfig{}, err
		}
		reviewLoop, err := normalizeReviewLoop(
			item.ReviewLoop, reviewProfileByID, unitPolicy,
			fmt.Sprintf("merge unit %s/%s review_loop", planID, mergeUnitID),
		)
		if err != nil {
			return ExecutionConfig{}, err
		}
		if reviewLoop != nil && reviewFixProtocol == nil {
			return ExecutionConfig{}, fmt.Errorf("merge unit %s/%s review_loop requires review_fix_protocol", planID, mergeUnitID)
		}
		key := planID.String() + "\x00" + mergeUnitID.String()
		if _, exists := unitKeys[key]; exists {
			return ExecutionConfig{}, fmt.Errorf("duplicate execution policy for merge unit %s/%s", planID, mergeUnitID)
		}
		unitKeys[key] = struct{}{}
		units = append(units, UnitExecution{
			planID: planID, mergeUnitID: mergeUnitID, profileID: profileID,
			policy: unitPolicy, boundary: boundary,
			commitProtocol: commitProtocol, reviewFixProtocol: reviewFixProtocol, reviewLoop: reviewLoop,
		})
	}
	sort.Slice(units, func(i, j int) bool {
		left := units[i].planID.String() + "\x00" + units[i].mergeUnitID.String()
		right := units[j].planID.String() + "\x00" + units[j].mergeUnitID.String()
		return left < right
	})

	return ExecutionConfig{policy: policy, profiles: profiles, reviewProfiles: reviewProfiles, mergeUnits: units}, nil
}

func (config ExecutionConfig) Policy() ExecutionPolicy { return config.policy }
func (config ExecutionConfig) Profiles() []ExecutionProfile {
	return append([]ExecutionProfile(nil), config.profiles...)
}
func (config ExecutionConfig) ReviewProfiles() []ReviewProfile {
	return append([]ReviewProfile(nil), config.reviewProfiles...)
}
func (config ExecutionConfig) MergeUnits() []UnitExecution {
	result := append([]UnitExecution(nil), config.mergeUnits...)
	for index := range result {
		result[index].commitProtocol = cloneCommitProtocol(result[index].commitProtocol)
		result[index].reviewFixProtocol = cloneReviewFixProtocol(result[index].reviewFixProtocol)
		if result[index].reviewLoop != nil {
			loop := cloneReviewLoop(*result[index].reviewLoop)
			result[index].reviewLoop = &loop
		}
	}
	return result
}

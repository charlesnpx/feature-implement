package workspace

import (
	"fmt"
	"sort"
	"strings"
)

type WorkspaceCompletionBlockedError struct {
	blockers []string
}

func (failure WorkspaceCompletionBlockedError) Error() string {
	return "workspace completion blocked: " +
		strings.Join(failure.blockers, "; ")
}

func (failure WorkspaceCompletionBlockedError) Blockers() []string {
	return append([]string(nil), failure.blockers...)
}

type workspaceCompletionAssessment struct {
	chain    []MergeUnitIntegrationIntent
	blockers []string
}

func assessWorkspaceCompletion(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	reviews ReviewRuntimeProjection,
	runtime WorkspaceRuntimeProjection,
) workspaceCompletionAssessment {
	blockers := make([]string, 0)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			blockers = append(blockers, value)
		}
	}
	if runtime.workspaceID != definition.workspace.id ||
		runtime.activeGeneration != definition.generation {
		add("workspace:generation_mismatch")
	}
	target, targetReady := runtime.LocalTarget()
	if !targetReady {
		add("local_effect:feature_ref_intent_missing")
	} else if !target.Created() {
		add("local_effect:feature_ref_creation_pending")
	}

	dependencies, references := definitionDependencyGraph(definition)
	known := make(map[string]MergeUnitReference, len(references))
	for _, reference := range references {
		known[reference.key()] = reference
	}
	completedByUnit := make(
		map[string][]RuntimeAttemptProjection, len(references),
	)
	for _, attempt := range runtime.attempts {
		if attempt.phase.nonterminal() {
			add(fmt.Sprintf(
				"attempt:%s:%s", attempt.attemptID, attempt.phase,
			))
		}
		if !attempt.leaseID.IsZero() {
			add(fmt.Sprintf(
				"attempt:%s:lease_held", attempt.attemptID,
			))
		}
		if attempt.serialSegmentHeld {
			add(fmt.Sprintf(
				"attempt:%s:serial_segment_held", attempt.attemptID,
			))
		}
		if attempt.integration != nil &&
			!attempt.integration.Integrated() {
			add(fmt.Sprintf(
				"local_effect:integration_pending:%s",
				attempt.attemptID,
			))
		}
		if attempt.phase == AttemptCompleted {
			if attempt.integration == nil ||
				!attempt.integration.Integrated() {
				add(fmt.Sprintf(
					"attempt:%s:completion_without_integration",
					attempt.attemptID,
				))
				continue
			}
			completedByUnit[attempt.mergeUnit.key()] = append(
				completedByUnit[attempt.mergeUnit.key()],
				attempt,
			)
		} else if attempt.integration != nil &&
			attempt.integration.Integrated() {
			add(fmt.Sprintf(
				"attempt:%s:integration_without_completion",
				attempt.attemptID,
			))
		}
	}

	selected := make(
		map[string]RuntimeAttemptProjection, len(references),
	)
	for _, reference := range references {
		attempts := completedByUnit[reference.key()]
		switch len(attempts) {
		case 0:
			add("merge_unit:" + reference.String() + ":not_integrated")
		case 1:
			selected[reference.key()] = attempts[0]
			if err := validateCompletionAttempt(
				snapshot, definition, reviews, attempts[0],
			); err != nil {
				add(
					"merge_unit:" + reference.String() + ":" +
						err.Error(),
				)
			}
		default:
			add(
				"merge_unit:" + reference.String() +
					":multiple_integrations",
			)
		}
	}
	for key, attempts := range completedByUnit {
		if _, exists := known[key]; !exists && len(attempts) != 0 {
			add(
				"merge_unit:" +
					attempts[0].mergeUnit.String() +
					":not_in_definition",
			)
		}
	}

	for unitKey, required := range dependencies {
		unit, exists := selected[unitKey]
		if !exists {
			continue
		}
		for _, dependency := range required {
			prior, exists := selected[dependency.key()]
			if !exists {
				continue
			}
			if prior.integration.integratedRecord >=
				unit.integration.integratedRecord {
				add(
					"merge_unit:" + unit.mergeUnit.String() +
						":dependency_order:" +
						dependency.String(),
				)
			}
		}
	}

	type transition struct {
		record uint64
		intent MergeUnitIntegrationIntent
	}
	transitions := make([]transition, 0, len(selected))
	for _, attempt := range selected {
		transitions = append(transitions, transition{
			record: attempt.integration.integratedRecord,
			intent: attempt.integration.intent,
		})
	}
	sort.Slice(transitions, func(i, j int) bool {
		return transitions[i].record < transitions[j].record
	})
	chain := make(
		[]MergeUnitIntegrationIntent, 0, len(transitions),
	)
	var previousRecord uint64
	var previousMerge GitObjectID
	for index, transition := range transitions {
		switch {
		case transition.record == 0:
			add("integration_frontier:zero_record")
		case previousRecord != 0 &&
			transition.record <= previousRecord:
			add("integration_frontier:unordered_records")
		}
		if index == 0 {
			if targetReady && target.Created() &&
				transition.intent.expectedFeatureHead !=
					target.binding.baseCommit {
				add("integration_frontier:wrong_initial_parent")
			}
		} else if transition.intent.expectedFeatureHead !=
			previousMerge {
			add("integration_frontier:not_first_parent_contiguous")
		}
		previousRecord = transition.record
		previousMerge = transition.intent.expectedMerge
		chain = append(chain, transition.intent)
	}
	if targetReady && target.Created() {
		if len(transitions) == 0 {
			add("integration_frontier:empty")
		} else {
			frontier := transitions[len(transitions)-1]
			if frontier.record != target.headRecord ||
				frontier.intent.expectedMerge != target.createdHead {
				add("integration_frontier:durable_head_mismatch")
			}
		}
	}
	blockers = sortedUniqueCompletionBlockers(blockers)
	return workspaceCompletionAssessment{
		chain:    chain,
		blockers: blockers,
	}
}

func validateCompletionAttempt(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	reviews ReviewRuntimeProjection,
	attempt RuntimeAttemptProjection,
) error {
	if attempt.phase != AttemptCompleted ||
		attempt.integration == nil ||
		!attempt.integration.Integrated() {
		return fmt.Errorf("not_completed")
	}
	intent := attempt.integration.intent
	if intent.workspaceID != definition.workspace.id ||
		intent.generation != definition.generation ||
		intent.attemptID != attempt.attemptID ||
		intent.mergeUnit != attempt.mergeUnit ||
		intent.expectedFeatureHead != attempt.base ||
		intent.acceptedHead != attempt.verifiedHead {
		return fmt.Errorf("integration_binding_mismatch")
	}
	for _, boundary := range attempt.boundaries {
		if boundary.resumedRecord == 0 {
			return fmt.Errorf("unresolved_boundary:%s", boundary.boundaryID)
		}
	}
	unit, err := executionForMergeUnit(
		definition.execution, attempt.mergeUnit,
	)
	if err != nil {
		return fmt.Errorf("execution_missing")
	}
	loop, reviewConfigured := unit.ReviewLoop()
	if reviewConfigured {
		state, exists := reviews.State(attempt.attemptID)
		if !exists || !state.MergeReady() ||
			state.loop.digest != loop.digest ||
			state.head != intent.acceptedHead ||
			state.tree != intent.acceptedTree {
			return fmt.Errorf("review_readiness_mismatch")
		}
		if err := validateAttemptReviewProtocolState(
			definition, unit, attempt, state, true, false,
		); err != nil {
			return fmt.Errorf("review_protocol_mismatch")
		}
		readiness, err := newReviewMergeReadiness(
			definition, attempt, state,
		)
		if err != nil ||
			intent.acceptanceMode !=
				IntegrationAcceptanceReviewReady ||
			intent.reviewReadinessDigest != readiness.digest ||
			!intent.adoptedHeadEventDigest.IsZero() {
			return fmt.Errorf("review_readiness_digest_mismatch")
		}
		return nil
	}
	if _, exists := reviews.State(attempt.attemptID); exists {
		return fmt.Errorf("unconfigured_review_evidence")
	}
	repository, err := NewReviewRepositorySnapshot(
		intent.acceptedHead, intent.acceptedTree, true,
	)
	if err != nil {
		return fmt.Errorf("accepted_head_tree_invalid")
	}
	adopted, exists := exactAdoptedHeadRecord(
		snapshot, attempt.attemptID, repository,
	)
	if !exists ||
		intent.acceptanceMode !=
			IntegrationAcceptanceAdoptedHead ||
		intent.adoptedHeadEventDigest != adopted.eventHash ||
		!intent.reviewReadinessDigest.IsZero() {
		return fmt.Errorf("adopted_head_evidence_mismatch")
	}
	return nil
}

func sortedUniqueCompletionBlockers(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func workspaceCompletionViewState(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	assessment workspaceCompletionAssessment,
	runtime WorkspaceRuntimeProjection,
) ([]string, bool, Digest, error) {
	blockers := append([]string(nil), assessment.blockers...)
	completion, recorded := runtime.Completion()
	if !recorded {
		if len(blockers) == 0 {
			blockers = append(
				blockers, "workspace_completion_not_recorded",
			)
		}
		return sortedUniqueCompletionBlockers(blockers),
			false, Digest{}, nil
	}
	expected, err := preCompletionReportDigest(
		snapshot, definition, completion.Record(),
	)
	if err != nil {
		return nil, false, Digest{}, err
	}
	if expected != completion.ReportDigest() {
		blockers = append(
			blockers,
			"workspace_completion_report_digest_mismatch",
		)
	}
	blockers = sortedUniqueCompletionBlockers(blockers)
	return blockers, len(blockers) == 0,
		completion.ReportDigest(), nil
}

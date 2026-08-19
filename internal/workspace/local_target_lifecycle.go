package workspace

import (
	"context"
	"fmt"
	"time"
)

type LocalTargetInitializationFaultPoint string

const (
	LocalTargetFaultAfterIntentSynced LocalTargetInitializationFaultPoint = "after_intent_synced"
	LocalTargetFaultBeforeRefUpdate   LocalTargetInitializationFaultPoint = "before_ref_update"
	LocalTargetFaultAfterRefUpdate    LocalTargetInitializationFaultPoint = "after_ref_update"
	LocalTargetFaultBeforeCompletion  LocalTargetInitializationFaultPoint = "before_completion"
)

type LocalTargetInitializationFaultInjector func(
	LocalTargetInitializationFaultPoint,
) error

func initializeLocalTarget(
	ctx context.Context,
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	occurredAt time.Time,
	adapter LocalTargetGitAdapter,
	fault LocalTargetInitializationFaultInjector,
) (JournalSnapshot, error) {
	if ctx == nil || journal == nil || definition.generation.IsZero() ||
		occurredAt.IsZero() || definition.workspace.target.IsZero() {
		return JournalSnapshot{}, fmt.Errorf(
			"local target initialization requires context, journal, definition, and occurrence time",
		)
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalSnapshot{}, err
	}
	if runtime.workspaceID != definition.workspace.id ||
		runtime.activeGeneration != definition.generation {
		return JournalSnapshot{}, fmt.Errorf(
			"local target initialization does not match the active workspace",
		)
	}

	targetProjection, hasTarget := runtime.LocalTarget()
	if !hasTarget {
		inspection, err := adapter.inspectUncreatedTarget(
			ctx, definition.workspace.target,
		)
		if err != nil {
			return JournalSnapshot{}, err
		}
		intent, err := NewFeatureRefCreationIntendedJournalEvent(
			definition.workspace.id,
			definition.generation,
			inspection.binding,
		)
		if err != nil {
			return JournalSnapshot{}, err
		}
		appendRequest, err := localTargetJournalAppend(
			intent, occurredAt,
		)
		if err != nil {
			return JournalSnapshot{}, err
		}
		if _, err := journal.Append(appendRequest); err != nil {
			return JournalSnapshot{}, err
		}
		if err := injectLocalTargetInitializationFault(
			fault, LocalTargetFaultAfterIntentSynced,
		); err != nil {
			return JournalSnapshot{}, err
		}
		snapshot, err = journal.ReadSnapshot()
		if err != nil {
			return JournalSnapshot{}, err
		}
		runtime, err = RebuildWorkspaceRuntime(snapshot)
		if err != nil {
			return JournalSnapshot{}, err
		}
		targetProjection, hasTarget = runtime.LocalTarget()
		if !hasTarget {
			return JournalSnapshot{}, fmt.Errorf(
				"durable feature-ref creation intent is missing after append",
			)
		}
	}

	binding := targetProjection.binding
	if binding.root != definition.workspace.target.root ||
		binding.baseRef != definition.workspace.target.baseRef ||
		binding.baseCommit != definition.workspace.target.baseCommit ||
		binding.featureBranch != definition.workspace.target.featureBranch {
		return JournalSnapshot{}, fmt.Errorf(
			"durable local target binding does not match the active workspace definition",
		)
	}
	if targetProjection.Created() {
		expectedMarker, err := expectedLocalTargetReflogMarker(
			runtime, targetProjection,
		)
		if err != nil {
			return JournalSnapshot{}, err
		}
		if _, err := adapter.verifyOwnedFeatureRefAt(
			ctx, binding, targetProjection.createdHead,
			expectedMarker,
		); err != nil {
			return JournalSnapshot{}, err
		}
		return snapshot, nil
	}

	inspection, err := adapter.inspectIntendedTarget(
		ctx, binding, targetProjection.intentDigest,
	)
	if err != nil {
		return JournalSnapshot{}, err
	}
	session, err := adapter.openBoundSession(binding)
	if err != nil {
		return JournalSnapshot{}, err
	}
	defer session.Close()
	inspection, err = session.inspectOwnedState(
		ctx, targetProjection.intentDigest, true,
	)
	if err != nil {
		return JournalSnapshot{}, err
	}
	if !inspection.featureRefExists {
		if err := injectLocalTargetInitializationFault(
			fault, LocalTargetFaultBeforeRefUpdate,
		); err != nil {
			return JournalSnapshot{}, err
		}
		inspection, err = session.createFeatureRef(
			ctx, targetProjection.intentDigest,
		)
		if err != nil {
			return JournalSnapshot{}, err
		}
		if err := injectLocalTargetInitializationFault(
			fault, LocalTargetFaultAfterRefUpdate,
		); err != nil {
			return JournalSnapshot{}, err
		}
	}
	verified, err := session.inspectOwnedState(
		ctx, targetProjection.intentDigest, true,
	)
	if err != nil {
		return JournalSnapshot{}, err
	}
	if !verified.featureRefExists ||
		verified.featureHead != binding.baseCommit {
		return JournalSnapshot{}, fmt.Errorf(
			"feature ref %s was not created at pinned base %s",
			binding.featureRef, binding.baseCommit,
		)
	}
	if err := injectLocalTargetInitializationFault(
		fault, LocalTargetFaultBeforeCompletion,
	); err != nil {
		return JournalSnapshot{}, err
	}
	if _, err := session.inspectOwnedState(
		ctx, targetProjection.intentDigest, true,
	); err != nil {
		return JournalSnapshot{}, err
	}
	completion, err := NewFeatureRefCreatedJournalEvent(
		definition.workspace.id,
		definition.generation,
		targetProjection.intentDigest,
		binding.featureRef,
		binding.baseCommit,
	)
	if err != nil {
		return JournalSnapshot{}, err
	}
	appendRequest, err := localTargetJournalAppend(
		completion, occurredAt,
	)
	if err != nil {
		return JournalSnapshot{}, err
	}
	if _, err := journal.Append(appendRequest); err != nil {
		return JournalSnapshot{}, err
	}
	snapshot, err = journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, err
	}
	runtime, err = RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalSnapshot{}, err
	}
	completed, ok := runtime.LocalTarget()
	if !ok || !completed.Created() ||
		completed.intentDigest != targetProjection.intentDigest ||
		completed.createdHead != binding.baseCommit {
		return JournalSnapshot{}, fmt.Errorf(
			"feature-ref creation completion did not replay exactly",
		)
	}
	return snapshot, nil
}

func expectedLocalTargetReflogMarker(
	runtime WorkspaceRuntimeProjection,
	target RuntimeLocalTargetProjection,
) (string, error) {
	if target.createdRecord == 0 || target.headRecord == 0 ||
		target.createdHead.IsZero() {
		return "", fmt.Errorf(
			"durable local target has no exact feature-head record",
		)
	}
	if target.headRecord == target.createdRecord {
		if target.createdHead != target.binding.baseCommit {
			return "", fmt.Errorf(
				"initial local target head does not match its bound base",
			)
		}
		return localTargetReflogMessage(target.intentDigest), nil
	}
	for _, attempt := range runtime.attempts {
		if attempt.integration == nil ||
			attempt.integration.integratedRecord != target.headRecord {
			continue
		}
		intent := attempt.integration.intent
		if intent.featureRef != target.binding.featureRef ||
			intent.expectedMerge != target.createdHead {
			return "", fmt.Errorf(
				"local target feature-head record does not match its integration",
			)
		}
		return integrationReflogMessage(intent.digest), nil
	}
	return "", fmt.Errorf(
		"local target feature head has no exact durable transition",
	)
}

func localTargetJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalAppend, error) {
	if !isLocalTargetJournalEvent(event) {
		return JournalAppend{}, fmt.Errorf(
			"local target append requires a local target journal event",
		)
	}
	return NewJournalAppend(event, occurredAt)
}

func injectLocalTargetInitializationFault(
	injector LocalTargetInitializationFaultInjector,
	point LocalTargetInitializationFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf(
			"local target initialization fault at %s: %w", point, err,
		)
	}
	return nil
}

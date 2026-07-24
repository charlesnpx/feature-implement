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
			intent, occurredAt, snapshot,
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
		if _, err := adapter.verifyOwnedFeatureRef(
			ctx, binding, targetProjection.intentDigest,
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
	if !inspection.featureRefExists {
		if err := injectLocalTargetInitializationFault(
			fault, LocalTargetFaultBeforeRefUpdate,
		); err != nil {
			return JournalSnapshot{}, err
		}
		if err := adapter.createFeatureRef(
			ctx, binding, targetProjection.intentDigest,
		); err != nil {
			return JournalSnapshot{}, err
		}
		if err := injectLocalTargetInitializationFault(
			fault, LocalTargetFaultAfterRefUpdate,
		); err != nil {
			return JournalSnapshot{}, err
		}
	}
	verified, err := adapter.verifyOwnedFeatureRef(
		ctx, binding, targetProjection.intentDigest,
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
		completion, occurredAt, snapshot,
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

func localTargetJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
	snapshot JournalSnapshot,
) (JournalAppend, error) {
	reads, writes, ok := localTargetJournalEventResources(event)
	if !ok {
		return JournalAppend{}, fmt.Errorf(
			"local target append requires a local target journal event",
		)
	}
	revisions := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(
			resource, snapshot.Revision(resource),
		)
		revisions = append(revisions, revision)
	}
	return newPrivilegedJournalAppend(
		event, occurredAt, revisions, writes,
	)
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

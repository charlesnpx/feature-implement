package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type AbandonWorkspaceRequest struct {
	OccurredAt time.Time
	Reason     string
}

type WorkspaceAbandonmentResult struct {
	abandonment RuntimeWorkspaceAbandonmentProjection
	record      JournalRecord
}

func (result WorkspaceAbandonmentResult) Abandonment() RuntimeWorkspaceAbandonmentProjection {
	return result.abandonment
}

func (result WorkspaceAbandonmentResult) Record() JournalRecord { return result.record }

// AbandonWorkspace records final runtime state and, when the durable local
// target created a feature ref, records its release without moving that ref.
func AbandonWorkspace(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git LocalTargetGitAdapter,
	request AbandonWorkspaceRequest,
) (WorkspaceAbandonmentResult, error) {
	if ctx == nil || journal == nil || request.OccurredAt.IsZero() {
		return WorkspaceAbandonmentResult{}, fmt.Errorf(
			"workspace abandonment requires context, journal, and occurrence time",
		)
	}
	reason := strings.TrimSpace(request.Reason)
	if err := validateBoundedText("workspace abandonment reason", reason, 16*1024); err != nil {
		return WorkspaceAbandonmentResult{}, err
	}

	snapshot, _, runtime, err := readCompletionRuntime(journal, definition)
	if err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	target, exists := runtime.LocalTarget()
	if !exists {
		return WorkspaceAbandonmentResult{}, fmt.Errorf(
			"workspace abandonment requires a durable local target intent",
		)
	}
	if _, completed := runtime.Completion(); completed {
		return WorkspaceAbandonmentResult{}, fmt.Errorf(
			"workspace abandonment is not allowed after workspace completion",
		)
	}
	if abandonment, exists := runtime.Abandonment(); exists {
		if err := verifyRecordedWorkspaceAbandonment(
			ctx, snapshot, runtime, target, abandonment, reason, git,
		); err != nil {
			return WorkspaceAbandonmentResult{}, err
		}
		return WorkspaceAbandonmentResult{abandonment: abandonment}, nil
	}

	featureHead := GitObjectID{}
	if target.Created() {
		expectedMarker, err := expectedLocalTargetReflogMarker(runtime, target)
		if err != nil {
			return WorkspaceAbandonmentResult{}, err
		}
		if _, err := git.verifyOwnedFeatureRefAt(
			ctx,
			target.binding,
			target.createdHead,
			expectedMarker,
		); err != nil {
			return WorkspaceAbandonmentResult{}, fmt.Errorf(
				"feature ref changed from its exact owned head and marker before release: %w",
				err,
			)
		}
		featureHead = target.createdHead
	}

	event, err := NewWorkspaceAbandonedJournalEvent(
		runtime.workspaceID,
		runtime.activeGeneration,
		target.binding.featureRef,
		featureHead,
		reason,
	)
	if err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	appendRequest, err := localTargetJournalAppend(
		event, request.OccurredAt, snapshot,
	)
	if err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	prospective, err := buildJournalRecord(snapshot, appendRequest)
	if err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	if _, err := reduceWorkspaceRuntime(runtime, prospective); err != nil {
		return WorkspaceAbandonmentResult{}, fmt.Errorf(
			"validate workspace abandonment transition: %w",
			err,
		)
	}
	record, err := journal.AppendIfHead(appendRequest, snapshot.head)
	if err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	if target.Created() {
		if err := git.EnsureReleasedFeatureRefMarker(
			ctx, target.binding, target.createdHead, target.intentDigest,
		); err != nil {
			return WorkspaceAbandonmentResult{}, err
		}
	}

	completedSnapshot, _, completedRuntime, err := readCompletionRuntime(
		journal, definition,
	)
	if err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	abandonment, exists := completedRuntime.Abandonment()
	if !exists || abandonment.Record() != record.sequence ||
		abandonment.EventDigest() != record.eventHash ||
		abandonment.FeatureRef() != target.binding.featureRef ||
		abandonment.FeatureHead() != featureHead || abandonment.Reason() != reason {
		return WorkspaceAbandonmentResult{}, fmt.Errorf(
			"workspace abandonment did not replay exactly",
		)
	}
	completedTarget, exists := completedRuntime.LocalTarget()
	if !exists {
		return WorkspaceAbandonmentResult{}, fmt.Errorf(
			"workspace abandonment lost its durable local target",
		)
	}
	if err := verifyRecordedWorkspaceAbandonment(
		ctx,
		completedSnapshot,
		completedRuntime,
		completedTarget,
		abandonment,
		reason,
		git,
	); err != nil {
		return WorkspaceAbandonmentResult{}, err
	}
	return WorkspaceAbandonmentResult{abandonment: abandonment, record: record}, nil
}

func reduceWorkspaceAbandonmentRuntime(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
	event WorkspaceAbandonedJournalEvent,
) error {
	if next == nil || current.workspaceID.IsZero() || current.activeGeneration.IsZero() {
		return fmt.Errorf("workspace abandonment requires an initialized runtime")
	}
	if current.completion != nil {
		return fmt.Errorf("workspace abandonment is not allowed after workspace completion")
	}
	if current.abandonment != nil {
		return fmt.Errorf("workspace abandonment is already recorded")
	}
	if record.generation != current.activeGeneration ||
		event.workspaceID != current.workspaceID ||
		event.generation != current.activeGeneration {
		return fmt.Errorf("workspace abandonment does not match the active generation")
	}
	target, exists := current.LocalTarget()
	if !exists || target.binding.featureRef != event.featureRef {
		return fmt.Errorf("workspace abandonment does not match the durable local target")
	}
	if target.Created() {
		if event.featureHead != target.createdHead {
			return fmt.Errorf("workspace abandonment does not match the durable feature frontier")
		}
	} else if !event.featureHead.IsZero() {
		return fmt.Errorf("workspace abandonment cannot release an uncreated feature ref")
	}
	next.abandonment = &RuntimeWorkspaceAbandonmentProjection{
		featureRef:  event.featureRef,
		featureHead: event.featureHead,
		reason:      event.reason,
		record:      record.sequence,
		eventDigest: record.eventHash,
	}
	return nil
}

func verifyRecordedWorkspaceAbandonment(
	ctx context.Context,
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
	target RuntimeLocalTargetProjection,
	abandonment RuntimeWorkspaceAbandonmentProjection,
	reason string,
	git LocalTargetGitAdapter,
) error {
	if abandonment.Record() == 0 ||
		abandonment.Record() > uint64(len(snapshot.records)) {
		return fmt.Errorf("workspace abandonment record is outside the journal")
	}
	record := snapshot.records[abandonment.Record()-1]
	event, ok := record.event.(WorkspaceAbandonedJournalEvent)
	if !ok || record.eventHash != abandonment.EventDigest() {
		return fmt.Errorf("workspace abandonment record does not contain workspace_abandoned")
	}
	if event.workspaceID != runtime.workspaceID ||
		event.generation != runtime.activeGeneration ||
		event.featureRef != abandonment.FeatureRef() ||
		event.featureHead != abandonment.FeatureHead() ||
		event.reason != abandonment.Reason() {
		return fmt.Errorf("workspace abandonment record does not match its projection")
	}
	if abandonment.Reason() != reason {
		return fmt.Errorf(
			"workspace abandonment reason %q does not match recorded reason %q",
			reason,
			abandonment.Reason(),
		)
	}
	if abandonment.FeatureRef() != target.binding.featureRef {
		return fmt.Errorf("workspace abandonment does not match the durable local target")
	}
	if target.Created() != abandonment.Released() ||
		(target.Created() && abandonment.FeatureHead() != target.createdHead) {
		return fmt.Errorf("workspace abandonment does not match the durable feature frontier")
	}
	if !abandonment.Released() {
		return nil
	}
	if err := git.EnsureReleasedFeatureRefMarker(
		ctx,
		target.binding,
		abandonment.FeatureHead(),
		target.intentDigest,
	); err != nil {
		return fmt.Errorf("reconcile released feature ref: %w", err)
	}
	if _, err := git.VerifyReleasedFeatureRefAt(
		ctx,
		target.binding,
		abandonment.FeatureHead(),
		target.intentDigest,
	); err != nil {
		return fmt.Errorf("verify released feature ref: %w", err)
	}
	return nil
}

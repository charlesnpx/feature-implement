package workspace

import (
	"context"
	"fmt"
	"time"
)

type CompletionLifecycleFaultPoint string

const (
	CompletionFaultBeforeAppend CompletionLifecycleFaultPoint = "before_append"
	CompletionFaultAfterAppend  CompletionLifecycleFaultPoint = "after_append"
)

type CompletionLifecycleFaultInjector func(
	CompletionLifecycleFaultPoint,
) error

type CompleteWorkspaceRequest struct {
	OccurredAt time.Time
	Fault      CompletionLifecycleFaultInjector
}

type WorkspaceCompletionResult struct {
	completion RuntimeWorkspaceCompletionProjection
	record     JournalRecord
}

func (result WorkspaceCompletionResult) Completion() RuntimeWorkspaceCompletionProjection {
	return result.completion
}

func (result WorkspaceCompletionResult) Record() JournalRecord {
	return result.record
}

func CompleteWorkspace(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git IntegrationGitPort,
	request CompleteWorkspaceRequest,
) (WorkspaceCompletionResult, error) {
	if ctx == nil || journal == nil || git == nil ||
		request.OccurredAt.IsZero() {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"workspace completion requires context, journal, Git adapter, and occurrence time",
		)
	}
	snapshot, reviews, runtime, err := readCompletionRuntime(
		journal, definition,
	)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	assessment := assessWorkspaceCompletion(
		snapshot, definition, reviews, runtime,
	)
	if len(assessment.blockers) != 0 {
		return WorkspaceCompletionResult{},
			WorkspaceCompletionBlockedError{
				blockers: assessment.blockers,
			}
	}
	target, exists := runtime.LocalTarget()
	if !exists || !target.Created() {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"workspace completion requires a durable local target",
		)
	}
	if err := git.VerifyCompletedIntegration(
		ctx, target.binding, assessment.chain,
	); err != nil {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"verify exact completed integration frontier: %w", err,
		)
	}

	if completed, exists := runtime.Completion(); exists {
		if err := verifyRecordedWorkspaceCompletion(
			snapshot, definition, completed,
		); err != nil {
			return WorkspaceCompletionResult{}, err
		}
		return WorkspaceCompletionResult{
			completion: completed,
		}, nil
	}

	report, err := RebuildWorkspaceReport(snapshot, definition)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	reportDigest, err := ParseDigest(report.ReportDigest)
	if err != nil {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"parse canonical pre-completion report digest: %w", err,
		)
	}
	event, err := NewWorkspaceCompletedJournalEvent(
		runtime.workspaceID,
		runtime.activeGeneration,
		target.binding.featureRef,
		target.createdHead,
		reportDigest,
	)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	appendRequest, err := completionJournalAppend(
		event, request.OccurredAt, snapshot,
	)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	prospective, err := buildJournalRecord(snapshot, appendRequest)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	if _, err := reduceWorkspaceRuntime(
		runtime, prospective,
	); err != nil {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"validate workspace completion transition: %w", err,
		)
	}
	if err := injectCompletionLifecycleFault(
		request.Fault, CompletionFaultBeforeAppend,
	); err != nil {
		return WorkspaceCompletionResult{}, err
	}
	record, err := journal.AppendIfHead(appendRequest, snapshot.head)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	if err := injectCompletionLifecycleFault(
		request.Fault, CompletionFaultAfterAppend,
	); err != nil {
		return WorkspaceCompletionResult{}, err
	}

	completedSnapshot, completedReviews, completedRuntime, err :=
		readCompletionRuntime(journal, definition)
	if err != nil {
		return WorkspaceCompletionResult{}, err
	}
	completed, exists := completedRuntime.Completion()
	if !exists || completed.Record() != record.sequence ||
		completed.EventDigest() != record.eventHash ||
		completed.FeatureRef() != target.binding.featureRef ||
		completed.FeatureHead() != target.createdHead ||
		completed.ReportDigest() != reportDigest {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"workspace completion did not replay exactly",
		)
	}
	completedAssessment := assessWorkspaceCompletion(
		completedSnapshot, definition,
		completedReviews, completedRuntime,
	)
	if len(completedAssessment.blockers) != 0 {
		return WorkspaceCompletionResult{},
			WorkspaceCompletionBlockedError{
				blockers: completedAssessment.blockers,
			}
	}
	if err := verifyRecordedWorkspaceCompletion(
		completedSnapshot, definition, completed,
	); err != nil {
		return WorkspaceCompletionResult{}, err
	}
	if err := git.VerifyCompletedIntegration(
		ctx, target.binding, completedAssessment.chain,
	); err != nil {
		return WorkspaceCompletionResult{}, fmt.Errorf(
			"reverify exact completed integration frontier: %w", err,
		)
	}
	return WorkspaceCompletionResult{
		completion: completed,
		record:     record,
	}, nil
}

func readCompletionRuntime(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (
	JournalSnapshot,
	ReviewRuntimeProjection,
	WorkspaceRuntimeProjection,
	error,
) {
	if journal == nil || definition.workspace.id.IsZero() ||
		definition.generation.IsZero() {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{},
			fmt.Errorf(
				"workspace completion requires journal and effective definition",
			)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	reviews, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	runtime := reviews.core
	if runtime.workspaceID != definition.workspace.id ||
		runtime.activeGeneration != definition.generation {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{},
			fmt.Errorf(
				"completion definition does not match the active workspace generation",
			)
	}
	if err := verifyWorkspaceWorktreeRootBinding(
		runtime.worktreeRoot,
	); err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	return snapshot, reviews, runtime, nil
}

func completionJournalAppend(
	event WorkspaceCompletedJournalEvent,
	occurredAt time.Time,
	snapshot JournalSnapshot,
) (JournalAppend, error) {
	reads, writes, ok := completionJournalEventResources(event)
	if !ok {
		return JournalAppend{}, fmt.Errorf(
			"completion append requires a workspace completion event",
		)
	}
	readSet := make(
		[]JournalResourceRevision, 0, len(reads),
	)
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(
			resource, snapshot.Revision(resource),
		)
		readSet = append(readSet, revision)
	}
	return newPrivilegedJournalAppend(
		event, occurredAt, readSet, writes,
	)
}

func verifyRecordedWorkspaceCompletion(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	completion RuntimeWorkspaceCompletionProjection,
) error {
	reportDigest, err := preCompletionReportDigest(
		snapshot, definition, completion.Record(),
	)
	if err != nil {
		return err
	}
	if reportDigest != completion.ReportDigest() {
		return fmt.Errorf(
			"workspace completion report digest is %s, canonical pre-completion report is %s",
			completion.ReportDigest(), reportDigest,
		)
	}
	return nil
}

func preCompletionReportDigest(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	completionRecord uint64,
) (Digest, error) {
	if completionRecord == 0 ||
		completionRecord > uint64(len(snapshot.records)) {
		return Digest{}, fmt.Errorf(
			"workspace completion record is outside the journal",
		)
	}
	completionIndex := int(completionRecord - 1)
	completionEvent := snapshot.records[completionIndex].event
	if _, ok := completionEvent.(WorkspaceCompletedJournalEvent); !ok {
		return Digest{}, fmt.Errorf(
			"workspace completion record does not contain workspace_completed",
		)
	}
	prefix := emptyJournalSnapshot()
	prefix.records = append(
		[]JournalRecord(nil),
		snapshot.records[:completionIndex]...,
	)
	for _, record := range prefix.records {
		prefix.head = record.eventHash
		prefix.revisions = applyJournalWrites(
			prefix.revisions, record.writeSet,
		)
	}
	report, err := RebuildWorkspaceReport(prefix, definition)
	if err != nil {
		return Digest{}, fmt.Errorf(
			"rebuild canonical pre-completion report: %w", err,
		)
	}
	digest, err := ParseDigest(report.ReportDigest)
	if err != nil {
		return Digest{}, fmt.Errorf(
			"parse canonical pre-completion report digest: %w", err,
		)
	}
	return digest, nil
}

func injectCompletionLifecycleFault(
	injector CompletionLifecycleFaultInjector,
	point CompletionLifecycleFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf(
			"completion lifecycle fault at %s: %w", point, err,
		)
	}
	return nil
}

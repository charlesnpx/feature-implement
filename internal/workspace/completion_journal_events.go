package workspace

import (
	"fmt"
	"strings"
)

// WorkspaceCompletedJournalEvent records that the complete local predicate was
// verified against one exact feature frontier and one canonical pre-completion
// report. It is final local workflow state.
type WorkspaceCompletedJournalEvent struct {
	workspaceID  ID
	generation   Digest
	featureRef   string
	featureHead  GitObjectID
	reportDigest Digest
}

func NewWorkspaceCompletedJournalEvent(
	workspaceID ID,
	generation Digest,
	featureRef string,
	featureHead GitObjectID,
	reportDigest Digest,
) (WorkspaceCompletedJournalEvent, error) {
	event := WorkspaceCompletedJournalEvent{
		workspaceID:  workspaceID,
		generation:   generation,
		featureRef:   strings.TrimSpace(featureRef),
		featureHead:  featureHead,
		reportDigest: reportDigest,
	}
	if err := event.validate(); err != nil {
		return WorkspaceCompletedJournalEvent{}, err
	}
	return event, nil
}

func (WorkspaceCompletedJournalEvent) isWorkspaceJournalEvent() {}
func (WorkspaceCompletedJournalEvent) eventType() JournalEventType {
	return JournalEventWorkspaceCompleted
}
func (event WorkspaceCompletedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event WorkspaceCompletedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.featureHead.IsZero() || event.reportDigest.IsZero() {
		return fmt.Errorf(
			"workspace completion requires workspace, generation, feature head, and local report digest",
		)
	}
	normalized, err := normalizeFullyQualifiedBaseRef(event.featureRef)
	if err != nil || normalized != event.featureRef ||
		!strings.HasPrefix(event.featureRef, "refs/heads/feature/") {
		return fmt.Errorf(
			"workspace completion requires the exact owned feature ref",
		)
	}
	return nil
}

func (event WorkspaceCompletedJournalEvent) WorkspaceID() ID {
	return event.workspaceID
}
func (event WorkspaceCompletedJournalEvent) Generation() Digest {
	return event.generation
}
func (event WorkspaceCompletedJournalEvent) FeatureRef() string {
	return event.featureRef
}
func (event WorkspaceCompletedJournalEvent) FeatureHead() GitObjectID {
	return event.featureHead
}
func (event WorkspaceCompletedJournalEvent) ReportDigest() Digest {
	return event.reportDigest
}

func isCompletionJournalEvent(event WorkspaceJournalEvent) bool {
	_, ok := event.(WorkspaceCompletedJournalEvent)
	return ok
}

func completionJournalEventResources(
	event WorkspaceJournalEvent,
) ([]JournalResource, []JournalResource, bool) {
	completed, ok := event.(WorkspaceCompletedJournalEvent)
	if !ok {
		return nil, nil, false
	}
	reads := []JournalResource{
		WorkspaceJournalResource(completed.workspaceID),
		GenerationJournalResource(completed.generation),
		featureRefJournalResource(
			completed.workspaceID, completed.featureRef,
		),
		CompletionJournalResource(completed.workspaceID),
	}
	writes := []JournalResource{
		CompletionJournalResource(completed.workspaceID),
	}
	return reads, writes, true
}

func cloneCompletionJournalEvent(
	event WorkspaceJournalEvent,
) WorkspaceJournalEvent {
	if completed, ok := event.(WorkspaceCompletedJournalEvent); ok {
		return completed
	}
	return nil
}

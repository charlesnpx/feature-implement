package workspace

import "fmt"

type MergeUnitIntegrationIntendedJournalEvent struct {
	intent MergeUnitIntegrationIntent
}

func NewMergeUnitIntegrationIntendedJournalEvent(
	intent MergeUnitIntegrationIntent,
) (MergeUnitIntegrationIntendedJournalEvent, error) {
	event := MergeUnitIntegrationIntendedJournalEvent{intent: intent}
	if err := event.validate(); err != nil {
		return MergeUnitIntegrationIntendedJournalEvent{}, err
	}
	return event, nil
}

func (MergeUnitIntegrationIntendedJournalEvent) isWorkspaceJournalEvent() {}
func (MergeUnitIntegrationIntendedJournalEvent) eventType() JournalEventType {
	return JournalEventMergeUnitIntegrationIntended
}
func (event MergeUnitIntegrationIntendedJournalEvent) boundGeneration() Digest {
	return event.intent.generation
}
func (event MergeUnitIntegrationIntendedJournalEvent) validate() error {
	return event.intent.validate()
}
func (event MergeUnitIntegrationIntendedJournalEvent) Intent() MergeUnitIntegrationIntent {
	return event.intent
}

type MergeUnitIntegratedJournalEvent struct {
	workspaceID         ID
	generation          Digest
	attemptID           ID
	mergeUnit           MergeUnitReference
	intentDigest        Digest
	featureRef          string
	expectedFeatureHead GitObjectID
	acceptedHead        GitObjectID
	acceptedTree        GitObjectID
	mergeCommit         GitObjectID
	leaseID             ID
	serialSegment       ID
}

func NewMergeUnitIntegratedJournalEvent(
	intent MergeUnitIntegrationIntent,
	leaseID ID,
	serialSegment ID,
) (MergeUnitIntegratedJournalEvent, error) {
	if err := intent.validate(); err != nil {
		return MergeUnitIntegratedJournalEvent{}, err
	}
	event := MergeUnitIntegratedJournalEvent{
		workspaceID:         intent.workspaceID,
		generation:          intent.generation,
		attemptID:           intent.attemptID,
		mergeUnit:           intent.mergeUnit,
		intentDigest:        intent.digest,
		featureRef:          intent.featureRef,
		expectedFeatureHead: intent.expectedFeatureHead,
		acceptedHead:        intent.acceptedHead,
		acceptedTree:        intent.acceptedTree,
		mergeCommit:         intent.expectedMerge,
		leaseID:             leaseID,
		serialSegment:       serialSegment,
	}
	if err := event.validate(); err != nil {
		return MergeUnitIntegratedJournalEvent{}, err
	}
	return event, nil
}

func (MergeUnitIntegratedJournalEvent) isWorkspaceJournalEvent() {}
func (MergeUnitIntegratedJournalEvent) eventType() JournalEventType {
	return JournalEventMergeUnitIntegrated
}
func (event MergeUnitIntegratedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event MergeUnitIntegratedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.attemptID.IsZero() || event.mergeUnit.planID.IsZero() ||
		event.mergeUnit.mergeUnitID.IsZero() ||
		event.intentDigest.IsZero() ||
		event.expectedFeatureHead.IsZero() ||
		event.acceptedHead.IsZero() || event.acceptedTree.IsZero() ||
		event.mergeCommit.IsZero() || event.leaseID.IsZero() {
		return fmt.Errorf(
			"merge-unit integration completion requires exact intent, Git, and workspace bindings",
		)
	}
	if event.featureRef == "" {
		return fmt.Errorf(
			"merge-unit integration completion requires its feature ref",
		)
	}
	algorithm := event.expectedFeatureHead.Algorithm()
	for _, object := range []GitObjectID{
		event.acceptedHead, event.acceptedTree, event.mergeCommit,
	} {
		if object.Algorithm() != algorithm {
			return fmt.Errorf(
				"merge-unit integration completion mixes Git object formats",
			)
		}
	}
	return nil
}

func (event MergeUnitIntegratedJournalEvent) WorkspaceID() ID {
	return event.workspaceID
}
func (event MergeUnitIntegratedJournalEvent) Generation() Digest {
	return event.generation
}
func (event MergeUnitIntegratedJournalEvent) AttemptID() ID {
	return event.attemptID
}
func (event MergeUnitIntegratedJournalEvent) MergeUnit() MergeUnitReference {
	return event.mergeUnit
}
func (event MergeUnitIntegratedJournalEvent) IntentDigest() Digest {
	return event.intentDigest
}
func (event MergeUnitIntegratedJournalEvent) FeatureRef() string {
	return event.featureRef
}
func (event MergeUnitIntegratedJournalEvent) ExpectedFeatureHead() GitObjectID {
	return event.expectedFeatureHead
}
func (event MergeUnitIntegratedJournalEvent) AcceptedHead() GitObjectID {
	return event.acceptedHead
}
func (event MergeUnitIntegratedJournalEvent) AcceptedTree() GitObjectID {
	return event.acceptedTree
}
func (event MergeUnitIntegratedJournalEvent) MergeCommit() GitObjectID {
	return event.mergeCommit
}
func (event MergeUnitIntegratedJournalEvent) LeaseID() ID {
	return event.leaseID
}
func (event MergeUnitIntegratedJournalEvent) SerialSegment() ID {
	return event.serialSegment
}

func IntegrationJournalResource(attemptID ID) JournalResource {
	resource, _ := NewJournalResource(
		JournalResourceIntegration,
		attemptID.String()+"/merge-unit",
	)
	return resource
}

func isIntegrationJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case MergeUnitIntegrationIntendedJournalEvent,
		MergeUnitIntegratedJournalEvent:
		return true
	default:
		return false
	}
}

func integrationJournalEventResources(
	event WorkspaceJournalEvent,
) ([]JournalResource, []JournalResource, bool) {
	var workspaceID, attemptID ID
	var generation Digest
	var mergeUnit MergeUnitReference
	var featureRef string
	switch event := event.(type) {
	case MergeUnitIntegrationIntendedJournalEvent:
		workspaceID = event.intent.workspaceID
		generation = event.intent.generation
		attemptID = event.intent.attemptID
		mergeUnit = event.intent.mergeUnit
		featureRef = event.intent.featureRef
	case MergeUnitIntegratedJournalEvent:
		workspaceID = event.workspaceID
		generation = event.generation
		attemptID = event.attemptID
		mergeUnit = event.mergeUnit
		featureRef = event.featureRef
	default:
		return nil, nil, false
	}
	reads := []JournalResource{
		WorkspaceJournalResource(workspaceID),
		GenerationJournalResource(generation),
		AttemptJournalResource(attemptID),
		MergeUnitJournalResource(mergeUnit),
		IntegrationJournalResource(attemptID),
		featureRefJournalResource(workspaceID, featureRef),
		ReviewJournalResource(attemptID),
	}
	writes := []JournalResource{
		AttemptJournalResource(attemptID),
		MergeUnitJournalResource(mergeUnit),
		IntegrationJournalResource(attemptID),
		featureRefJournalResource(workspaceID, featureRef),
	}
	if completed, ok := event.(MergeUnitIntegratedJournalEvent); ok {
		lease := LeaseJournalResource(completed.leaseID)
		reads, writes = append(reads, lease), append(writes, lease)
		if !completed.serialSegment.IsZero() {
			segment := SerialSegmentJournalResource(
				completed.serialSegment,
			)
			reads, writes = append(reads, segment),
				append(writes, segment)
		}
	}
	return reads, writes, true
}

func cloneIntegrationJournalEvent(
	event WorkspaceJournalEvent,
) WorkspaceJournalEvent {
	switch event := event.(type) {
	case MergeUnitIntegrationIntendedJournalEvent:
		return event
	case MergeUnitIntegratedJournalEvent:
		return event
	default:
		return nil
	}
}

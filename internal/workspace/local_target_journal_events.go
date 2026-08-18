package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

type FeatureRefCreationIntendedJournalEvent struct {
	workspaceID  ID
	generation   Digest
	binding      LocalTargetBinding
	intentDigest Digest
}

func NewFeatureRefCreationIntendedJournalEvent(
	workspaceID ID,
	generation Digest,
	binding LocalTargetBinding,
) (FeatureRefCreationIntendedJournalEvent, error) {
	event := FeatureRefCreationIntendedJournalEvent{
		workspaceID: workspaceID, generation: generation, binding: binding,
	}
	if workspaceID.IsZero() || generation.IsZero() || binding.IsZero() {
		return FeatureRefCreationIntendedJournalEvent{}, fmt.Errorf(
			"feature-ref creation intent requires workspace, generation, and local target binding",
		)
	}
	digest, err := digestFeatureRefCreationIntent(
		workspaceID, generation, binding,
	)
	if err != nil {
		return FeatureRefCreationIntendedJournalEvent{}, err
	}
	event.intentDigest = digest
	if err := event.validate(); err != nil {
		return FeatureRefCreationIntendedJournalEvent{}, err
	}
	return event, nil
}

func (FeatureRefCreationIntendedJournalEvent) isWorkspaceJournalEvent() {}
func (FeatureRefCreationIntendedJournalEvent) eventType() JournalEventType {
	return JournalEventFeatureRefCreationIntended
}
func (event FeatureRefCreationIntendedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event FeatureRefCreationIntendedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.binding.IsZero() || event.intentDigest.IsZero() {
		return fmt.Errorf(
			"feature-ref creation intent requires complete immutable bindings",
		)
	}
	expected, err := digestFeatureRefCreationIntent(
		event.workspaceID, event.generation, event.binding,
	)
	if err != nil {
		return err
	}
	if expected != event.intentDigest {
		return fmt.Errorf("feature-ref creation intent digest mismatch")
	}
	return nil
}
func (event FeatureRefCreationIntendedJournalEvent) WorkspaceID() ID {
	return event.workspaceID
}
func (event FeatureRefCreationIntendedJournalEvent) Generation() Digest {
	return event.generation
}
func (event FeatureRefCreationIntendedJournalEvent) Binding() LocalTargetBinding {
	return event.binding
}
func (event FeatureRefCreationIntendedJournalEvent) IntentDigest() Digest {
	return event.intentDigest
}

type FeatureRefCreatedJournalEvent struct {
	workspaceID  ID
	generation   Digest
	intentDigest Digest
	featureRef   string
	head         GitObjectID
}

func NewFeatureRefCreatedJournalEvent(
	workspaceID ID,
	generation, intentDigest Digest,
	featureRef string,
	head GitObjectID,
) (FeatureRefCreatedJournalEvent, error) {
	event := FeatureRefCreatedJournalEvent{
		workspaceID: workspaceID, generation: generation,
		intentDigest: intentDigest, featureRef: featureRef, head: head,
	}
	if err := event.validate(); err != nil {
		return FeatureRefCreatedJournalEvent{}, err
	}
	return event, nil
}

func (FeatureRefCreatedJournalEvent) isWorkspaceJournalEvent() {}
func (FeatureRefCreatedJournalEvent) eventType() JournalEventType {
	return JournalEventFeatureRefCreated
}
func (event FeatureRefCreatedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event FeatureRefCreatedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.intentDigest.IsZero() || event.head.IsZero() {
		return fmt.Errorf(
			"feature-ref creation completion requires workspace, generation, intent, and head",
		)
	}
	if _, err := normalizeFullyQualifiedBaseRef(event.featureRef); err != nil {
		return fmt.Errorf("feature ref: %w", err)
	}
	branch := event.featureRef[len("refs/heads/"):]
	if _, err := normalizeFeatureBranch(branch); err != nil {
		return fmt.Errorf("feature ref: %w", err)
	}
	return nil
}
func (event FeatureRefCreatedJournalEvent) WorkspaceID() ID {
	return event.workspaceID
}
func (event FeatureRefCreatedJournalEvent) Generation() Digest {
	return event.generation
}
func (event FeatureRefCreatedJournalEvent) IntentDigest() Digest {
	return event.intentDigest
}
func (event FeatureRefCreatedJournalEvent) FeatureRef() string {
	return event.featureRef
}
func (event FeatureRefCreatedJournalEvent) Head() GitObjectID {
	return event.head
}

// WorkspaceAbandonedJournalEvent records the final release of a workspace's
// local feature-ref ownership. A zero feature head means creation had not
// completed, so no ref was released.
type WorkspaceAbandonedJournalEvent struct {
	workspaceID ID
	generation  Digest
	featureRef  string
	featureHead GitObjectID
	reason      string
}

func NewWorkspaceAbandonedJournalEvent(
	workspaceID ID,
	generation Digest,
	featureRef string,
	featureHead GitObjectID,
	reason string,
) (WorkspaceAbandonedJournalEvent, error) {
	event := WorkspaceAbandonedJournalEvent{
		workspaceID: workspaceID,
		generation:  generation,
		featureRef:  strings.TrimSpace(featureRef),
		featureHead: featureHead,
		reason:      strings.TrimSpace(reason),
	}
	if err := event.validate(); err != nil {
		return WorkspaceAbandonedJournalEvent{}, err
	}
	return event, nil
}

func (WorkspaceAbandonedJournalEvent) isWorkspaceJournalEvent() {}
func (WorkspaceAbandonedJournalEvent) eventType() JournalEventType {
	return JournalEventWorkspaceAbandoned
}
func (event WorkspaceAbandonedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event WorkspaceAbandonedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() {
		return fmt.Errorf("workspace abandonment requires workspace and generation bindings")
	}
	normalized, err := normalizeFullyQualifiedBaseRef(event.featureRef)
	if err != nil || normalized != event.featureRef ||
		!strings.HasPrefix(event.featureRef, "refs/heads/feature/") {
		return fmt.Errorf("workspace abandonment requires the exact owned feature ref")
	}
	if err := validateBoundedText("workspace abandonment reason", event.reason, 16*1024); err != nil {
		return err
	}
	return nil
}
func (event WorkspaceAbandonedJournalEvent) WorkspaceID() ID {
	return event.workspaceID
}
func (event WorkspaceAbandonedJournalEvent) Generation() Digest {
	return event.generation
}
func (event WorkspaceAbandonedJournalEvent) FeatureRef() string {
	return event.featureRef
}
func (event WorkspaceAbandonedJournalEvent) FeatureHead() GitObjectID {
	return event.featureHead
}
func (event WorkspaceAbandonedJournalEvent) Reason() string { return event.reason }

func digestFeatureRefCreationIntent(
	workspaceID ID,
	generation Digest,
	binding LocalTargetBinding,
) (Digest, error) {
	type intentWire struct {
		SchemaVersion int                    `json:"schema_version"`
		WorkspaceID   string                 `json:"workspace_id"`
		Generation    string                 `json:"generation"`
		Binding       localTargetBindingWire `json:"binding"`
	}
	content, err := json.Marshal(intentWire{
		SchemaVersion: JournalSchemaVersion,
		WorkspaceID:   workspaceID.String(), Generation: generation.String(),
		Binding: localTargetBindingToWire(binding),
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func featureRefJournalResource(
	workspaceID ID,
	featureRef string,
) JournalResource {
	resource, _ := NewJournalResource(
		JournalResourceFeatureRef,
		workspaceID.String()+":"+featureRef,
	)
	return resource
}

func localTargetJournalEventResources(
	event WorkspaceJournalEvent,
) ([]JournalResource, []JournalResource, bool) {
	var workspaceID ID
	var featureRef string
	switch event := event.(type) {
	case FeatureRefCreationIntendedJournalEvent:
		workspaceID, featureRef = event.workspaceID, event.binding.featureRef
	case FeatureRefCreatedJournalEvent:
		workspaceID, featureRef = event.workspaceID, event.featureRef
	case WorkspaceAbandonedJournalEvent:
		workspaceID, featureRef = event.workspaceID, event.featureRef
	default:
		return nil, nil, false
	}
	resources := []JournalResource{
		WorkspaceJournalResource(workspaceID),
		featureRefJournalResource(workspaceID, featureRef),
	}
	return resources, append([]JournalResource(nil), resources...), true
}

func isLocalTargetJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case FeatureRefCreationIntendedJournalEvent, FeatureRefCreatedJournalEvent,
		WorkspaceAbandonedJournalEvent:
		return true
	default:
		return false
	}
}

func cloneLocalTargetJournalEvent(
	event WorkspaceJournalEvent,
) WorkspaceJournalEvent {
	switch event := event.(type) {
	case FeatureRefCreationIntendedJournalEvent:
		return event
	case FeatureRefCreatedJournalEvent:
		return event
	case WorkspaceAbandonedJournalEvent:
		return event
	default:
		return nil
	}
}

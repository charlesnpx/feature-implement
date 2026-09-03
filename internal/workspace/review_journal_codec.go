package workspace

import (
	"encoding/json"
	"fmt"
	"time"
)

type reviewHeadAdoptedPayloadWire struct {
	WorkspaceID    string `json:"workspace_id"`
	Generation     string `json:"generation"`
	AttemptID      string `json:"attempt_id"`
	PlanID         string `json:"plan_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	PriorHead      string `json:"prior_head"`
	Head           string `json:"head"`
	Tree           string `json:"tree"`
	SnapshotDigest string `json:"snapshot_digest"`
}

type reviewGateDispatchPayloadWire struct {
	WorkspaceID  string `json:"workspace_id"`
	Generation   string `json:"generation"`
	AttemptID    string `json:"attempt_id"`
	PlanID       string `json:"plan_id"`
	MergeUnitID  string `json:"merge_unit_id"`
	Adapter      string `json:"adapter"`
	Recipe       string `json:"recipe"`
	PolicyDigest string `json:"policy_digest"`
	Head         string `json:"head"`
	Tree         string `json:"tree"`
	Digest       string `json:"digest"`
}

type reviewGateRecordPayloadWire struct {
	DispatchDigest string            `json:"dispatch_digest"`
	Adapter        string            `json:"adapter"`
	Recipe         string            `json:"recipe"`
	Head           string            `json:"head"`
	Tree           string            `json:"tree"`
	Verdict        ReviewGateVerdict `json:"verdict"`
	EvidenceDigest string            `json:"evidence_digest"`
	PolicyDigest   string            `json:"policy_digest"`
	OccurredAt     string            `json:"occurred_at"`
	Digest         string            `json:"digest"`
}

type reviewGateDispatchedPayloadWire struct {
	Dispatch reviewGateDispatchPayloadWire `json:"dispatch"`
}

type reviewDocumentArtifactPayloadWire struct {
	RawDocumentDigest string `json:"raw_document_digest"`
	ReportDigest      string `json:"report_digest"`
	RequestDigest     string `json:"request_digest"`
	ReviewInputDigest string `json:"review_input_digest"`
	CharterHash       string `json:"charter_hash"`
	Path              string `json:"path"`
}

type reviewGateRecordedPayloadWire struct {
	Dispatch reviewGateDispatchPayloadWire      `json:"dispatch"`
	Record   reviewGateRecordPayloadWire        `json:"record"`
	Document *reviewDocumentArtifactPayloadWire `json:"document,omitempty"`
}

func marshalReviewJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		value = reviewHeadAdoptedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), PlanID: event.mergeUnit.planID.String(),
			MergeUnitID: event.mergeUnit.mergeUnitID.String(), PriorHead: event.priorHead.String(),
			Head: event.head.String(), Tree: event.tree.String(), SnapshotDigest: event.snapshotDigest.String(),
		}
	case ReviewGateDispatchedJournalEvent:
		value = reviewGateDispatchedPayloadWire{Dispatch: reviewGateDispatchToWire(event.dispatch)}
	case ReviewGateRecordedJournalEvent:
		gate := reviewGateRecordedPayloadWire{
			Dispatch: reviewGateDispatchToWire(event.dispatch), Record: reviewGateRecordToWire(event.record),
		}
		if event.document != nil {
			document := reviewDocumentArtifactToWire(*event.document)
			gate.Document = &document
		}
		value = gate
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeReviewJournalEvent(
	eventType JournalEventType, payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventReviewHeadAdopted:
		var wire reviewHeadAdoptedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode head adoption: %w", err)
		}
		workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		planID, err := NewID(wire.PlanID)
		if err != nil {
			return nil, true, err
		}
		mergeUnitID, err := NewID(wire.MergeUnitID)
		if err != nil {
			return nil, true, err
		}
		mergeUnit, err := NewMergeUnitReference(planID, mergeUnitID)
		if err != nil {
			return nil, true, err
		}
		priorHead, err := ParseGitObjectID(wire.PriorHead)
		if err != nil {
			return nil, true, err
		}
		head, err := ParseGitObjectID(wire.Head)
		if err != nil {
			return nil, true, err
		}
		tree, err := ParseGitObjectID(wire.Tree)
		if err != nil {
			return nil, true, err
		}
		snapshotDigest, err := ParseDigest(wire.SnapshotDigest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewHeadAdoptedJournalEvent(
			workspaceID, generation, attemptID, mergeUnit, priorHead, head, tree, snapshotDigest,
		)
		return event, true, err
	case JournalEventReviewGateDispatched:
		var wire reviewGateDispatchedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review gate dispatch: %w", err)
		}
		dispatch, err := reviewGateDispatchFromWire(wire.Dispatch)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewGateDispatchedJournalEvent(dispatch)
		return event, true, err
	case JournalEventReviewGateRecorded:
		var wire reviewGateRecordedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review gate record: %w", err)
		}
		dispatch, err := reviewGateDispatchFromWire(wire.Dispatch)
		if err != nil {
			return nil, true, err
		}
		record, err := reviewGateRecordFromWire(wire.Record, dispatch)
		if err != nil {
			return nil, true, err
		}
		if wire.Document == nil {
			event, eventErr := NewReviewGateRecordedJournalEvent(dispatch, record)
			return event, true, eventErr
		}
		document, documentErr := reviewDocumentArtifactFromWire(*wire.Document)
		if documentErr != nil {
			return nil, true, documentErr
		}
		event, eventErr := NewReviewGateRecordedDocumentJournalEvent(dispatch, record, document)
		return event, true, eventErr
	default:
		return nil, false, nil
	}
}

func reviewGateDispatchToWire(dispatch ReviewGateDispatch) reviewGateDispatchPayloadWire {
	return reviewGateDispatchPayloadWire{
		WorkspaceID: dispatch.workspaceID.String(), Generation: dispatch.generation.String(),
		AttemptID: dispatch.attemptID.String(), PlanID: dispatch.mergeUnit.planID.String(),
		MergeUnitID: dispatch.mergeUnit.mergeUnitID.String(), Adapter: dispatch.adapter.String(),
		Recipe: dispatch.recipe.String(), PolicyDigest: dispatch.policyDigest.String(),
		Head: dispatch.head.String(), Tree: dispatch.tree.String(), Digest: dispatch.digest.String(),
	}
}

func reviewGateDispatchFromWire(wire reviewGateDispatchPayloadWire) (ReviewGateDispatch, error) {
	workspaceID, generation, attemptID, err := parseReviewEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	planID, err := NewID(wire.PlanID)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	mergeUnitID, err := NewID(wire.MergeUnitID)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	mergeUnit, err := NewMergeUnitReference(planID, mergeUnitID)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	adapter, err := NewID(wire.Adapter)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	recipe, err := NewID(wire.Recipe)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	policyDigest, err := ParseDigest(wire.PolicyDigest)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	head, err := ParseGitObjectID(wire.Head)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	tree, err := ParseGitObjectID(wire.Tree)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	dispatch, err := NewReviewGateDispatch(ReviewGateDispatchOptions{
		WorkspaceID: workspaceID, Generation: generation, AttemptID: attemptID, MergeUnit: mergeUnit,
		Adapter: adapter, Recipe: recipe, PolicyDigest: policyDigest, Head: head, Tree: tree,
	})
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != dispatch.digest {
		return ReviewGateDispatch{}, fmt.Errorf("review gate dispatch digest mismatch")
	}
	return dispatch, nil
}

func reviewGateRecordToWire(record ReviewGateRecord) reviewGateRecordPayloadWire {
	return reviewGateRecordPayloadWire{
		DispatchDigest: record.dispatchDigest.String(), Adapter: record.adapter.String(), Recipe: record.recipe.String(),
		Head: record.head.String(), Tree: record.tree.String(), Verdict: record.verdict,
		EvidenceDigest: record.evidenceDigest.String(), PolicyDigest: record.policyDigest.String(),
		OccurredAt: record.occurredAt.Format(time.RFC3339Nano), Digest: record.digest.String(),
	}
}

func reviewGateRecordFromWire(wire reviewGateRecordPayloadWire, dispatch ReviewGateDispatch) (ReviewGateRecord, error) {
	dispatchDigest, err := ParseDigest(wire.DispatchDigest)
	if err != nil || dispatchDigest != dispatch.digest {
		return ReviewGateRecord{}, fmt.Errorf("review gate record dispatch digest mismatch")
	}
	adapter, err := NewID(wire.Adapter)
	if err != nil || adapter != dispatch.adapter {
		return ReviewGateRecord{}, fmt.Errorf("review gate record adapter mismatch")
	}
	recipe, err := NewID(wire.Recipe)
	if err != nil || recipe != dispatch.recipe {
		return ReviewGateRecord{}, fmt.Errorf("review gate record recipe mismatch")
	}
	head, err := ParseGitObjectID(wire.Head)
	if err != nil || head != dispatch.head {
		return ReviewGateRecord{}, fmt.Errorf("review gate record head mismatch")
	}
	tree, err := ParseGitObjectID(wire.Tree)
	if err != nil || tree != dispatch.tree {
		return ReviewGateRecord{}, fmt.Errorf("review gate record tree mismatch")
	}
	policyDigest, err := ParseDigest(wire.PolicyDigest)
	if err != nil || policyDigest != dispatch.policyDigest {
		return ReviewGateRecord{}, fmt.Errorf("review gate record policy digest mismatch")
	}
	evidenceDigest, err := ParseDigest(wire.EvidenceDigest)
	if err != nil {
		return ReviewGateRecord{}, err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	if err != nil || wire.OccurredAt != occurredAt.UTC().Format(time.RFC3339Nano) {
		return ReviewGateRecord{}, fmt.Errorf("review gate record occurred_at must be canonical UTC RFC3339Nano")
	}
	record, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: wire.Verdict, EvidenceDigest: evidenceDigest, OccurredAt: occurredAt,
	})
	if err != nil {
		return ReviewGateRecord{}, err
	}
	digest, err := ParseDigest(wire.Digest)
	if err != nil || digest != record.digest {
		return ReviewGateRecord{}, fmt.Errorf("review gate record digest mismatch")
	}
	return record, nil
}

func reviewDocumentArtifactToWire(artifact ReviewDocumentArtifact) reviewDocumentArtifactPayloadWire {
	return reviewDocumentArtifactPayloadWire{
		RawDocumentDigest: artifact.rawDocumentDigest.String(), ReportDigest: artifact.reportDigest.String(),
		RequestDigest: artifact.requestDigest.String(), ReviewInputDigest: artifact.reviewInputDigest.String(),
		CharterHash: artifact.charterHash.String(), Path: artifact.path,
	}
}

func reviewDocumentArtifactFromWire(wire reviewDocumentArtifactPayloadWire) (ReviewDocumentArtifact, error) {
	rawDocumentDigest, err := ParseDigest(wire.RawDocumentDigest)
	if err != nil {
		return ReviewDocumentArtifact{}, err
	}
	reportDigest, err := ParseDigest(wire.ReportDigest)
	if err != nil {
		return ReviewDocumentArtifact{}, err
	}
	requestDigest, err := ParseDigest(wire.RequestDigest)
	if err != nil {
		return ReviewDocumentArtifact{}, err
	}
	reviewInputDigest, err := ParseDigest(wire.ReviewInputDigest)
	if err != nil {
		return ReviewDocumentArtifact{}, err
	}
	charterHash, err := ParseDigest(wire.CharterHash)
	if err != nil {
		return ReviewDocumentArtifact{}, err
	}
	artifact := ReviewDocumentArtifact{
		rawDocumentDigest: rawDocumentDigest, reportDigest: reportDigest, requestDigest: requestDigest,
		reviewInputDigest: reviewInputDigest, charterHash: charterHash, path: wire.Path,
	}
	if err := artifact.validate(); err != nil {
		return ReviewDocumentArtifact{}, err
	}
	return artifact, nil
}

func parseReviewEnvelope(workspaceRaw, generationRaw, attemptRaw string) (ID, Digest, ID, error) {
	workspaceID, err := NewID(workspaceRaw)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	generation, err := ParseDigest(generationRaw)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	attemptID, err := NewID(attemptRaw)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	return workspaceID, generation, attemptID, nil
}

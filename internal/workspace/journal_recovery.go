package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const WorkspaceJournalRecoveryFileName = "journal-recovery.v3.pending.json"

type journalRecoveryIntentWire struct {
	SchemaVersion int    `json:"schema_version"`
	WorkspaceID   string `json:"workspace_id"`
	Generation    string `json:"generation"`
	DiscardOffset int64  `json:"discard_offset"`
	DiscardSize   int64  `json:"discard_size"`
	DiscardDigest string `json:"discard_digest"`
	ResultingHead string `json:"resulting_head"`
	TailTruncated bool   `json:"tail_truncated"`
}

type journalRecoveryIntent struct {
	workspaceID   ID
	generation    Digest
	discardOffset int64
	discardSize   int64
	discardDigest Digest
	resultingHead Digest
	tailTruncated bool
}

type JournalRecoveryReport struct {
	recovered     bool
	discardOffset int64
	discardSize   int64
	discardDigest Digest
	truncatedHead Digest
	journalHead   Digest
}

func (report JournalRecoveryReport) Recovered() bool       { return report.recovered }
func (report JournalRecoveryReport) DiscardOffset() int64  { return report.discardOffset }
func (report JournalRecoveryReport) DiscardSize() int64    { return report.discardSize }
func (report JournalRecoveryReport) DiscardDigest() Digest { return report.discardDigest }
func (report JournalRecoveryReport) TruncatedHead() Digest { return report.truncatedHead }
func (report JournalRecoveryReport) JournalHead() Digest   { return report.journalHead }

func workspaceJournalRecoveryPath(workspaceDir string) string {
	return filepath.Join(WorkspaceStateDirectory(workspaceDir), WorkspaceJournalRecoveryFileName)
}

func journalRecoveryPending(journal *WorkspaceJournal) (bool, error) {
	if journal == nil || journal.runtime == nil {
		return false, fmt.Errorf("workspace journal is closed")
	}
	if err := journal.runtime.state.RecoverReplaceable(WorkspaceJournalRecoveryFileName); err != nil {
		return false, err
	}
	_, exists, err := journal.runtime.state.adapter.inspectExact(WorkspaceJournalRecoveryFileName)
	if err == nil && exists {
		return true, nil
	}
	if err == nil {
		return false, nil
	}
	return false, fmt.Errorf("inspect pending journal recovery: %w", err)
}

func (journal *WorkspaceJournal) RecoverIncompleteTail(workspaceID ID, occurredAt time.Time) (JournalRecoveryReport, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireWriter(); err != nil {
		return JournalRecoveryReport{}, err
	}
	if workspaceID.IsZero() || occurredAt.IsZero() {
		return JournalRecoveryReport{}, fmt.Errorf("journal recovery requires workspace identity and occurrence time")
	}
	intent, exists, err := loadJournalRecoveryIntent(journal)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	if exists {
		if intent.workspaceID != workspaceID {
			return JournalRecoveryReport{}, fmt.Errorf("pending journal recovery belongs to workspace %s", intent.workspaceID)
		}
		return journal.completeJournalRecovery(intent, occurredAt.UTC())
	}
	snapshot, tail, err := journal.readSnapshotAllowTail()
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	if tail == nil {
		return JournalRecoveryReport{journalHead: snapshot.head, truncatedHead: snapshot.head}, nil
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	generation := runtime.activeGeneration
	if generation.IsZero() {
		if !runtime.workspaceID.IsZero() && runtime.workspaceID != workspaceID {
			return JournalRecoveryReport{}, fmt.Errorf("journal belongs to workspace %s", runtime.workspaceID)
		}
		generation, err = initialGenerationForRecovery(journal.workspaceDir, workspaceID)
		if err != nil {
			return JournalRecoveryReport{}, err
		}
	} else if runtime.workspaceID != workspaceID {
		return JournalRecoveryReport{}, fmt.Errorf("journal belongs to workspace %s", runtime.workspaceID)
	}
	intent = journalRecoveryIntent{
		workspaceID: workspaceID, generation: generation,
		discardOffset: tail.offset, discardSize: tail.size, discardDigest: tail.digest,
		resultingHead: tail.resultingHead,
	}
	if err := writeJournalRecoveryIntent(journal, intent); err != nil {
		return JournalRecoveryReport{}, err
	}
	return journal.completeJournalRecovery(intent, occurredAt.UTC())
}

func (journal *WorkspaceJournal) completeJournalRecovery(intent journalRecoveryIntent, occurredAt time.Time) (JournalRecoveryReport, error) {
	content, err := journal.runtime.state.ReadBounded(WorkspaceJournalFileName, MaxJournalBytes)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	snapshot, tail, err := parseJournalBytes(content)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	if tail == nil && journalRecoveryAlreadyRecorded(snapshot, intent) {
		file, _, err := journal.runtime.state.openOwnedRegularFile(
			WorkspaceJournalFileName, os.O_RDWR, 0, false,
		)
		if err != nil {
			return JournalRecoveryReport{}, err
		}
		syncErr := journal.runtime.state.adapter.synchronizeOpenedFile(WorkspaceJournalFileName, file)
		closeErr := file.Close()
		if syncErr != nil {
			return JournalRecoveryReport{}, syncErr
		}
		if closeErr != nil {
			return JournalRecoveryReport{}, closeErr
		}
		if err := removeJournalRecoveryIntent(journal); err != nil {
			return JournalRecoveryReport{}, err
		}
		return recoveryReportFromIntent(intent, snapshot.head), nil
	}
	if snapshot.head != intent.resultingHead || snapshot.byteLength != intent.discardOffset {
		return JournalRecoveryReport{}, fmt.Errorf("journal changed outside the pending recovery boundary")
	}
	if tail != nil {
		if tail.offset != intent.discardOffset {
			return JournalRecoveryReport{}, fmt.Errorf("incomplete journal tail moved after recovery was prepared")
		}
		if !intent.tailTruncated && (tail.size != intent.discardSize || tail.digest != intent.discardDigest) {
			return JournalRecoveryReport{}, fmt.Errorf("incomplete journal tail changed after recovery was prepared")
		}
		if err := truncateJournalSynchronously(journal, intent.discardOffset); err != nil {
			return JournalRecoveryReport{}, err
		}
		intent.tailTruncated = true
		if err := writeJournalRecoveryIntent(journal, intent); err != nil {
			return JournalRecoveryReport{}, err
		}
		snapshot, tail, err = journal.readSnapshotAllowTail()
		if err != nil {
			return JournalRecoveryReport{}, err
		}
		if tail != nil {
			return JournalRecoveryReport{}, fmt.Errorf("journal remained incomplete after truncation")
		}
	} else if !intent.tailTruncated {
		intent.tailTruncated = true
		if err := writeJournalRecoveryIntent(journal, intent); err != nil {
			return JournalRecoveryReport{}, err
		}
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	if runtime.activeGeneration.IsZero() {
		if !runtime.workspaceID.IsZero() && runtime.workspaceID != intent.workspaceID {
			return JournalRecoveryReport{}, fmt.Errorf("pending bootstrap recovery workspace no longer matches journal history")
		}
		generation, err := initialGenerationForRecovery(journal.workspaceDir, intent.workspaceID)
		if err != nil {
			return JournalRecoveryReport{}, err
		}
		if generation != intent.generation {
			return JournalRecoveryReport{}, fmt.Errorf("pending bootstrap recovery generation no longer matches the stored definition")
		}
	} else if runtime.workspaceID != intent.workspaceID || runtime.activeGeneration != intent.generation {
		return JournalRecoveryReport{}, fmt.Errorf("pending recovery generation no longer matches journal history")
	}
	event, err := NewJournalTailRecoveredEvent(
		intent.workspaceID, intent.generation, intent.discardOffset, intent.discardSize,
		intent.discardDigest, intent.resultingHead,
	)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	request, err := NewJournalAppend(event, occurredAt)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	record, err := journal.appendToSnapshot(snapshot, request)
	if err != nil {
		return JournalRecoveryReport{}, err
	}
	if err := removeJournalRecoveryIntent(journal); err != nil {
		return JournalRecoveryReport{}, err
	}
	return recoveryReportFromIntent(intent, record.eventHash), nil
}

func initialGenerationForRecovery(workspaceDir string, workspaceID ID) (Digest, error) {
	store, err := OpenGenerationStore(workspaceDir)
	if err != nil {
		return Digest{}, err
	}
	defer store.Close()
	generations, err := store.List()
	if err != nil {
		return Digest{}, err
	}
	if len(generations) != 1 {
		return Digest{}, fmt.Errorf("bootstrap journal recovery requires exactly one stored generation, found %d", len(generations))
	}
	stored, err := store.Load(generations[0])
	if err != nil {
		return Digest{}, err
	}
	if stored.workspaceID != workspaceID {
		return Digest{}, fmt.Errorf("stored bootstrap generation belongs to workspace %s, not %s", stored.workspaceID, workspaceID)
	}
	return stored.generation, nil
}

func journalRecoveryAlreadyRecorded(snapshot JournalSnapshot, intent journalRecoveryIntent) bool {
	if len(snapshot.records) == 0 {
		return false
	}
	event, ok := snapshot.records[len(snapshot.records)-1].event.(JournalTailRecoveredEvent)
	return ok && event.workspaceID == intent.workspaceID && event.generation == intent.generation &&
		event.discardOffset == intent.discardOffset && event.discardSize == intent.discardSize &&
		event.discardDigest == intent.discardDigest && event.resultingHead == intent.resultingHead
}

func recoveryReportFromIntent(intent journalRecoveryIntent, journalHead Digest) JournalRecoveryReport {
	return JournalRecoveryReport{
		recovered: true, discardOffset: intent.discardOffset, discardSize: intent.discardSize,
		discardDigest: intent.discardDigest, truncatedHead: intent.resultingHead, journalHead: journalHead,
	}
}

func loadJournalRecoveryIntent(journal *WorkspaceJournal) (journalRecoveryIntent, bool, error) {
	if journal == nil || journal.runtime == nil {
		return journalRecoveryIntent{}, false, fmt.Errorf("workspace journal is closed")
	}
	content, err := journal.runtime.state.ReadReplaceable(WorkspaceJournalRecoveryFileName, 64*1024)
	if err != nil {
		if os.IsNotExist(err) {
			return journalRecoveryIntent{}, false, nil
		}
		return journalRecoveryIntent{}, false, err
	}
	var wire journalRecoveryIntentWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return journalRecoveryIntent{}, false, fmt.Errorf("decode pending journal recovery: %w", err)
	}
	if wire.SchemaVersion != JournalSchemaVersion {
		return journalRecoveryIntent{}, false, fmt.Errorf("pending journal recovery schema_version must be %d", JournalSchemaVersion)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return journalRecoveryIntent{}, false, err
	}
	if !bytes.Equal(canonical, content) {
		return journalRecoveryIntent{}, false, fmt.Errorf("pending journal recovery is not canonical JSON")
	}
	workspaceID, err := NewID(wire.WorkspaceID)
	if err != nil {
		return journalRecoveryIntent{}, false, err
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return journalRecoveryIntent{}, false, err
	}
	discardDigest, err := ParseDigest(wire.DiscardDigest)
	if err != nil {
		return journalRecoveryIntent{}, false, err
	}
	resultingHead, err := ParseDigest(wire.ResultingHead)
	if err != nil {
		return journalRecoveryIntent{}, false, err
	}
	intent := journalRecoveryIntent{
		workspaceID: workspaceID, generation: generation, discardOffset: wire.DiscardOffset,
		discardSize: wire.DiscardSize, discardDigest: discardDigest,
		resultingHead: resultingHead, tailTruncated: wire.TailTruncated,
	}
	if intent.discardOffset < 0 || intent.discardSize <= 0 {
		return journalRecoveryIntent{}, false, fmt.Errorf("pending journal recovery has invalid discarded range")
	}
	if err := journal.runtime.state.Sync(); err != nil {
		return journalRecoveryIntent{}, false, fmt.Errorf("synchronize pending journal recovery: %w", err)
	}
	return intent, true, nil
}

func writeJournalRecoveryIntent(journal *WorkspaceJournal, intent journalRecoveryIntent) error {
	wire := journalRecoveryIntentWire{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: intent.workspaceID.String(),
		Generation: intent.generation.String(), DiscardOffset: intent.discardOffset,
		DiscardSize: intent.discardSize, DiscardDigest: intent.discardDigest.String(),
		ResultingHead: intent.resultingHead.String(), TailTruncated: intent.tailTruncated,
	}
	content, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	return journal.runtime.state.PublishReplaceable(
		WorkspaceJournalRecoveryFileName,
		content,
		0o600,
		64*1024,
		PublicationOptions{},
	)
}

func truncateJournalSynchronously(journal *WorkspaceJournal, size int64) error {
	file, _, err := journal.runtime.state.openOwnedRegularFile(
		WorkspaceJournalFileName, os.O_RDWR, 0, false,
	)
	if err != nil {
		return err
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := journal.runtime.state.verifyOwnedRegularFile(WorkspaceJournalFileName, file); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return journal.runtime.state.Sync()
}

func removeJournalRecoveryIntent(journal *WorkspaceJournal) error {
	content, err := journal.runtime.state.ReadReplaceable(WorkspaceJournalRecoveryFileName, 64*1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	removed, err := journal.runtime.state.adapter.removeFileContentExact(
		WorkspaceJournalRecoveryFileName,
		content,
		64*1024,
		journal.runtime.Verify,
	)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("pending journal recovery disappeared")
	}
	return nil
}

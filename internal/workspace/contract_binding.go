package workspace

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EventContractBound = "contract.bound"

	contractBindingStatusMissing = "missing"
	contractBindingStatusCurrent = "current"
	contractBindingStatusStale   = "stale"

	eventPayloadPublicationEventIDKey   = "publication_event_id"
	eventPayloadPublicationEventHashKey = "publication_event_hash"
)

type ContractBindOptions struct {
	WorkspaceDir   string
	ContractID     string
	ArtifactID     string
	MergeUnitID    string
	AttemptID      string
	AgentID        string
	LeaseID        string
	CommandResults []ContractCommandResult
	Now            func() time.Time
}

type ContractCheckOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	Now          func() time.Time
}

type ContractBindResult struct {
	Status                 string                  `json:"status"`
	WorkspaceDir           string                  `json:"workspace_dir"`
	WorkspaceID            string                  `json:"workspace_id"`
	BaseRef                string                  `json:"base_ref"`
	ContractID             string                  `json:"contract_id"`
	Version                string                  `json:"version"`
	ProducerMergeUnitID    string                  `json:"producer_merge_unit_id"`
	ProducerCommit         string                  `json:"producer_commit"`
	MergeUnitID            string                  `json:"merge_unit_id"`
	AttemptID              string                  `json:"attempt_id"`
	AgentID                string                  `json:"agent_id"`
	LeaseID                string                  `json:"lease_id"`
	ArtifactID             string                  `json:"artifact_id"`
	ArtifactPath           string                  `json:"artifact_path"`
	ArtifactHash           string                  `json:"artifact_hash"`
	CommandResults         []ContractCommandResult `json:"command_results"`
	PublicationEventID     string                  `json:"publication_event_id"`
	PublicationEventHash   string                  `json:"publication_event_hash"`
	ContractBindingEventID string                  `json:"event_id"`
	EventHash              string                  `json:"event_hash"`
}

type ContractCheckResult struct {
	Status       string                  `json:"status"`
	WorkspaceDir string                  `json:"workspace_dir"`
	WorkspaceID  string                  `json:"workspace_id"`
	BaseRef      string                  `json:"base_ref"`
	MergeUnitID  string                  `json:"merge_unit_id"`
	AttemptID    string                  `json:"attempt_id"`
	Bindings     []ContractBindingStatus `json:"bindings"`
}

type ContractBindingStatus struct {
	ContractID             string                  `json:"contract_id"`
	ArtifactID             string                  `json:"artifact_id"`
	Status                 string                  `json:"status"`
	Version                string                  `json:"version,omitempty"`
	ProducerMergeUnitID    string                  `json:"producer_merge_unit_id,omitempty"`
	ProducerCommit         string                  `json:"producer_commit,omitempty"`
	ArtifactPath           string                  `json:"artifact_path,omitempty"`
	ArtifactHash           string                  `json:"artifact_hash,omitempty"`
	BoundVersion           string                  `json:"bound_version,omitempty"`
	BoundArtifactPath      string                  `json:"bound_artifact_path,omitempty"`
	BoundArtifactHash      string                  `json:"bound_artifact_hash,omitempty"`
	AttemptID              string                  `json:"attempt_id,omitempty"`
	CommandResults         []ContractCommandResult `json:"command_results,omitempty"`
	PublicationEventID     string                  `json:"publication_event_id,omitempty"`
	PublicationEventHash   string                  `json:"publication_event_hash,omitempty"`
	ContractBindingEventID string                  `json:"event_id,omitempty"`
	EventHash              string                  `json:"event_hash,omitempty"`
}

type contractBindingSnapshot struct {
	EventID              string
	EventHash            string
	ContractID           string
	Version              string
	ProducerMergeUnitID  string
	ProducerCommit       string
	MergeUnitID          string
	AttemptID            string
	AgentID              string
	LeaseID              string
	ArtifactID           string
	ArtifactPath         string
	ArtifactHash         string
	CommandResults       []ContractCommandResult
	PublicationEventID   string
	PublicationEventHash string
}

func ContractBindingResource(mergeUnitID string, contractID string, artifactID string) string {
	return resourceKey("contract_binding", mergeUnitID+":"+contractID+":"+artifactID)
}

func BindContract(opts ContractBindOptions) (ContractBindResult, error) {
	opts, boundAt, err := normalizeContractBindOptions(opts)
	if err != nil {
		return ContractBindResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return ContractBindResult{}, err
	}
	gate, err := findContractGate(lock, opts.ContractID)
	if err != nil {
		return ContractBindResult{}, err
	}
	artifact, err := selectContractArtifact(gate, opts.ArtifactID)
	if err != nil {
		return ContractBindResult{}, err
	}
	if !containsString(gate.Consumers, opts.MergeUnitID) {
		return ContractBindResult{}, fmt.Errorf("merge unit %s is not a consumer for contract %s", opts.MergeUnitID, gate.ID)
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, boundAt)
	if err != nil {
		return ContractBindResult{}, err
	}
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return ContractBindResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return ContractBindResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ContractBindResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, boundAt)
	if err != nil {
		return ContractBindResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return ContractBindResult{}, err
	}
	publication, found, err := latestContractPublication(state.Events, gate.ID, artifact.ID)
	if err != nil {
		return ContractBindResult{}, err
	}
	if !found {
		return ContractBindResult{}, fmt.Errorf("contract %s artifact %s is unpublished", gate.ID, artifact.ID)
	}
	if len(opts.CommandResults) == 0 {
		return ContractBindResult{}, fmt.Errorf("workspace contract bind requires --command-result for each validation command")
	}
	commandResults, err := normalizeContractCommandResults(gate, opts.CommandResults)
	if err != nil {
		return ContractBindResult{}, err
	}
	contractResource := ContractResource(gate.ID)
	bindingResource := ContractBindingResource(opts.MergeUnitID, gate.ID, artifact.ID)
	leaseResource := LeaseResource(opts.MergeUnitID)
	mergeUnitResource := MergeUnitResource(opts.MergeUnitID)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventContractBound,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:          opts.MergeUnitID,
			eventPayloadAttemptIDKey:            opts.AttemptID,
			eventPayloadAgentIDKey:              opts.AgentID,
			eventPayloadLeaseIDKey:              opts.LeaseID,
			eventPayloadContractIDKey:           gate.ID,
			eventPayloadVersionKey:              publication.Version,
			eventPayloadProducerMergeUnitIDKey:  publication.ProducerMergeUnitID,
			eventPayloadProducerCommitKey:       publication.ProducerCommit,
			eventPayloadArtifactIDKey:           publication.ArtifactID,
			eventPayloadArtifactPathKey:         publication.ArtifactPath,
			eventPayloadArtifactHashKey:         publication.ArtifactHash,
			eventPayloadCommandResultsKey:       commandResults,
			eventPayloadPublicationEventIDKey:   publication.EventID,
			eventPayloadPublicationEventHashKey: publication.EventHash,
		},
		ReadSet: map[string]int{
			contractResource:  state.Revisions[contractResource],
			bindingResource:   state.Revisions[bindingResource],
			leaseResource:     state.Revisions[leaseResource],
			mergeUnitResource: state.Revisions[mergeUnitResource],
		},
		WriteSet: []string{bindingResource},
		Now:      func() time.Time { return boundAt },
	})
	if err != nil {
		return ContractBindResult{}, err
	}
	return ContractBindResult{
		Status:                 "bound",
		WorkspaceDir:           opts.WorkspaceDir,
		WorkspaceID:            lock.WorkspaceID,
		BaseRef:                lock.BaseRef,
		ContractID:             gate.ID,
		Version:                publication.Version,
		ProducerMergeUnitID:    publication.ProducerMergeUnitID,
		ProducerCommit:         publication.ProducerCommit,
		MergeUnitID:            opts.MergeUnitID,
		AttemptID:              opts.AttemptID,
		AgentID:                opts.AgentID,
		LeaseID:                opts.LeaseID,
		ArtifactID:             publication.ArtifactID,
		ArtifactPath:           publication.ArtifactPath,
		ArtifactHash:           publication.ArtifactHash,
		CommandResults:         commandResults,
		PublicationEventID:     publication.EventID,
		PublicationEventHash:   publication.EventHash,
		ContractBindingEventID: event.ID,
		EventHash:              event.EventHash,
	}, nil
}

func CheckContracts(opts ContractCheckOptions) (ContractCheckResult, error) {
	opts, checkedAt, err := normalizeContractCheckOptions(opts)
	if err != nil {
		return ContractCheckResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return ContractCheckResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, checkedAt)
	if err != nil {
		return ContractCheckResult{}, err
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ContractCheckResult{}, err
	}
	if _, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, checkedAt); err != nil {
		return ContractCheckResult{}, err
	}
	bindings, err := contractBindingStatuses(lock, state.Events, opts.MergeUnitID, opts.AttemptID)
	if err != nil {
		return ContractCheckResult{}, err
	}
	return ContractCheckResult{
		Status:       aggregateContractBindingStatus(bindings),
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  lock.WorkspaceID,
		BaseRef:      lock.BaseRef,
		MergeUnitID:  opts.MergeUnitID,
		AttemptID:    opts.AttemptID,
		Bindings:     bindings,
	}, nil
}

func normalizeContractBindOptions(opts ContractBindOptions) (ContractBindOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ContractBindOptions{}, time.Time{}, fmt.Errorf("workspace contract bind requires <workspace-dir>")
	}
	opts.ContractID = strings.TrimSpace(opts.ContractID)
	if opts.ContractID == "" {
		return ContractBindOptions{}, time.Time{}, fmt.Errorf("workspace contract bind requires --contract")
	}
	opts.ArtifactID = strings.TrimSpace(opts.ArtifactID)
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return ContractBindOptions{}, time.Time{}, fmt.Errorf("workspace contract bind requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return ContractBindOptions{}, time.Time{}, fmt.Errorf("workspace contract bind requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return ContractBindOptions{}, time.Time{}, fmt.Errorf("workspace contract bind requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return ContractBindOptions{}, time.Time{}, fmt.Errorf("workspace contract bind requires --lease")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func normalizeContractCheckOptions(opts ContractCheckOptions) (ContractCheckOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ContractCheckOptions{}, time.Time{}, fmt.Errorf("workspace contract check-contracts requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return ContractCheckOptions{}, time.Time{}, fmt.Errorf("workspace contract check-contracts requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return ContractCheckOptions{}, time.Time{}, fmt.Errorf("workspace contract check-contracts requires --attempt")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func contractBindingStatuses(lock WorkspaceLock, events []JournalEvent, mergeUnitID string, attemptID string) ([]ContractBindingStatus, error) {
	statuses := []ContractBindingStatus{}
	for _, gate := range lock.ContractGates {
		if !containsString(gate.Consumers, mergeUnitID) {
			continue
		}
		for _, artifact := range gate.Artifacts {
			publication, publicationFound, err := latestContractPublication(events, gate.ID, artifact.ID)
			if err != nil {
				return nil, err
			}
			status := ContractBindingStatus{
				ContractID: gate.ID,
				ArtifactID: artifact.ID,
				Status:     contractBindingStatusMissing,
			}
			if publicationFound {
				status.Version = publication.Version
				status.ProducerMergeUnitID = publication.ProducerMergeUnitID
				status.ProducerCommit = publication.ProducerCommit
				status.ArtifactPath = publication.ArtifactPath
				status.ArtifactHash = publication.ArtifactHash
				status.PublicationEventID = publication.EventID
				status.PublicationEventHash = publication.EventHash
			}
			if attemptID == "" {
				statuses = append(statuses, status)
				continue
			}
			binding, bindingFound, err := latestContractBinding(events, mergeUnitID, attemptID, gate.ID, artifact.ID)
			if err != nil {
				return nil, err
			}
			if !bindingFound {
				statuses = append(statuses, status)
				continue
			}
			status.BoundVersion = binding.Version
			status.BoundArtifactPath = binding.ArtifactPath
			status.BoundArtifactHash = binding.ArtifactHash
			status.AttemptID = binding.AttemptID
			status.CommandResults = binding.CommandResults
			status.ContractBindingEventID = binding.EventID
			status.EventHash = binding.EventHash
			status.Status = contractBindingStatusStale
			if publicationFound &&
				binding.PublicationEventID == publication.EventID &&
				binding.PublicationEventHash == publication.EventHash &&
				binding.Version == publication.Version &&
				binding.ArtifactPath == publication.ArtifactPath &&
				binding.ArtifactHash == publication.ArtifactHash {
				status.Status = contractBindingStatusCurrent
			}
			statuses = append(statuses, status)
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].ContractID != statuses[j].ContractID {
			return statuses[i].ContractID < statuses[j].ContractID
		}
		return statuses[i].ArtifactID < statuses[j].ArtifactID
	})
	return statuses, nil
}

func aggregateContractBindingStatus(bindings []ContractBindingStatus) string {
	if len(bindings) == 0 {
		return "none"
	}
	status := contractBindingStatusCurrent
	for _, binding := range bindings {
		if binding.Status == contractBindingStatusStale {
			return contractBindingStatusStale
		}
		if binding.Status == contractBindingStatusMissing {
			status = contractBindingStatusMissing
		}
	}
	return status
}

func latestContractBinding(events []JournalEvent, mergeUnitID string, attemptID string, contractID string, artifactID string) (contractBindingSnapshot, bool, error) {
	var latest contractBindingSnapshot
	found := false
	for _, event := range events {
		if event.Type != EventContractBound {
			continue
		}
		binding, err := contractBindingFromEvent(event)
		if err != nil {
			return contractBindingSnapshot{}, false, err
		}
		if binding.MergeUnitID != mergeUnitID ||
			binding.AttemptID != attemptID ||
			binding.ContractID != contractID ||
			binding.ArtifactID != artifactID {
			continue
		}
		latest = binding
		found = true
	}
	return latest, found, nil
}

func contractBindingFromEvent(event JournalEvent) (contractBindingSnapshot, error) {
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	agentID, err := eventStringPayload(event, eventPayloadAgentIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	leaseID, err := eventStringPayload(event, eventPayloadLeaseIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	contractID, err := contractEventStringPayload(event, eventPayloadContractIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	version, err := contractEventStringPayload(event, eventPayloadVersionKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	producerMergeUnitID, err := contractEventStringPayload(event, eventPayloadProducerMergeUnitIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	producerCommit, err := contractEventStringPayload(event, eventPayloadProducerCommitKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	artifactID, err := contractEventStringPayload(event, eventPayloadArtifactIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	artifactPath, err := contractEventStringPayload(event, eventPayloadArtifactPathKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	artifactHash, err := contractEventStringPayload(event, eventPayloadArtifactHashKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	commandResults, err := contractEventCommandResults(event)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	publicationEventID, err := contractEventStringPayload(event, eventPayloadPublicationEventIDKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	publicationEventHash, err := contractEventStringPayload(event, eventPayloadPublicationEventHashKey)
	if err != nil {
		return contractBindingSnapshot{}, err
	}
	return contractBindingSnapshot{
		EventID:              event.ID,
		EventHash:            event.EventHash,
		ContractID:           contractID,
		Version:              version,
		ProducerMergeUnitID:  producerMergeUnitID,
		ProducerCommit:       producerCommit,
		MergeUnitID:          mergeUnitID,
		AttemptID:            attemptID,
		AgentID:              agentID,
		LeaseID:              leaseID,
		ArtifactID:           artifactID,
		ArtifactPath:         artifactPath,
		ArtifactHash:         artifactHash,
		CommandResults:       commandResults,
		PublicationEventID:   publicationEventID,
		PublicationEventHash: publicationEventHash,
	}, nil
}

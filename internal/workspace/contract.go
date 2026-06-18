package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EventContractPublished = "contract.published"

	eventPayloadContractIDKey          = "contract_id"
	eventPayloadVersionKey             = "version"
	eventPayloadProducerMergeUnitIDKey = "producer_merge_unit_id"
	eventPayloadProducerCommitKey      = "producer_commit"
	eventPayloadArtifactIDKey          = "artifact_id"
	eventPayloadArtifactPathKey        = "artifact_path"
	eventPayloadArtifactHashKey        = "artifact_hash"
	eventPayloadCommandResultsKey      = "command_results"
)

type ContractPublishOptions struct {
	WorkspaceDir        string
	ContractID          string
	Version             string
	ArtifactID          string
	ProducerMergeUnitID string
	ProducerCommit      string
	AttemptID           string
	AgentID             string
	LeaseID             string
	CommandResults      []ContractCommandResult
	Now                 func() time.Time
}

type ContractVerifyOptions struct {
	WorkspaceDir string
	ContractID   string
	ArtifactID   string
}

type ContractCommandResult struct {
	Command string `json:"command"`
	Status  string `json:"status"`
}

type ContractPublishResult struct {
	Status              string                  `json:"status"`
	WorkspaceDir        string                  `json:"workspace_dir"`
	WorkspaceID         string                  `json:"workspace_id"`
	BaseRef             string                  `json:"base_ref"`
	ContractID          string                  `json:"contract_id"`
	Version             string                  `json:"version"`
	ProducerMergeUnitID string                  `json:"producer_merge_unit_id"`
	ProducerCommit      string                  `json:"producer_commit"`
	AttemptID           string                  `json:"attempt_id,omitempty"`
	AgentID             string                  `json:"agent_id,omitempty"`
	LeaseID             string                  `json:"lease_id,omitempty"`
	ArtifactID          string                  `json:"artifact_id"`
	ArtifactPath        string                  `json:"artifact_path"`
	ArtifactHash        string                  `json:"artifact_hash"`
	CommandResults      []ContractCommandResult `json:"command_results"`
	EventID             string                  `json:"event_id"`
	EventHash           string                  `json:"event_hash"`
}

type ContractVerifyResult struct {
	Status              string                  `json:"status"`
	WorkspaceDir        string                  `json:"workspace_dir"`
	WorkspaceID         string                  `json:"workspace_id"`
	BaseRef             string                  `json:"base_ref"`
	ContractID          string                  `json:"contract_id"`
	Version             string                  `json:"version,omitempty"`
	ProducerMergeUnitID string                  `json:"producer_merge_unit_id,omitempty"`
	ProducerCommit      string                  `json:"producer_commit,omitempty"`
	ArtifactID          string                  `json:"artifact_id,omitempty"`
	ArtifactPath        string                  `json:"artifact_path,omitempty"`
	PublishedHash       string                  `json:"published_hash,omitempty"`
	CurrentHash         string                  `json:"current_hash,omitempty"`
	ArtifactExists      bool                    `json:"artifact_exists"`
	HashMatches         bool                    `json:"hash_matches"`
	CommandResults      []ContractCommandResult `json:"command_results,omitempty"`
	EventID             string                  `json:"event_id,omitempty"`
	EventHash           string                  `json:"event_hash,omitempty"`
}

type contractPublishProducer struct {
	mergeUnitID string
	attemptID   string
	agentID     string
	leaseID     string
	readSet     map[string]int
}

func ContractResource(id string) string {
	return resourceKey("contract", id)
}

func PublishContract(opts ContractPublishOptions) (ContractPublishResult, error) {
	opts, publishedAt, err := normalizeContractPublishOptions(opts)
	if err != nil {
		return ContractPublishResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return ContractPublishResult{}, err
	}
	gate, err := findContractGate(lock, opts.ContractID)
	if err != nil {
		return ContractPublishResult{}, err
	}
	artifact, err := selectContractArtifact(gate, opts.ArtifactID)
	if err != nil {
		return ContractPublishResult{}, err
	}
	producer, err := resolveContractPublishProducer(opts, gate, publishedAt)
	if err != nil {
		return ContractPublishResult{}, err
	}
	commandResults, err := normalizeContractCommandResults(gate, opts.CommandResults)
	if err != nil {
		return ContractPublishResult{}, err
	}
	artifactHash, err := hashContractArtifact(opts.WorkspaceDir, lock.Repo, artifact.Path)
	if err != nil {
		return ContractPublishResult{}, err
	}
	contractResource := ContractResource(gate.ID)
	readSet := map[string]int{contractResource: 0}
	for resource, revision := range producer.readSet {
		readSet[resource] = revision
	}
	revisions, err := ResourceRevisions(opts.WorkspaceDir)
	if err != nil {
		return ContractPublishResult{}, err
	}
	readSet[contractResource] = revisions[contractResource]
	payload := map[string]any{
		eventPayloadContractIDKey:          gate.ID,
		eventPayloadVersionKey:             opts.Version,
		eventPayloadProducerMergeUnitIDKey: producer.mergeUnitID,
		eventPayloadProducerCommitKey:      opts.ProducerCommit,
		eventPayloadArtifactIDKey:          artifact.ID,
		eventPayloadArtifactPathKey:        artifact.Path,
		eventPayloadArtifactHashKey:        artifactHash,
		eventPayloadCommandResultsKey:      commandResults,
	}
	if producer.attemptID != "" {
		payload[eventPayloadAttemptIDKey] = producer.attemptID
		payload[eventPayloadAgentIDKey] = producer.agentID
		payload[eventPayloadLeaseIDKey] = producer.leaseID
	}
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventContractPublished,
		Payload:      payload,
		ReadSet:      readSet,
		WriteSet:     []string{contractResource},
		Now:          func() time.Time { return publishedAt },
	})
	if err != nil {
		return ContractPublishResult{}, err
	}
	return ContractPublishResult{
		Status:              "published",
		WorkspaceDir:        opts.WorkspaceDir,
		WorkspaceID:         lock.WorkspaceID,
		BaseRef:             lock.BaseRef,
		ContractID:          gate.ID,
		Version:             opts.Version,
		ProducerMergeUnitID: producer.mergeUnitID,
		ProducerCommit:      opts.ProducerCommit,
		AttemptID:           producer.attemptID,
		AgentID:             producer.agentID,
		LeaseID:             producer.leaseID,
		ArtifactID:          artifact.ID,
		ArtifactPath:        artifact.Path,
		ArtifactHash:        artifactHash,
		CommandResults:      commandResults,
		EventID:             event.ID,
		EventHash:           event.EventHash,
	}, nil
}

func VerifyContract(opts ContractVerifyOptions) (ContractVerifyResult, error) {
	opts, err := normalizeContractVerifyOptions(opts)
	if err != nil {
		return ContractVerifyResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return ContractVerifyResult{}, err
	}
	gate, err := findContractGate(lock, opts.ContractID)
	if err != nil {
		return ContractVerifyResult{}, err
	}
	artifact, err := selectContractArtifact(gate, opts.ArtifactID)
	if err != nil {
		return ContractVerifyResult{}, err
	}
	events, err := readJournalEvents(EventsPath(opts.WorkspaceDir))
	if err != nil {
		return ContractVerifyResult{}, err
	}
	publication, found, err := latestContractPublication(events, gate.ID, artifact.ID)
	if err != nil {
		return ContractVerifyResult{}, err
	}
	if !found {
		return ContractVerifyResult{
			Status:         "unpublished",
			WorkspaceDir:   opts.WorkspaceDir,
			WorkspaceID:    lock.WorkspaceID,
			BaseRef:        lock.BaseRef,
			ContractID:     gate.ID,
			ArtifactID:     artifact.ID,
			ArtifactPath:   artifact.Path,
			ArtifactExists: artifactExists(opts.WorkspaceDir, lock.Repo, artifact.Path),
		}, nil
	}
	currentHash, exists, err := currentContractArtifactHash(opts.WorkspaceDir, lock.Repo, publication.ArtifactPath)
	if err != nil {
		return ContractVerifyResult{}, err
	}
	status := "missing"
	hashMatches := false
	if exists {
		hashMatches = currentHash == publication.ArtifactHash
		if hashMatches {
			status = "ok"
		} else {
			status = "mismatch"
		}
	}
	return ContractVerifyResult{
		Status:              status,
		WorkspaceDir:        opts.WorkspaceDir,
		WorkspaceID:         lock.WorkspaceID,
		BaseRef:             lock.BaseRef,
		ContractID:          publication.ContractID,
		Version:             publication.Version,
		ProducerMergeUnitID: publication.ProducerMergeUnitID,
		ProducerCommit:      publication.ProducerCommit,
		ArtifactID:          publication.ArtifactID,
		ArtifactPath:        publication.ArtifactPath,
		PublishedHash:       publication.ArtifactHash,
		CurrentHash:         currentHash,
		ArtifactExists:      exists,
		HashMatches:         hashMatches,
		CommandResults:      publication.CommandResults,
		EventID:             publication.EventID,
		EventHash:           publication.EventHash,
	}, nil
}

func normalizeContractPublishOptions(opts ContractPublishOptions) (ContractPublishOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ContractPublishOptions{}, time.Time{}, fmt.Errorf("workspace contract publish requires <workspace-dir>")
	}
	opts.ContractID = strings.TrimSpace(opts.ContractID)
	if opts.ContractID == "" {
		return ContractPublishOptions{}, time.Time{}, fmt.Errorf("workspace contract publish requires --contract")
	}
	opts.Version = strings.TrimSpace(opts.Version)
	if opts.Version == "" {
		return ContractPublishOptions{}, time.Time{}, fmt.Errorf("workspace contract publish requires --version")
	}
	opts.ArtifactID = strings.TrimSpace(opts.ArtifactID)
	opts.ProducerMergeUnitID = strings.TrimSpace(opts.ProducerMergeUnitID)
	opts.ProducerCommit = strings.TrimSpace(opts.ProducerCommit)
	if opts.ProducerCommit == "" {
		return ContractPublishOptions{}, time.Time{}, fmt.Errorf("workspace contract publish requires --producer-commit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func normalizeContractVerifyOptions(opts ContractVerifyOptions) (ContractVerifyOptions, error) {
	if opts.WorkspaceDir == "" {
		return ContractVerifyOptions{}, fmt.Errorf("workspace contract verify requires <workspace-dir>")
	}
	opts.ContractID = strings.TrimSpace(opts.ContractID)
	if opts.ContractID == "" {
		return ContractVerifyOptions{}, fmt.Errorf("workspace contract verify requires --contract")
	}
	opts.ArtifactID = strings.TrimSpace(opts.ArtifactID)
	return opts, nil
}

func findContractGate(lock WorkspaceLock, contractID string) (WorkspaceContractGateLock, error) {
	for _, gate := range lock.ContractGates {
		if gate.ID == contractID {
			return gate, nil
		}
	}
	return WorkspaceContractGateLock{}, fmt.Errorf("unknown contract %s", contractID)
}

func selectContractArtifact(gate WorkspaceContractGateLock, artifactID string) (WorkspaceContractArtifactLock, error) {
	if artifactID == "" {
		if len(gate.Artifacts) == 1 {
			return gate.Artifacts[0], nil
		}
		return WorkspaceContractArtifactLock{}, fmt.Errorf("contract %s has multiple artifacts; --artifact is required", gate.ID)
	}
	for _, artifact := range gate.Artifacts {
		if artifact.ID == artifactID {
			return artifact, nil
		}
	}
	return WorkspaceContractArtifactLock{}, fmt.Errorf("contract %s has no artifact %s", gate.ID, artifactID)
}

func resolveContractPublishProducer(opts ContractPublishOptions, gate WorkspaceContractGateLock, publishedAt time.Time) (contractPublishProducer, error) {
	hasExplicitProducer := opts.ProducerMergeUnitID != ""
	hasAttemptMetadata := opts.AttemptID != "" || opts.AgentID != "" || opts.LeaseID != ""
	if hasExplicitProducer && hasAttemptMetadata {
		return contractPublishProducer{}, fmt.Errorf("workspace contract publish cannot combine --producer-merge-unit with --attempt, --agent, or --lease")
	}
	if hasExplicitProducer {
		if !containsString(gate.Producers, opts.ProducerMergeUnitID) {
			return contractPublishProducer{}, fmt.Errorf("merge unit %s is not a producer for contract %s", opts.ProducerMergeUnitID, gate.ID)
		}
		return contractPublishProducer{mergeUnitID: opts.ProducerMergeUnitID}, nil
	}
	if opts.AttemptID == "" || opts.AgentID == "" || opts.LeaseID == "" {
		return contractPublishProducer{}, fmt.Errorf("workspace contract publish requires either --producer-merge-unit or --attempt, --agent, and --lease")
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, publishedAt)
	if err != nil {
		return contractPublishProducer{}, err
	}
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return contractPublishProducer{}, err
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return contractPublishProducer{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, lease.MergeUnitID, opts.AttemptID, publishedAt)
	if err != nil {
		return contractPublishProducer{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return contractPublishProducer{}, err
	}
	if !containsString(gate.Producers, current.MergeUnitID) {
		return contractPublishProducer{}, fmt.Errorf("merge unit %s is not a producer for contract %s", current.MergeUnitID, gate.ID)
	}
	leaseResource := LeaseResource(current.MergeUnitID)
	mergeUnitResource := MergeUnitResource(current.MergeUnitID)
	return contractPublishProducer{
		mergeUnitID: current.MergeUnitID,
		attemptID:   current.AttemptID,
		agentID:     current.AgentID,
		leaseID:     current.LeaseID,
		readSet: map[string]int{
			leaseResource:     state.Revisions[leaseResource],
			mergeUnitResource: state.Revisions[mergeUnitResource],
		},
	}, nil
}

func normalizeContractCommandResults(gate WorkspaceContractGateLock, values []ContractCommandResult) ([]ContractCommandResult, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("workspace contract publish requires --command-result for each validation command")
	}
	byCommand := map[string]string{}
	for _, value := range values {
		command := strings.TrimSpace(value.Command)
		status := strings.TrimSpace(value.Status)
		if command == "" || status == "" {
			return nil, fmt.Errorf("command results require non-empty command and status")
		}
		if _, exists := byCommand[command]; exists {
			return nil, fmt.Errorf("duplicate command result for %q", command)
		}
		byCommand[command] = status
	}
	results := make([]ContractCommandResult, 0, len(gate.ValidationCommands))
	for _, command := range gate.ValidationCommands {
		status, ok := byCommand[command]
		if !ok {
			return nil, fmt.Errorf("missing command result for %q", command)
		}
		delete(byCommand, command)
		results = append(results, ContractCommandResult{Command: command, Status: status})
	}
	if len(byCommand) > 0 {
		extras := make([]string, 0, len(byCommand))
		for command := range byCommand {
			extras = append(extras, command)
		}
		sort.Strings(extras)
		return nil, fmt.Errorf("command result references unknown validation command %q", extras[0])
	}
	return results, nil
}

func hashContractArtifact(workspaceDir string, repo string, artifactPath string) (string, error) {
	currentHash, exists, err := currentContractArtifactHash(workspaceDir, repo, artifactPath)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("contract artifact missing: %s", repoArtifactFullPath(workspaceDir, repo, artifactPath))
	}
	return currentHash, nil
}

func currentContractArtifactHash(workspaceDir string, repo string, artifactPath string) (string, bool, error) {
	path := repoArtifactFullPath(workspaceDir, repo, artifactPath)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true, nil
}

func artifactExists(workspaceDir string, repo string, artifactPath string) bool {
	_, exists, err := currentContractArtifactHash(workspaceDir, repo, artifactPath)
	return err == nil && exists
}

func repoArtifactFullPath(workspaceDir string, repo string, artifactPath string) string {
	return filepath.Join(resolveWorkspacePath(workspaceDir, repo), filepath.FromSlash(artifactPath))
}

type contractPublicationSnapshot struct {
	EventID             string
	EventHash           string
	ContractID          string
	Version             string
	ProducerMergeUnitID string
	ProducerCommit      string
	ArtifactID          string
	ArtifactPath        string
	ArtifactHash        string
	CommandResults      []ContractCommandResult
}

func latestContractPublication(events []JournalEvent, contractID string, artifactID string) (contractPublicationSnapshot, bool, error) {
	var latest contractPublicationSnapshot
	found := false
	for _, event := range events {
		if event.Type != EventContractPublished {
			continue
		}
		publication, err := contractPublicationFromEvent(event)
		if err != nil {
			return contractPublicationSnapshot{}, false, err
		}
		if publication.ContractID != contractID || publication.ArtifactID != artifactID {
			continue
		}
		latest = publication
		found = true
	}
	return latest, found, nil
}

func contractPublicationFromEvent(event JournalEvent) (contractPublicationSnapshot, error) {
	contractID, err := contractEventStringPayload(event, eventPayloadContractIDKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	version, err := contractEventStringPayload(event, eventPayloadVersionKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	producerMergeUnitID, err := contractEventStringPayload(event, eventPayloadProducerMergeUnitIDKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	producerCommit, err := contractEventStringPayload(event, eventPayloadProducerCommitKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	artifactID, err := contractEventStringPayload(event, eventPayloadArtifactIDKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	artifactPath, err := contractEventStringPayload(event, eventPayloadArtifactPathKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	artifactHash, err := contractEventStringPayload(event, eventPayloadArtifactHashKey)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	commandResults, err := contractEventCommandResults(event)
	if err != nil {
		return contractPublicationSnapshot{}, err
	}
	return contractPublicationSnapshot{
		EventID:             event.ID,
		EventHash:           event.EventHash,
		ContractID:          contractID,
		Version:             version,
		ProducerMergeUnitID: producerMergeUnitID,
		ProducerCommit:      producerCommit,
		ArtifactID:          artifactID,
		ArtifactPath:        artifactPath,
		ArtifactHash:        artifactHash,
		CommandResults:      commandResults,
	}, nil
}

func contractEventStringPayload(event JournalEvent, key string) (string, error) {
	value, ok := event.Payload[key]
	if !ok {
		return "", fmt.Errorf("contract event %s missing payload %s", event.ID, key)
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return "", fmt.Errorf("contract event %s payload %s must be a string", event.ID, key)
	}
	return text, nil
}

func contractEventCommandResults(event JournalEvent) ([]ContractCommandResult, error) {
	raw, ok := event.Payload[eventPayloadCommandResultsKey]
	if !ok {
		return nil, fmt.Errorf("contract event %s missing payload %s", event.ID, eventPayloadCommandResultsKey)
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("contract event %s payload %s must be a list", event.ID, eventPayloadCommandResultsKey)
	}
	results := make([]ContractCommandResult, 0, len(values))
	for i, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("contract event %s command result %d must be an object", event.ID, i+1)
		}
		command, commandOK := item["command"].(string)
		status, statusOK := item["status"].(string)
		if !commandOK || command == "" || !statusOK || status == "" {
			return nil, fmt.Errorf("contract event %s command result %d requires command and status", event.ID, i+1)
		}
		results = append(results, ContractCommandResult{Command: command, Status: status})
	}
	return results, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

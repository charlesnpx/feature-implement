package workspacecmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

const requestSchemaVersion = 2

// MaxCommandInputBytes is the allocation boundary enforced by the CLI before
// a workspace request reaches strict decoding.
const MaxCommandInputBytes = workspace.MaxArtifactBytes

type Options struct {
	Action             string
	Subaction          string
	BundleDir          string
	CandidateBundleDir string
	WorkspaceDir       string
	Input              []byte
	WriteLocks         bool
	GeneratorVersion   string
}

type ValidationResult struct {
	SchemaVersion    int      `json:"schema_version"`
	Status           string   `json:"status"`
	BundleRoot       string   `json:"bundle_root"`
	WorkspaceID      string   `json:"workspace_id"`
	Generation       string   `json:"generation"`
	DescriptorDigest string   `json:"descriptor_digest"`
	LockRoot         string   `json:"lock_root,omitempty"`
	Created          []string `json:"created,omitempty"`
	Updated          []string `json:"updated,omitempty"`
	Deleted          []string `json:"deleted,omitempty"`
}

type InitializationResult struct {
	SchemaVersion    int                       `json:"schema_version"`
	Status           string                    `json:"status"`
	WorkspaceDir     string                    `json:"workspace_dir"`
	WorkspaceID      string                    `json:"workspace_id"`
	Generation       string                    `json:"generation"`
	PlanCheckpoint   string                    `json:"plan_checkpoint"`
	JournalHead      string                    `json:"journal_head"`
	ProjectionDigest string                    `json:"projection_digest"`
	Report           workspace.WorkspaceReport `json:"report"`
}

type MutationResult struct {
	SchemaVersion int                       `json:"schema_version"`
	Status        string                    `json:"status"`
	Action        string                    `json:"action"`
	Directives    []BoundaryDirectiveView   `json:"directives,omitempty"`
	Report        workspace.WorkspaceReport `json:"report"`
}

type BoundaryDirectiveView struct {
	Kind            string   `json:"kind"`
	WorkspaceID     string   `json:"workspace_id"`
	Generation      string   `json:"generation"`
	AttemptID       string   `json:"attempt_id"`
	BoundaryID      string   `json:"boundary_id"`
	GoalID          string   `json:"goal_id"`
	GoalScope       string   `json:"goal_scope"`
	Head            string   `json:"head"`
	DirectiveDigest string   `json:"directive_digest"`
	IdempotencyKey  string   `json:"idempotency_key,omitempty"`
	Choices         []string `json:"choices,omitempty"`
}

func Execute(ctx context.Context, options Options) (any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("workspace command requires context")
	}
	action := strings.TrimSpace(options.Action)
	switch action {
	case "schema":
		switch strings.TrimSpace(options.Subaction) {
		case "", "bundle":
			return workspace.WorkspaceBundleSchema(), nil
		case "requests":
			return RequestSchemas(), nil
		default:
			return nil, fmt.Errorf("unsupported workspace schema %q", options.Subaction)
		}
	case "validate", "init", "status", "recover", "scheduler", "gates", "queue", "receipts", "report":
		// handled below
	case "reconcile", "attempt", "commit", "review", "control", "provider", "complete":
		// handled below
	default:
		return nil, fmt.Errorf("unsupported workspace command %q", action)
	}
	if strings.TrimSpace(options.BundleDir) == "" {
		return nil, fmt.Errorf("workspace %s requires --bundle <dir>", action)
	}
	bundle, err := workspace.LoadWorkspaceBundle(options.BundleDir)
	if err != nil {
		return nil, err
	}
	if options.GeneratorVersion == "" {
		options.GeneratorVersion = "dev"
	}
	switch action {
	case "validate":
		return validateBundle(bundle, options)
	case "init":
		return initializeWorkspace(ctx, bundle, options)
	case "status", "report":
		return readReport(bundle, options.WorkspaceDir)
	case "scheduler":
		report, err := readReport(bundle, options.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		return report.Scheduler, nil
	case "gates":
		report, err := readReport(bundle, options.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		return report.Gates, nil
	case "queue":
		report, err := readReport(bundle, options.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		return report.Queue, nil
	case "receipts":
		report, err := readReport(bundle, options.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		return report.Receipts, nil
	case "recover":
		return recoverWorkspace(bundle, options)
	case "reconcile":
		return executeReconciliation(ctx, bundle, options)
	case "attempt":
		return executeAttempt(ctx, bundle, options)
	case "commit":
		return executeCommit(ctx, bundle, options)
	case "review":
		return executeReview(ctx, bundle, options)
	case "control":
		return executeControl(ctx, bundle, options)
	case "provider":
		return executeProvider(ctx, bundle, options)
	case "complete":
		return executeCompletion(ctx, bundle, options)
	default:
		panic("unreachable")
	}
}

// BundleExample is discovery-only example metadata. The referenced source
// files remain the authority and must be supplied beside this descriptor.
func BundleExample() string {
	return `{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/example.yaml"],
  "execution_config": "config/execution.yaml",
  "authorities": [
    {
      "id": "owner-policy",
      "kind": "git_blob",
      "content_path": "authority/owner-policy.yaml",
      "repository_identity": "https://github.com/example/policy.git",
      "commit_object": "sha1:1111111111111111111111111111111111111111",
      "blob_object": "sha1:2222222222222222222222222222222222222222"
    }
  ],
  "control_plane_authority": "owner-policy"
}
`
}

// RequestSchemas describes every accepted mutation envelope. Runtime decoding
// remains authoritative and rejects unknown fields and trailing JSON.
func RequestSchemas() map[string]any {
	stringProperty := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	optionalString := func() map[string]any { return map[string]any{"type": "string"} }
	integerProperty := func(minimum int) map[string]any { return map[string]any{"type": "integer", "minimum": minimum} }
	booleanProperty := func() map[string]any { return map[string]any{"type": "boolean"} }
	enumProperty := func(values ...string) map[string]any { return map[string]any{"enum": values} }
	arrayOfStrings := func() map[string]any {
		return map[string]any{"type": "array", "uniqueItems": true, "items": stringProperty()}
	}
	arrayOfEnum := func(values ...string) map[string]any {
		return map[string]any{"type": "array", "uniqueItems": true, "items": enumProperty(values...)}
	}
	request := func(required []string, properties map[string]any) map[string]any {
		properties["schema_version"] = map[string]any{"const": requestSchemaVersion}
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required": append([]string{"schema_version"}, required...), "properties": properties,
		}
	}
	goal := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"id", "scope"},
		"properties": map[string]any{"id": stringProperty(), "scope": enumProperty("merge_unit", "workspace")},
	}
	receipt := map[string]any{"type": "object"}
	evidenceItem := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"name", "value"},
		"properties": map[string]any{"name": stringProperty(), "value": stringProperty()},
	}
	evidence := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"kind", "digest", "items"},
		"properties": map[string]any{
			"kind": stringProperty(), "digest": stringProperty(),
			"items": map[string]any{"type": "array", "items": evidenceItem},
		},
	}
	reviewFinding := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"severity", "category", "path", "line", "summary", "evidence_digest"},
		"properties": map[string]any{
			"severity": enumProperty("critical", "high", "medium", "low"), "category": stringProperty(),
			"path": map[string]any{"type": "string"}, "line": integerProperty(0),
			"summary": stringProperty(), "evidence_digest": stringProperty(),
		},
	}
	isolation := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"repository_read_only", "scratch_ephemeral", "credentials_available", "repository_hooks", "write_network", "provider_broker", "external_write"},
		"properties": map[string]any{
			"repository_read_only": booleanProperty(), "scratch_ephemeral": booleanProperty(),
			"credentials_available": booleanProperty(), "repository_hooks": booleanProperty(),
			"write_network": booleanProperty(), "provider_broker": booleanProperty(), "external_write": booleanProperty(),
		},
	}
	occurred := func(properties map[string]any) map[string]any {
		properties["occurred_at"] = map[string]any{"type": "string", "format": "date-time"}
		return properties
	}
	attemptIdentity := func() map[string]any {
		return occurred(map[string]any{"attempt_id": stringProperty()})
	}
	schemas := map[string]any{
		"init":            request([]string{"occurred_at"}, occurred(map[string]any{})),
		"recover":         request([]string{"occurred_at"}, occurred(map[string]any{})),
		"reconcile.stage": request([]string{"occurred_at"}, occurred(map[string]any{})),
		"reconcile.plan":  request(nil, map[string]any{}),
		"reconcile.activate": request([]string{"occurred_at", "token", "receipt"}, occurred(map[string]any{
			"token": map[string]any{"type": "object"}, "receipt": receipt,
		})),
		"attempt.reserve": request([]string{"occurred_at", "plan_id", "merge_unit_id", "attempt_number", "base", "worktree_root", "goal"}, occurred(map[string]any{
			"plan_id": stringProperty(), "merge_unit_id": stringProperty(), "attempt_number": integerProperty(1),
			"base": stringProperty(), "worktree_root": stringProperty(), "goal": goal,
		})),
		"attempt.materialize": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"attempt.adopt-head":  request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"attempt.boundary": request([]string{"occurred_at", "attempt_id", "evidence"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "evidence": map[string]any{"type": "array", "items": evidence},
		})),
		"attempt.next-goal": request([]string{"occurred_at", "attempt_id", "goal"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "goal": goal,
		})),
		"attempt.acknowledge": request([]string{"occurred_at", "attempt_id", "kind", "goal", "receipt"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "kind": enumProperty("goal_completed", "next_goal_created"), "goal": goal, "receipt": receipt,
		})),
		"attempt.owner-response": request([]string{"occurred_at", "attempt_id", "response", "receipt"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "response": enumProperty("continue"), "receipt": receipt,
		})),
		"attempt.resume": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"commit.next": request([]string{"occurred_at", "attempt_id"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "body": optionalString(),
		})),
		"commit.rebase": request([]string{"occurred_at", "attempt_id", "new_base", "new_head"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "new_base": stringProperty(), "new_head": stringProperty(),
		})),
		"review.start": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"review.reserve": request([]string{"occurred_at", "attempt_id", "reviewer_instance", "idempotency_key"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "reviewer_instance": stringProperty(), "idempotency_key": stringProperty(),
		})),
		"review.record": request([]string{"occurred_at", "attempt_id", "reservation_digest", "request_digest", "reviewer_instance", "status", "findings", "isolation", "receipt"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "reservation_digest": stringProperty(), "request_digest": stringProperty(),
			"reviewer_instance": stringProperty(), "status": enumProperty("completed", "infrastructure_failure"),
			"findings":               map[string]any{"type": "array", "items": reviewFinding},
			"infrastructure_failure": optionalString(), "isolation": isolation, "receipt": receipt,
		})),
		"review.reserve-fix": request([]string{"occurred_at", "attempt_id", "ordinal", "accepted_finding_ids"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "ordinal": integerProperty(1), "accepted_finding_ids": arrayOfStrings(), "body": optionalString(),
		})),
		"review.apply-fix": request([]string{"occurred_at", "attempt_id", "ordinal", "accepted_finding_ids"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "ordinal": integerProperty(1), "accepted_finding_ids": arrayOfStrings(), "body": optionalString(),
		})),
		"review.record-fix": request([]string{"occurred_at", "attempt_id", "ordinal", "accepted_finding_ids"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "ordinal": integerProperty(1), "accepted_finding_ids": arrayOfStrings(), "body": optionalString(),
		})),
		"review.ready": request([]string{"attempt_id"}, map[string]any{"attempt_id": stringProperty()}),
		"control.grant": request([]string{"occurred_at", "serial_segment", "base", "head", "actions", "expires_at", "epoch", "requires_provider_pull_request", "receipt"}, occurred(map[string]any{
			"serial_segment": stringProperty(), "base": stringProperty(), "head": stringProperty(),
			"actions":    arrayOfEnum("push", "open_pull_request", "merge"),
			"expires_at": map[string]any{"type": "string", "format": "date-time"}, "epoch": integerProperty(1),
			"requires_provider_pull_request": booleanProperty(), "receipt": receipt,
		})),
		"control.revoke": request([]string{"occurred_at", "next_epoch", "reason", "receipt"}, occurred(map[string]any{
			"target_grant": optionalString(), "next_epoch": integerProperty(1), "reason": stringProperty(), "receipt": receipt,
		})),
		"control.safety": request([]string{"occurred_at", "gates_blocked", "reconciliation_pending", "drift_detected", "ambiguous_effect", "receipt"}, occurred(map[string]any{
			"gates_blocked": booleanProperty(), "reconciliation_pending": booleanProperty(), "drift_detected": booleanProperty(),
			"ambiguous_effect": booleanProperty(), "receipt": receipt,
		})),
		"control.segment-complete": request([]string{"occurred_at", "serial_segment"}, occurred(map[string]any{"serial_segment": stringProperty()})),
		"control.inspect-receipt":  request([]string{"receipt"}, map[string]any{"receipt": receipt}),
		"provider.reserve": request([]string{"occurred_at", "kind", "attempt_id", "branch", "head", "tree"}, occurred(map[string]any{
			"kind": enumProperty("push", "open_pull_request", "merge"), "attempt_id": stringProperty(),
			"branch": stringProperty(), "head": stringProperty(), "tree": stringProperty(),
			"expected_remote_head": optionalString(), "expect_remote_absent": booleanProperty(), "integration_base_head": optionalString(),
			"title": optionalString(), "body": optionalString(),
		})),
		"provider.preflight":    request([]string{"occurred_at", "intent_id"}, occurred(map[string]any{"intent_id": stringProperty()})),
		"provider.dispatch":     request([]string{"occurred_at", "intent_id"}, occurred(map[string]any{"intent_id": stringProperty()})),
		"provider.reconcile":    request([]string{"occurred_at", "intent_id"}, occurred(map[string]any{"intent_id": stringProperty()})),
		"provider.abandon":      request([]string{"occurred_at", "intent_id"}, occurred(map[string]any{"intent_id": stringProperty()})),
		"provider.authorize-pr": request([]string{"occurred_at", "intent_id"}, occurred(map[string]any{"intent_id": stringProperty()})),
		"complete.verify":       request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
	}
	return map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "schema_version": requestSchemaVersion,
		"requests": schemas,
	}
}

func validateBundle(bundle workspace.WorkspaceBundle, options Options) (ValidationResult, error) {
	if err := bundle.VerifyRoot(); err != nil {
		return ValidationResult{}, err
	}
	result := ValidationResult{
		SchemaVersion: requestSchemaVersion, Status: "valid", BundleRoot: bundle.Root(),
		WorkspaceID: bundle.Definition().Workspace().ID().String(), Generation: bundle.Definition().Generation().String(),
		DescriptorDigest: bundle.DescriptorDigest().String(), Created: []string{}, Updated: []string{}, Deleted: []string{},
	}
	if !options.WriteLocks {
		if err := bundle.VerifyRoot(); err != nil {
			return ValidationResult{}, err
		}
		return result, nil
	}
	artifacts, err := workspace.WorkspaceBundleLockArtifacts(bundle)
	if err != nil {
		return ValidationResult{}, err
	}
	lockRoot := filepath.Join(bundle.Root(), workspace.WorkspaceGeneratedDirectory)
	materialized, err := workspace.SynchronizeMaterialization(
		lockRoot,
		workspace.PlanCheckpointGeneratorVersion,
		artifacts,
		workspace.MaterializationOptions{},
	)
	if err != nil {
		return ValidationResult{}, err
	}
	result.Created = materialized.Created()
	result.Updated = materialized.Updated()
	result.Deleted = materialized.Deleted()
	result.LockRoot = lockRoot
	if err := bundle.VerifyRoot(); err != nil {
		return ValidationResult{}, err
	}
	return result, nil
}

type initializeRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
}

func initializeWorkspace(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (result InitializationResult, resultErr error) {
	workspaceDir, err := absoluteDirectory(options.WorkspaceDir, "workspace")
	if err != nil {
		return InitializationResult{}, err
	}
	var request initializeRequest
	if err := decodeRequest(options.Input, &request); err != nil {
		return InitializationResult{}, err
	}
	occurredAt, err := parseOccurredAt(request.SchemaVersion, request.OccurredAt)
	if err != nil {
		return InitializationResult{}, err
	}
	definition := bundle.Definition()
	roots, err := workspace.OpenWorkspaceInitializationRootGuard(
		bundle.Root(), workspaceDir, definition.Workspace().RepositoryRoot(),
	)
	if err != nil {
		return InitializationResult{}, err
	}
	defer roots.Close()
	if err := roots.VerifyBeforeRuntimeCreation(); err != nil {
		return InitializationResult{}, err
	}
	defer func() {
		var verifyErr error
		if resultErr == nil {
			verifyErr = roots.VerifyAfterRuntimeCreation()
		} else {
			verifyErr = roots.VerifyAfterEffects()
		}
		if verifyErr != nil {
			result = InitializationResult{}
			resultErr = errors.Join(resultErr, verifyErr)
		}
	}()
	if err := bundle.VerifyRoot(); err != nil {
		return InitializationResult{}, err
	}
	checkpoint, err := workspace.VerifyPlanLockCheckpoint(ctx, bundle)
	if err != nil {
		return InitializationResult{}, err
	}
	if err := bundle.VerifyRoot(); err != nil {
		return InitializationResult{}, err
	}
	if err := roots.VerifyBeforeRuntimeCreation(); err != nil {
		return InitializationResult{}, err
	}
	initialized, err := workspace.InitializeWorkspaceV2(
		workspaceDir,
		definition,
		occurredAt,
		checkpoint.Commit(),
	)
	if err != nil {
		return InitializationResult{}, err
	}
	if err := roots.VerifyAfterRuntimeCreation(); err != nil {
		return InitializationResult{}, err
	}
	if err := bundle.VerifyRoot(); err != nil {
		return InitializationResult{}, err
	}
	report, err := workspace.RebuildWorkspaceReport(initialized.Snapshot(), definition)
	if err != nil {
		return InitializationResult{}, err
	}
	result = InitializationResult{
		SchemaVersion: requestSchemaVersion, Status: "initialized", WorkspaceDir: workspaceDir,
		WorkspaceID: initialized.Runtime().WorkspaceID().String(), Generation: initialized.Runtime().ActiveGeneration().String(),
		PlanCheckpoint: initialized.Runtime().PlanCheckpoint().String(),
		JournalHead:    initialized.Snapshot().Head().String(), ProjectionDigest: initialized.ProjectionDigest().String(), Report: report,
	}
	return result, nil
}

func readReport(bundle workspace.WorkspaceBundle, workspaceDir string) (workspace.WorkspaceReport, error) {
	directory, err := absoluteDirectory(workspaceDir, "workspace")
	if err != nil {
		return workspace.WorkspaceReport{}, err
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(directory)
	if err != nil {
		return workspace.WorkspaceReport{}, err
	}
	return workspace.RebuildWorkspaceReport(snapshot, bundle.Definition())
}

type recoverRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
}

func recoverWorkspace(bundle workspace.WorkspaceBundle, options Options) (MutationResult, error) {
	directory, err := absoluteDirectory(options.WorkspaceDir, "workspace")
	if err != nil {
		return MutationResult{}, err
	}
	var request recoverRequest
	if err := decodeRequest(options.Input, &request); err != nil {
		return MutationResult{}, err
	}
	occurredAt, err := parseOccurredAt(request.SchemaVersion, request.OccurredAt)
	if err != nil {
		return MutationResult{}, err
	}
	journal, err := workspace.OpenWorkspaceJournal(directory, workspace.JournalReadWrite)
	if err != nil {
		return MutationResult{}, err
	}
	defer journal.Close()
	if _, err := journal.RecoverIncompleteTail(bundle.Definition().Workspace().ID(), occurredAt); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("recover", journal, bundle.Definition(), nil)
}

func mutationResult(
	action string,
	journal *workspace.WorkspaceJournal,
	definition workspace.EffectiveWorkspaceDefinition,
	directives []BoundaryDirectiveView,
) (MutationResult, error) {
	if _, err := workspace.RebuildWorkspaceRuntimeProjectionFile(journal); err != nil {
		return MutationResult{}, err
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return MutationResult{}, err
	}
	report, err := workspace.RebuildWorkspaceReport(snapshot, definition)
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{
		SchemaVersion: requestSchemaVersion, Status: "recorded", Action: action,
		Directives: append([]BoundaryDirectiveView(nil), directives...), Report: report,
	}, nil
}

func decodeRequest(source []byte, target any) error {
	if len(source) == 0 {
		return fmt.Errorf("workspace command requires --input <json-file|->")
	}
	if len(source) > workspace.MaxArtifactBytes {
		return fmt.Errorf("workspace command input exceeds %d bytes", workspace.MaxArtifactBytes)
	}
	if err := workspace.DecodeStrictJSON(source, target); err != nil {
		return fmt.Errorf("decode workspace command input: %w", err)
	}
	return nil
}

func parseOccurredAt(schema int, value string) (time.Time, error) {
	if schema != requestSchemaVersion {
		return time.Time{}, fmt.Errorf("workspace command schema_version must be %d", requestSchemaVersion)
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil || parsed.IsZero() {
		return time.Time{}, fmt.Errorf("workspace command occurred_at must be RFC3339Nano")
	}
	return parsed.UTC(), nil
}

func absoluteDirectory(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s directory is required", label)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func parseID(value, label string) (workspace.ID, error) {
	id, err := workspace.NewID(value)
	if err != nil {
		return workspace.ID{}, fmt.Errorf("%s: %w", label, err)
	}
	return id, nil
}

func parseDigest(value, label string) (workspace.Digest, error) {
	digest, err := workspace.ParseDigest(value)
	if err != nil {
		return workspace.Digest{}, fmt.Errorf("%s: %w", label, err)
	}
	return digest, nil
}

func parseGitObject(value, label string) (workspace.GitObjectID, error) {
	object, err := workspace.ParseGitObjectID(value)
	if err != nil {
		return workspace.GitObjectID{}, fmt.Errorf("%s: %w", label, err)
	}
	return object, nil
}

func openWritableJournal(options Options) (*workspace.WorkspaceJournal, string, error) {
	directory, err := absoluteDirectory(options.WorkspaceDir, "workspace")
	if err != nil {
		return nil, "", err
	}
	journal, err := workspace.OpenWorkspaceJournal(directory, workspace.JournalReadWrite)
	return journal, directory, err
}

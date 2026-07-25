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
	Action           string
	Subaction        string
	BundleDir        string
	WorkspaceDir     string
	Input            []byte
	WriteLocks       bool
	GeneratorVersion string
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
		case "reports":
			return ReportSchemas(), nil
		default:
			return nil, fmt.Errorf("unsupported workspace schema %q", options.Subaction)
		}
	case "queue", "receipts", "reconcile", "control", "provider":
		return nil, removedWorkspaceCommand(action)
	case "validate", "init", "status", "recover", "scheduler", "gates", "report":
		// handled below
	case "attempt", "commit", "review", "integrate", "complete":
		// handled below
	default:
		return nil, fmt.Errorf("unsupported workspace command %q", action)
	}
	if action == "commit" && strings.TrimSpace(options.Subaction) == "rebase" {
		return nil, fmt.Errorf(
			"workspace commit rebase was removed; attempt bases are immutable",
		)
	}
	if err := validateWorkspaceSubaction(action, options.Subaction); err != nil {
		return nil, err
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
		return validateBundle(ctx, bundle, options)
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
	case "recover":
		return recoverWorkspace(bundle, options)
	case "attempt":
		return executeAttempt(ctx, bundle, options)
	case "commit":
		return executeCommit(ctx, bundle, options)
	case "review":
		return executeReview(ctx, bundle, options)
	case "integrate":
		return executeIntegration(bundle, options)
	case "complete":
		return executeCompletion(bundle, options)
	default:
		panic("unreachable")
	}
}

func removedWorkspaceCommand(action string) error {
	return fmt.Errorf(
		"workspace %s was removed from the local-only workflow",
		action,
	)
}

func validateWorkspaceSubaction(action, subaction string) error {
	subaction = strings.TrimSpace(subaction)
	var supported map[string]struct{}
	switch action {
	case "attempt":
		supported = stringSet(
			"reserve", "materialize", "adopt-head", "boundary",
			"next-goal", "acknowledge", "owner-response", "resume",
		)
	case "commit":
		supported = stringSet("next")
	case "review":
		supported = stringSet(
			"start", "reserve", "record", "reserve-fix",
			"apply-fix", "record-fix", "ready",
		)
	case "integrate":
		supported = stringSet("merge-unit")
	case "complete":
		supported = stringSet("verify")
	default:
		return nil
	}
	if _, ok := supported[subaction]; !ok {
		return fmt.Errorf(
			"unsupported workspace %s action %q",
			action, subaction,
		)
	}
	return nil
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// BundleExample is discovery-only example metadata. The referenced source
// files remain the authority and must be supplied beside this descriptor.
func BundleExample() string {
	return `{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/example.yaml"],
  "execution_config": "config/execution.yaml"
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
		"required": []string{"repository_read_only", "scratch_ephemeral", "repository_hooks", "write_network", "external_write"},
		"properties": map[string]any{
			"repository_read_only": booleanProperty(), "scratch_ephemeral": booleanProperty(),
			"repository_hooks": booleanProperty(), "write_network": booleanProperty(),
			"external_write": booleanProperty(),
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
		"init": request([]string{"occurred_at", "worktree_root"}, occurred(map[string]any{
			"worktree_root": stringProperty(),
		})),
		"recover": request([]string{"occurred_at"}, occurred(map[string]any{})),
		"attempt.reserve": request([]string{"occurred_at", "plan_id", "merge_unit_id", "attempt_number", "goal"}, occurred(map[string]any{
			"plan_id": stringProperty(), "merge_unit_id": stringProperty(), "attempt_number": integerProperty(1),
			"goal": goal,
		})),
		"attempt.materialize": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"attempt.adopt-head":  request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"attempt.boundary": request([]string{"occurred_at", "attempt_id", "evidence"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "evidence": map[string]any{"type": "array", "items": evidence},
		})),
		"attempt.next-goal": request([]string{"occurred_at", "attempt_id", "goal"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "goal": goal,
		})),
		"attempt.acknowledge": request([]string{
			"occurred_at", "attempt_id", "kind", "directive_digest", "goal", "idempotency_key",
		}, occurred(map[string]any{
			"attempt_id": stringProperty(), "kind": enumProperty("goal_completed", "next_goal_created"),
			"directive_digest": stringProperty(), "goal": goal, "idempotency_key": stringProperty(),
		})),
		"attempt.owner-response": request([]string{
			"occurred_at", "attempt_id", "boundary_id", "directive_digest", "goal", "expected_head", "response",
		}, occurred(map[string]any{
			"attempt_id": stringProperty(), "boundary_id": stringProperty(),
			"directive_digest": stringProperty(), "goal": goal,
			"expected_head": stringProperty(), "response": enumProperty("continue"),
		})),
		"attempt.resume": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"commit.next": request([]string{"occurred_at", "attempt_id"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "body": optionalString(),
		})),
		"review.start": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"review.reserve": request([]string{"occurred_at", "attempt_id", "reviewer_instance", "idempotency_key"}, occurred(map[string]any{
			"attempt_id": stringProperty(),
			"reviewer_instance": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Descriptive local reviewer label; not an authenticated identity.",
			},
			"idempotency_key": stringProperty(),
		})),
		"review.record": request([]string{
			"occurred_at", "attempt_id", "reservation_digest", "request_digest",
			"reviewer_instance", "status", "findings", "isolation",
		}, occurred(map[string]any{
			"attempt_id": stringProperty(), "reservation_digest": stringProperty(), "request_digest": stringProperty(),
			"reviewer_instance": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Descriptive local reviewer label; not an authenticated identity.",
			},
			"status":                 enumProperty("completed", "infrastructure_failure"),
			"findings":               map[string]any{"type": "array", "items": reviewFinding},
			"infrastructure_failure": optionalString(), "isolation": isolation,
		})),
		"review.reserve-fix": request([]string{"occurred_at", "attempt_id", "ordinal", "accepted_finding_ids"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "ordinal": integerProperty(1), "accepted_finding_ids": arrayOfStrings(),
		})),
		"review.apply-fix": request([]string{"occurred_at", "attempt_id", "ordinal", "accepted_finding_ids"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "ordinal": integerProperty(1), "accepted_finding_ids": arrayOfStrings(), "body": optionalString(),
		})),
		"review.record-fix": request([]string{"occurred_at", "attempt_id", "ordinal", "accepted_finding_ids"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "ordinal": integerProperty(1), "accepted_finding_ids": arrayOfStrings(),
		})),
		"review.ready": request([]string{"attempt_id"}, map[string]any{"attempt_id": stringProperty()}),
		"integrate.merge-unit": request([]string{"occurred_at", "attempt_id"}, occurred(map[string]any{
			"attempt_id": stringProperty(),
		})),
		"complete.verify": request([]string{"occurred_at"}, occurred(map[string]any{})),
	}
	return map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "schema_version": requestSchemaVersion,
		"requests": schemas,
	}
}

func validateBundle(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (ValidationResult, error) {
	if err := bundle.VerifyRoot(); err != nil {
		return ValidationResult{}, err
	}
	if _, err := workspace.ValidateLocalTarget(
		ctx, bundle.Definition().Workspace(),
	); err != nil {
		return ValidationResult{}, fmt.Errorf(
			"validate local target: %w", err,
		)
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
		if _, err := workspace.ValidateLocalTarget(
			ctx, bundle.Definition().Workspace(),
		); err != nil {
			return ValidationResult{}, fmt.Errorf(
				"revalidate local target: %w", err,
			)
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
	if _, err := workspace.ValidateLocalTarget(
		ctx, bundle.Definition().Workspace(),
	); err != nil {
		return ValidationResult{}, fmt.Errorf(
			"revalidate local target: %w", err,
		)
	}
	return result, nil
}

type initializeRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	WorktreeRoot  string `json:"worktree_root"`
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
	worktreeRoot := filepath.Clean(strings.TrimSpace(request.WorktreeRoot))
	if !filepath.IsAbs(worktreeRoot) {
		return InitializationResult{}, fmt.Errorf(
			"workspace init worktree_root must be absolute",
		)
	}
	definition := bundle.Definition()
	roots, err := workspace.OpenWorkspaceInitializationRootGuard(
		bundle.Root(), workspaceDir, definition.Workspace().RepositoryRoot(),
		worktreeRoot,
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
	var initialized workspace.WorkspaceInitializationResult
	_, err = workspace.WithVerifiedPlanLockCheckpoint(
		ctx,
		bundle,
		func(checkpoint workspace.VerifiedPlanLockCheckpoint) error {
			if err := bundle.VerifyRoot(); err != nil {
				return err
			}
			if err := roots.VerifyBeforeRuntimeCreation(); err != nil {
				return err
			}
			var initializeErr error
			initialized, initializeErr = workspace.InitializeWorkspaceV2WithOptions(
				ctx,
				workspaceDir,
				definition,
				occurredAt,
				workspace.WorkspaceInitializationOptions{
					PlanCheckpoint: &checkpoint,
					WorktreeRoot:   worktreeRoot,
				},
			)
			return initializeErr
		},
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

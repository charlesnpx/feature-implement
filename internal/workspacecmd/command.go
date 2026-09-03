package workspacecmd

import (
	"context"
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
	LockPath         string   `json:"lock_path,omitempty"`
	Created          []string `json:"created,omitempty"`
	Updated          []string `json:"updated,omitempty"`
	Deleted          []string `json:"deleted,omitempty"`
}

type InitializationResult struct {
	SchemaVersion    int                     `json:"schema_version"`
	Status           string                  `json:"status"`
	WorkspaceDir     string                  `json:"workspace_dir"`
	WorkspaceID      string                  `json:"workspace_id"`
	Generation       string                  `json:"generation"`
	PlanCheckpoint   string                  `json:"plan_checkpoint"`
	JournalHead      string                  `json:"journal_head"`
	ProjectionDigest string                  `json:"projection_digest"`
	Report           workspace.WorkspaceView `json:"report"`
}

type MutationResult struct {
	SchemaVersion int                     `json:"schema_version"`
	Status        string                  `json:"status"`
	Action        string                  `json:"action"`
	Report        workspace.WorkspaceView `json:"report"`
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
			return WorkspaceViewSchema(), nil
		default:
			return nil, fmt.Errorf("unsupported workspace schema %q", options.Subaction)
		}
	case "queue", "receipts", "reconcile", "control", "provider":
		return nil, removedWorkspaceCommand(action)
	case "validate", "init", "status", "recover":
		// handled below
	case "attempt", "review", "integrate", "complete":
		// handled below
	default:
		return nil, fmt.Errorf("unsupported workspace command %q", action)
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
	if action != "validate" {
		workspaceDir, directoryErr := resolvedWorkspaceDirectory(bundle, options.WorkspaceDir)
		if directoryErr != nil {
			return nil, directoryErr
		}
		options.WorkspaceDir = workspaceDir
	}
	if options.GeneratorVersion == "" {
		options.GeneratorVersion = "dev"
	}
	switch action {
	case "validate":
		return validateBundle(ctx, bundle, options)
	case "init":
		return initializeWorkspace(ctx, bundle, options)
	case "status":
		return readWorkspaceView(ctx, bundle, options.WorkspaceDir)
	case "recover":
		return recoverWorkspace(ctx, bundle, options)
	case "attempt":
		return executeAttempt(ctx, bundle, options)
	case "review":
		return executeReview(ctx, bundle, options)
	case "integrate":
		return executeIntegration(ctx, bundle, options)
	case "complete":
		return executeCompletion(ctx, bundle, options)
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
			"start", "adopt-head", "pause", "resume", "abandon",
		)
	case "review":
		supported = stringSet(
			"dispatch", "record", "record-document", "ready",
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
	integerProperty := func(minimum int) map[string]any { return map[string]any{"type": "integer", "minimum": minimum} }
	enumProperty := func(values ...string) map[string]any { return map[string]any{"enum": values} }
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
	occurred := func(properties map[string]any) map[string]any {
		properties["occurred_at"] = map[string]any{"type": "string", "format": "date-time"}
		return properties
	}
	attemptIdentity := func() map[string]any {
		return occurred(map[string]any{"attempt_id": stringProperty()})
	}
	schemas := map[string]any{
		"init":    request([]string{"occurred_at"}, occurred(map[string]any{})),
		"recover": request([]string{"occurred_at"}, occurred(map[string]any{})),
		"attempt.start": request([]string{"occurred_at", "plan_id", "merge_unit_id", "attempt_number", "goal"}, occurred(map[string]any{
			"plan_id": stringProperty(), "merge_unit_id": stringProperty(), "attempt_number": integerProperty(1),
			"goal": goal,
		})),
		"attempt.adopt-head": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"attempt.pause": request([]string{"occurred_at", "attempt_id", "kind", "evidence"}, occurred(map[string]any{
			"attempt_id": stringProperty(), "kind": enumProperty("checkpoint", "escalation"),
			"evidence": map[string]any{"type": "array", "items": evidence},
		})),
		"attempt.resume":  request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"attempt.abandon": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"review.dispatch": request([]string{"occurred_at", "attempt_id"}, attemptIdentity()),
		"review.record": request([]string{
			"occurred_at", "attempt_id", "dispatch_digest", "verdict", "evidence_digest",
		}, occurred(map[string]any{
			"attempt_id": stringProperty(), "dispatch_digest": stringProperty(),
			"verdict":         enumProperty("satisfied", "not_satisfied", "failed_to_run"),
			"evidence_digest": stringProperty(),
		})),
		"review.record-document": request([]string{
			"occurred_at", "attempt_id", "dispatch_digest", "verdict", "document",
		}, occurred(map[string]any{
			"attempt_id": stringProperty(), "dispatch_digest": stringProperty(),
			"verdict": enumProperty("satisfied", "not_satisfied"),
			"document": map[string]any{
				"type":        "object",
				"description": "Raw review-report-v1 document; the Witness contract performs strict decoding and validation.",
			},
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
	materialized, err := workspace.WriteWorkspaceBundleLock(
		bundle, workspace.WorkspaceLockWriteOptions{},
	)
	if err != nil {
		return ValidationResult{}, err
	}
	result.LockPath = filepath.Join(bundle.Root(), workspace.WorkspaceLockFileName)
	if materialized.Created() {
		result.Created = []string{workspace.WorkspaceLockFileName}
	}
	if materialized.Updated() {
		result.Updated = []string{workspace.WorkspaceLockFileName}
	}
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
}

func initializeWorkspace(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (result InitializationResult, resultErr error) {
	workspaceDir, err := resolvedWorkspaceDirectory(bundle, options.WorkspaceDir)
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
			var initializeErr error
			initialized, initializeErr = workspace.InitializeWorkspaceV2WithOptions(
				ctx,
				workspaceDir,
				definition,
				occurredAt,
				workspace.WorkspaceInitializationOptions{
					PlanCheckpoint: &checkpoint,
				},
			)
			return initializeErr
		},
	)
	if err != nil {
		return InitializationResult{}, err
	}
	if err := bundle.VerifyRoot(); err != nil {
		return InitializationResult{}, err
	}
	report, err := workspace.RebuildWorkspaceView(initialized.Snapshot(), definition)
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

func readWorkspaceView(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	workspaceDir string,
) (workspace.WorkspaceView, error) {
	directory, err := resolvedWorkspaceDirectory(bundle, workspaceDir)
	if err != nil {
		return workspace.WorkspaceView{}, err
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(directory)
	if err != nil {
		return workspace.WorkspaceView{}, err
	}
	view, err := workspace.RebuildWorkspaceView(snapshot, bundle.Definition())
	if err != nil {
		return workspace.WorkspaceView{}, err
	}
	if err := workspace.ApplyWorkspaceIntegrationDrift(
		ctx, &view, workspace.DefaultLocalIntegrationGitAdapter(),
	); err != nil {
		return workspace.WorkspaceView{}, err
	}
	return view, nil
}

type recoverRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
}

func recoverWorkspace(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (MutationResult, error) {
	directory, err := resolvedWorkspaceDirectory(bundle, options.WorkspaceDir)
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
	repository := localReviewRepository{
		git: workspace.DefaultLocalCommitGitAdapter(),
	}
	if _, err := workspace.RecoverWorkspaceLocalEffects(
		ctx,
		journal,
		bundle.Definition(),
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.DefaultLocalAttemptGitAdapter(),
		repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.RecoverWorkspaceLocalEffectsRequest{
			OccurredAt: occurredAt,
		},
	); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("recover", journal, bundle.Definition())
}

func mutationResult(
	action string,
	journal *workspace.WorkspaceJournal,
	definition workspace.EffectiveWorkspaceDefinition,
) (MutationResult, error) {
	if _, err := workspace.RebuildWorkspaceRuntimeProjectionFile(journal); err != nil {
		return MutationResult{}, err
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return MutationResult{}, err
	}
	report, err := workspace.RebuildWorkspaceView(snapshot, definition)
	if err != nil {
		return MutationResult{}, err
	}
	if err := workspace.ApplyWorkspaceIntegrationDrift(
		context.Background(), &report,
		workspace.DefaultLocalIntegrationGitAdapter(),
	); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{
		SchemaVersion: requestSchemaVersion, Status: "recorded", Action: action,
		Report: report,
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

func resolvedWorkspaceDirectory(
	bundle workspace.WorkspaceBundle,
	configured string,
) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return absoluteDirectory(configured, "workspace")
	}
	return workspace.DerivedWorkspaceRuntimeDirectory(bundle.Root())
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

func openWritableJournal(options Options) (*workspace.WorkspaceJournal, string, error) {
	directory, err := absoluteDirectory(options.WorkspaceDir, "workspace")
	if err != nil {
		return nil, "", err
	}
	journal, err := workspace.OpenWorkspaceJournal(directory, workspace.JournalReadWrite)
	return journal, directory, err
}

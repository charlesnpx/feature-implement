package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	PlanCheckpointArtifactFileName = "plan-checkpoint.v5.json"
	maxPlanGitOutputBytes          = 8 * 1024 * 1024
)

type VerifiedPlanLockCheckpoint struct {
	root           string
	head           GitObjectID
	sourceDigest   Digest
	semanticDigest Digest
	generation     Digest
	lockDigest     Digest
	checkpointID   Digest
	artifactDigest Digest
	artifactBytes  []byte
}

func (checkpoint VerifiedPlanLockCheckpoint) Root() string         { return checkpoint.root }
func (checkpoint VerifiedPlanLockCheckpoint) Head() GitObjectID    { return checkpoint.head }
func (checkpoint VerifiedPlanLockCheckpoint) Commit() GitObjectID  { return checkpoint.head }
func (checkpoint VerifiedPlanLockCheckpoint) SourceDigest() Digest { return checkpoint.sourceDigest }
func (checkpoint VerifiedPlanLockCheckpoint) SemanticDigest() Digest {
	return checkpoint.semanticDigest
}
func (checkpoint VerifiedPlanLockCheckpoint) Generation() Digest   { return checkpoint.generation }
func (checkpoint VerifiedPlanLockCheckpoint) LockDigest() Digest   { return checkpoint.lockDigest }
func (checkpoint VerifiedPlanLockCheckpoint) CheckpointID() Digest { return checkpoint.checkpointID }
func (checkpoint VerifiedPlanLockCheckpoint) ArtifactDigest() Digest {
	return checkpoint.artifactDigest
}
func (checkpoint VerifiedPlanLockCheckpoint) ArtifactBytes() []byte {
	return append([]byte(nil), checkpoint.artifactBytes...)
}

type planCheckpointArtifactWire struct {
	SchemaVersion  int    `json:"schema_version"`
	Kind           string `json:"kind"`
	PlanHead       string `json:"plan_head"`
	SourceDigest   string `json:"source_digest"`
	SemanticDigest string `json:"semantic_digest"`
	Generation     string `json:"generation"`
	LockDigest     string `json:"lock_digest"`
	CheckpointID   string `json:"checkpoint_id"`
}

func VerifyPlanLockCheckpoint(
	ctx context.Context,
	bundle WorkspaceBundle,
) (VerifiedPlanLockCheckpoint, error) {
	if ctx == nil {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("plan lock checkpoint verification requires context")
	}
	if bundle.root == "" || bundle.definition.generation.IsZero() {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("validated workspace bundle is required")
	}
	if err := bundle.VerifyRoot(); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	head, err := committedPlanHead(ctx, bundle.root)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if err := requireCleanPlanRepository(ctx, bundle.root); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	sourceFiles := bundle.sourceFiles
	if len(sourceFiles) == 0 {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("workspace bundle has no source files")
	}
	for _, relative := range sortedByteMapKeys(sourceFiles) {
		committed, err := gitShowFile(ctx, bundle.root, head, relative)
		if err != nil {
			return VerifiedPlanLockCheckpoint{}, err
		}
		if !bytes.Equal(committed, sourceFiles[relative]) {
			return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
				"committed plan source %s does not match current bundle bytes",
				relative,
			)
		}
	}
	lockBytes, err := WorkspaceBundleLockBytes(bundle)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	currentLock, err := ReadWorkspaceBundleLock(bundle)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if !bytes.Equal(currentLock, lockBytes) {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
			"current workspace lock does not match the normalized definition",
		)
	}
	committedLock, err := gitShowFile(ctx, bundle.root, head, WorkspaceLockFileName)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if !bytes.Equal(committedLock, lockBytes) {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
			"committed workspace lock does not match current lock bytes",
		)
	}
	sourceDigest := digestNamedBytes(sourceFiles)
	lockDigest := DigestBytes(lockBytes)
	semanticDigest := digestDefinitionSemantics(bundle.definition)
	checkpointID, err := deterministicPlanCheckpointID(
		head, sourceDigest, semanticDigest, bundle.definition.generation, lockDigest,
	)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	artifactBytes, err := json.Marshal(planCheckpointArtifactWire{
		SchemaVersion:  RuntimeFormatSchemaVersion,
		Kind:           "workspace_plan_checkpoint",
		PlanHead:       head.String(),
		SourceDigest:   sourceDigest.String(),
		SemanticDigest: semanticDigest.String(),
		Generation:     bundle.definition.generation.String(),
		LockDigest:     lockDigest.String(),
		CheckpointID:   checkpointID.String(),
	})
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	checkpoint := VerifiedPlanLockCheckpoint{
		root:           bundle.root,
		head:           head,
		sourceDigest:   sourceDigest,
		semanticDigest: semanticDigest,
		generation:     bundle.definition.generation,
		lockDigest:     lockDigest,
		checkpointID:   checkpointID,
		artifactDigest: DigestBytes(artifactBytes),
		artifactBytes:  artifactBytes,
	}
	if err := checkpoint.validate(); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if err := bundle.VerifyRoot(); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	return checkpoint, nil
}

func WithVerifiedPlanLockCheckpoint(
	ctx context.Context,
	bundle WorkspaceBundle,
	use func(VerifiedPlanLockCheckpoint) error,
) (VerifiedPlanLockCheckpoint, error) {
	if use == nil {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("plan lock checkpoint verification requires a binding callback")
	}
	checkpoint, err := VerifyPlanLockCheckpoint(ctx, bundle)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if err := use(checkpoint); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	return checkpoint, nil
}

func (checkpoint VerifiedPlanLockCheckpoint) validate() error {
	if checkpoint.root == "" || checkpoint.head.IsZero() ||
		checkpoint.sourceDigest.IsZero() || checkpoint.semanticDigest.IsZero() ||
		checkpoint.generation.IsZero() || checkpoint.lockDigest.IsZero() ||
		checkpoint.checkpointID.IsZero() || checkpoint.artifactDigest.IsZero() ||
		len(checkpoint.artifactBytes) == 0 {
		return fmt.Errorf("verified plan lock checkpoint is incomplete")
	}
	expected, err := deterministicPlanCheckpointID(
		checkpoint.head,
		checkpoint.sourceDigest,
		checkpoint.semanticDigest,
		checkpoint.generation,
		checkpoint.lockDigest,
	)
	if err != nil {
		return err
	}
	if expected != checkpoint.checkpointID {
		return fmt.Errorf("verified plan lock checkpoint id mismatch")
	}
	return nil
}

func deterministicPlanCheckpointID(
	head GitObjectID,
	sourceDigest, semanticDigest, generation, lockDigest Digest,
) (Digest, error) {
	if head.IsZero() || sourceDigest.IsZero() || semanticDigest.IsZero() ||
		generation.IsZero() || lockDigest.IsZero() {
		return Digest{}, fmt.Errorf("plan checkpoint id requires complete bindings")
	}
	type identity struct {
		SchemaVersion  int    `json:"schema_version"`
		PlanHead       string `json:"plan_head"`
		SourceDigest   string `json:"source_digest"`
		SemanticDigest string `json:"semantic_digest"`
		Generation     string `json:"generation"`
		LockDigest     string `json:"lock_digest"`
	}
	content, err := json.Marshal(identity{
		SchemaVersion:  RuntimeFormatSchemaVersion,
		PlanHead:       head.String(),
		SourceDigest:   sourceDigest.String(),
		SemanticDigest: semanticDigest.String(),
		Generation:     generation.String(),
		LockDigest:     lockDigest.String(),
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func digestDefinitionSemantics(definition EffectiveWorkspaceDefinition) Digest {
	type semanticArtifact struct {
		Kind         ArtifactKind `json:"kind"`
		ID           string       `json:"id"`
		Path         string       `json:"path"`
		SemanticHash string       `json:"semantic_hash"`
	}
	artifacts := definition.Artifacts()
	items := make([]semanticArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		items = append(items, semanticArtifact{
			Kind:         artifact.Kind(),
			ID:           artifact.ID().String(),
			Path:         artifact.Path(),
			SemanticHash: artifact.SemanticHash().String(),
		})
	}
	content, _ := json.Marshal(items)
	return DigestBytes(content)
}

func digestNamedBytes(files map[string][]byte) Digest {
	keys := sortedByteMapKeys(files)
	type item struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
	}
	values := make([]item, 0, len(keys))
	for _, key := range keys {
		values = append(values, item{Path: key, Hash: DigestBytes(files[key]).String()})
	}
	content, _ := json.Marshal(values)
	return DigestBytes(content)
}

func sortedByteMapKeys(files map[string][]byte) []string {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func committedPlanHead(ctx context.Context, root string) (GitObjectID, error) {
	algorithm, err := gitHashAlgorithm(ctx, root)
	if err != nil {
		return GitObjectID{}, err
	}
	output, err := runPlanGit(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return GitObjectID{}, fmt.Errorf("read committed plan HEAD: %w", err)
	}
	return qualifyGitObjectID(algorithm, strings.TrimSpace(string(output)))
}

func gitHashAlgorithm(ctx context.Context, root string) (GitHashAlgorithm, error) {
	output, err := runPlanGit(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return "", fmt.Errorf("read plan repository object format: %w", err)
	}
	switch strings.TrimSpace(string(output)) {
	case "sha1":
		return GitHashSHA1, nil
	case "sha256":
		return GitHashSHA256, nil
	default:
		return "", fmt.Errorf("unsupported plan repository object format %q", strings.TrimSpace(string(output)))
	}
}

func requireCleanPlanRepository(ctx context.Context, root string) error {
	topLevel, err := runPlanGit(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("inspect plan repository root: %w", err)
	}
	canonicalTopLevel, err := canonicalizeTrustedRootPath(strings.TrimSpace(string(topLevel)))
	if err != nil {
		return err
	}
	if canonicalTopLevel != root {
		return fmt.Errorf("workspace bundle root must be the plan repository root")
	}
	status, err := runPlanGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("inspect plan repository status: %w", err)
	}
	// The persistent flock inode is operational synchronization state, not a
	// plan source or generated lock artifact.
	for _, line := range bytes.Split(status, []byte("\n")) {
		if len(line) == 0 || bytes.Equal(
			line, []byte("?? "+workspaceLockPublicationLockName),
		) {
			continue
		}
		return fmt.Errorf("plan repository must be clean before workspace initialization")
	}
	return nil
}

func gitShowFile(ctx context.Context, root string, head GitObjectID, relative string) ([]byte, error) {
	if _, err := normalizeSourcePath(relative); err != nil {
		return nil, err
	}
	content, err := runPlanGit(ctx, root, "show", gitObjectHex(head)+":"+relative)
	if err != nil {
		return nil, fmt.Errorf("read committed plan file %s: %w", relative, err)
	}
	return content, nil
}

func runPlanGit(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("plan Git command requires context")
	}
	cmd := exec.CommandContext(
		ctx,
		"git",
		trustedGitArguments(root, arguments...)...,
	)
	environment, err := BuildIsolatedProcessEnvironment(os.Environ(), nil)
	if err != nil {
		return nil, err
	}
	cmd.Env = environment
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxPlanGitOutputBytes
	stderr.maximum = 128 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("plan Git output exceeded its bound")
	}
	if runErr != nil {
		detail := strings.TrimSpace(string(stderr.bytes()))
		if detail != "" {
			return nil, fmt.Errorf("%v: %s", runErr, detail)
		}
		return nil, runErr
	}
	return stdout.bytes(), nil
}

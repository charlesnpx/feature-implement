package workspace

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"strings"
	"time"
)

const (
	integrationAuthorName  = "Feature Implement"
	integrationAuthorEmail = "feature-implement@localhost"
)

type IntegrationAcceptanceMode string

const (
	IntegrationAcceptanceReviewReady IntegrationAcceptanceMode = "review-ready"
	IntegrationAcceptanceAdoptedHead IntegrationAcceptanceMode = "adopted-head"
)

func (mode IntegrationAcceptanceMode) valid() bool {
	return mode == IntegrationAcceptanceReviewReady ||
		mode == IntegrationAcceptanceAdoptedHead
}

type MergeUnitIntegrationIntentOptions struct {
	WorkspaceID            ID
	Generation             Digest
	AttemptID              ID
	MergeUnit              MergeUnitReference
	FeatureRef             string
	ExpectedFeatureHead    GitObjectID
	ExpectedFeatureMarker  string
	AttemptWorktreeBinding AttemptWorktreeGitBinding
	AcceptedHead           GitObjectID
	AcceptedTree           GitObjectID
	ReviewReadinessDigest  Digest
	AdoptedHeadEventDigest Digest
	OccurredAt             time.Time
}

// MergeUnitIntegrationIntent is the complete deterministic input to one
// local two-parent integration commit. It is journaled before either the
// commit object or feature-ref update is allowed.
type MergeUnitIntegrationIntent struct {
	workspaceID            ID
	generation             Digest
	attemptID              ID
	mergeUnit              MergeUnitReference
	featureRef             string
	expectedFeatureHead    GitObjectID
	expectedFeatureMarker  string
	attemptWorktreeBinding AttemptWorktreeGitBinding
	acceptedHead           GitObjectID
	acceptedTree           GitObjectID
	acceptanceMode         IntegrationAcceptanceMode
	reviewReadinessDigest  Digest
	adoptedHeadEventDigest Digest
	parents                [2]GitObjectID
	message                string
	authorName             string
	authorEmail            string
	authorAt               time.Time
	committerName          string
	committerEmail         string
	committerAt            time.Time
	expectedMerge          GitObjectID
	digest                 Digest
}

func NewMergeUnitIntegrationIntent(
	options MergeUnitIntegrationIntentOptions,
) (MergeUnitIntegrationIntent, error) {
	featureRef := strings.TrimSpace(options.FeatureRef)
	if options.WorkspaceID.IsZero() || options.Generation.IsZero() ||
		options.AttemptID.IsZero() || options.MergeUnit.planID.IsZero() ||
		options.MergeUnit.mergeUnitID.IsZero() ||
		options.ExpectedFeatureHead.IsZero() ||
		options.AttemptWorktreeBinding.IsZero() ||
		options.AcceptedHead.IsZero() || options.AcceptedTree.IsZero() ||
		options.OccurredAt.IsZero() {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration intent requires workspace, generation, attempt, merge unit, Git objects, and occurrence time",
		)
	}
	if _, err := normalizeFullyQualifiedBaseRef(featureRef); err != nil {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration feature ref: %w", err,
		)
	}
	if !strings.HasPrefix(featureRef, "refs/heads/feature/") {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration may update only a feature branch ref",
		)
	}
	expectedFeatureMarker := strings.TrimSpace(
		options.ExpectedFeatureMarker,
	)
	if expectedFeatureMarker == "" ||
		expectedFeatureMarker != options.ExpectedFeatureMarker ||
		strings.ContainsAny(expectedFeatureMarker, "\x00\r\n") {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration requires an exact prior feature-ref marker",
		)
	}
	algorithm := options.ExpectedFeatureHead.Algorithm()
	if options.AcceptedHead.Algorithm() != algorithm ||
		options.AcceptedTree.Algorithm() != algorithm {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration Git objects must use one object format",
		)
	}
	mode := IntegrationAcceptanceMode("")
	switch {
	case !options.ReviewReadinessDigest.IsZero() &&
		options.AdoptedHeadEventDigest.IsZero():
		mode = IntegrationAcceptanceReviewReady
	case options.ReviewReadinessDigest.IsZero() &&
		!options.AdoptedHeadEventDigest.IsZero():
		mode = IntegrationAcceptanceAdoptedHead
	default:
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration intent requires exactly one review-readiness or adopted-head event digest",
		)
	}
	occurredAt := options.OccurredAt.UTC().Truncate(time.Second)
	intent := MergeUnitIntegrationIntent{
		workspaceID: options.WorkspaceID, generation: options.Generation,
		attemptID: options.AttemptID, mergeUnit: options.MergeUnit,
		featureRef:             featureRef,
		expectedFeatureHead:    options.ExpectedFeatureHead,
		expectedFeatureMarker:  expectedFeatureMarker,
		attemptWorktreeBinding: options.AttemptWorktreeBinding,
		acceptedHead:           options.AcceptedHead, acceptedTree: options.AcceptedTree,
		acceptanceMode:         mode,
		reviewReadinessDigest:  options.ReviewReadinessDigest,
		adoptedHeadEventDigest: options.AdoptedHeadEventDigest,
		parents: [2]GitObjectID{
			options.ExpectedFeatureHead,
			options.AcceptedHead,
		},
		authorName: integrationAuthorName, authorEmail: integrationAuthorEmail,
		authorAt:      occurredAt,
		committerName: integrationAuthorName, committerEmail: integrationAuthorEmail,
		committerAt: occurredAt,
	}
	intent.message = canonicalIntegrationMessage(intent)
	expected, err := gitCommitObjectID(
		algorithm, intent.commitContent(),
	)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	intent.expectedMerge = expected
	digest, err := digestMergeUnitIntegrationIntent(intent)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	intent.digest = digest
	if err := intent.validate(); err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	return intent, nil
}

func (intent MergeUnitIntegrationIntent) WorkspaceID() ID {
	return intent.workspaceID
}
func (intent MergeUnitIntegrationIntent) Generation() Digest {
	return intent.generation
}
func (intent MergeUnitIntegrationIntent) AttemptID() ID {
	return intent.attemptID
}
func (intent MergeUnitIntegrationIntent) MergeUnit() MergeUnitReference {
	return intent.mergeUnit
}
func (intent MergeUnitIntegrationIntent) FeatureRef() string {
	return intent.featureRef
}
func (intent MergeUnitIntegrationIntent) ExpectedFeatureHead() GitObjectID {
	return intent.expectedFeatureHead
}
func (intent MergeUnitIntegrationIntent) ExpectedFeatureMarker() string {
	return intent.expectedFeatureMarker
}
func (intent MergeUnitIntegrationIntent) AttemptWorktreeBinding() AttemptWorktreeGitBinding {
	return intent.attemptWorktreeBinding
}
func (intent MergeUnitIntegrationIntent) AcceptedHead() GitObjectID {
	return intent.acceptedHead
}
func (intent MergeUnitIntegrationIntent) AcceptedTree() GitObjectID {
	return intent.acceptedTree
}
func (intent MergeUnitIntegrationIntent) AcceptanceMode() IntegrationAcceptanceMode {
	return intent.acceptanceMode
}
func (intent MergeUnitIntegrationIntent) ReviewReadinessDigest() Digest {
	return intent.reviewReadinessDigest
}
func (intent MergeUnitIntegrationIntent) AdoptedHeadEventDigest() Digest {
	return intent.adoptedHeadEventDigest
}
func (intent MergeUnitIntegrationIntent) Parents() []GitObjectID {
	return []GitObjectID{intent.parents[0], intent.parents[1]}
}
func (intent MergeUnitIntegrationIntent) Message() string {
	return intent.message
}
func (intent MergeUnitIntegrationIntent) AuthorAt() time.Time {
	return intent.authorAt
}
func (intent MergeUnitIntegrationIntent) CommitterAt() time.Time {
	return intent.committerAt
}
func (intent MergeUnitIntegrationIntent) ExpectedMerge() GitObjectID {
	return intent.expectedMerge
}
func (intent MergeUnitIntegrationIntent) Digest() Digest {
	return intent.digest
}
func (intent MergeUnitIntegrationIntent) CommitContent() []byte {
	return intent.commitContent()
}

func (intent MergeUnitIntegrationIntent) acceptanceValue() string {
	switch intent.acceptanceMode {
	case IntegrationAcceptanceReviewReady:
		return string(intent.acceptanceMode) + ":" +
			intent.reviewReadinessDigest.String()
	case IntegrationAcceptanceAdoptedHead:
		return string(intent.acceptanceMode) + ":" +
			intent.adoptedHeadEventDigest.String()
	default:
		return ""
	}
}

func (intent MergeUnitIntegrationIntent) commitContent() []byte {
	lines := []string{
		"tree " + gitObjectHex(intent.acceptedTree),
		"parent " + gitObjectHex(intent.parents[0]),
		"parent " + gitObjectHex(intent.parents[1]),
		fmt.Sprintf(
			"author %s <%s> %d +0000",
			intent.authorName, intent.authorEmail, intent.authorAt.Unix(),
		),
		fmt.Sprintf(
			"committer %s <%s> %d +0000",
			intent.committerName, intent.committerEmail,
			intent.committerAt.Unix(),
		),
	}
	return []byte(strings.Join(lines, "\n") + "\n\n" + intent.message)
}

func (intent MergeUnitIntegrationIntent) validate() error {
	if intent.workspaceID.IsZero() || intent.generation.IsZero() ||
		intent.attemptID.IsZero() || intent.mergeUnit.planID.IsZero() ||
		intent.mergeUnit.mergeUnitID.IsZero() ||
		intent.expectedFeatureHead.IsZero() ||
		intent.expectedFeatureMarker == "" ||
		intent.attemptWorktreeBinding.IsZero() ||
		intent.acceptedHead.IsZero() || intent.acceptedTree.IsZero() ||
		intent.expectedMerge.IsZero() || intent.digest.IsZero() ||
		!intent.acceptanceMode.valid() {
		return fmt.Errorf("integration intent has incomplete immutable bindings")
	}
	if intent.featureRef == "" ||
		!strings.HasPrefix(intent.featureRef, "refs/heads/feature/") {
		return fmt.Errorf("integration intent has an invalid feature ref")
	}
	if strings.TrimSpace(intent.expectedFeatureMarker) !=
		intent.expectedFeatureMarker ||
		strings.ContainsAny(intent.expectedFeatureMarker, "\x00\r\n") {
		return fmt.Errorf(
			"integration intent has an invalid prior feature-ref marker",
		)
	}
	if err := intent.attemptWorktreeBinding.validate(); err != nil {
		return err
	}
	if intent.parents[0] != intent.expectedFeatureHead ||
		intent.parents[1] != intent.acceptedHead {
		return fmt.Errorf(
			"integration intent parent order does not match feature and accepted heads",
		)
	}
	algorithm := intent.expectedFeatureHead.Algorithm()
	for _, object := range []GitObjectID{
		intent.acceptedHead, intent.acceptedTree, intent.parents[0],
		intent.parents[1], intent.expectedMerge,
	} {
		if object.Algorithm() != algorithm {
			return fmt.Errorf(
				"integration intent mixes Git object formats",
			)
		}
	}
	if intent.authorName != integrationAuthorName ||
		intent.authorEmail != integrationAuthorEmail ||
		intent.committerName != integrationAuthorName ||
		intent.committerEmail != integrationAuthorEmail ||
		intent.authorAt.IsZero() || intent.committerAt.IsZero() ||
		intent.authorAt.Location() != time.UTC ||
		intent.committerAt.Location() != time.UTC ||
		intent.authorAt.Nanosecond() != 0 ||
		intent.committerAt.Nanosecond() != 0 {
		return fmt.Errorf(
			"integration intent does not use the fixed identity and UTC timestamps",
		)
	}
	switch intent.acceptanceMode {
	case IntegrationAcceptanceReviewReady:
		if intent.reviewReadinessDigest.IsZero() ||
			!intent.adoptedHeadEventDigest.IsZero() {
			return fmt.Errorf(
				"review-ready integration requires only review readiness",
			)
		}
	case IntegrationAcceptanceAdoptedHead:
		if !intent.reviewReadinessDigest.IsZero() ||
			intent.adoptedHeadEventDigest.IsZero() {
			return fmt.Errorf(
				"adopted-head integration requires only its event digest",
			)
		}
	}
	if intent.message != canonicalIntegrationMessage(intent) {
		return fmt.Errorf("integration intent message is not canonical")
	}
	expectedMerge, err := gitCommitObjectID(
		algorithm, intent.commitContent(),
	)
	if err != nil {
		return err
	}
	if expectedMerge != intent.expectedMerge {
		return fmt.Errorf(
			"integration intent expected merge object does not match its content",
		)
	}
	digest, err := digestMergeUnitIntegrationIntent(intent)
	if err != nil {
		return err
	}
	if digest != intent.digest {
		return fmt.Errorf("integration intent digest mismatch")
	}
	return nil
}

func canonicalIntegrationMessage(
	intent MergeUnitIntegrationIntent,
) string {
	return strings.Join([]string{
		"feature workspace integration: " + intent.mergeUnit.String(),
		"",
		"Plan: " + intent.mergeUnit.planID.String(),
		"Merge-Unit: " + intent.mergeUnit.mergeUnitID.String(),
		"Attempt: " + intent.attemptID.String(),
		"Generation: " + intent.generation.String(),
		"Accepted-Head: " + intent.acceptedHead.String(),
		"Acceptance: " + intent.acceptanceValue(),
	}, "\n") + "\n"
}

type integrationIntentDigestWire struct {
	SchemaVersion          int                           `json:"schema_version"`
	WorkspaceID            string                        `json:"workspace_id"`
	Generation             string                        `json:"generation"`
	AttemptID              string                        `json:"attempt_id"`
	PlanID                 string                        `json:"plan_id"`
	MergeUnitID            string                        `json:"merge_unit_id"`
	FeatureRef             string                        `json:"feature_ref"`
	ExpectedFeatureHead    string                        `json:"expected_feature_head"`
	ExpectedFeatureMarker  string                        `json:"expected_feature_marker"`
	AttemptWorktreeBinding attemptWorktreeGitBindingWire `json:"attempt_worktree_binding"`
	AcceptedHead           string                        `json:"accepted_head"`
	AcceptedTree           string                        `json:"accepted_tree"`
	AcceptanceMode         IntegrationAcceptanceMode     `json:"acceptance_mode"`
	ReviewReadinessDigest  string                        `json:"review_readiness_digest,omitempty"`
	AdoptedHeadEventDigest string                        `json:"adopted_head_event_digest,omitempty"`
	Parents                []string                      `json:"parents"`
	Message                string                        `json:"message"`
	AuthorName             string                        `json:"author_name"`
	AuthorEmail            string                        `json:"author_email"`
	AuthorAt               string                        `json:"author_at"`
	CommitterName          string                        `json:"committer_name"`
	CommitterEmail         string                        `json:"committer_email"`
	CommitterAt            string                        `json:"committer_at"`
	ExpectedMerge          string                        `json:"expected_merge"`
}

func integrationIntentDigestValue(
	intent MergeUnitIntegrationIntent,
) integrationIntentDigestWire {
	return integrationIntentDigestWire{
		SchemaVersion:         JournalSchemaVersion,
		WorkspaceID:           intent.workspaceID.String(),
		Generation:            intent.generation.String(),
		AttemptID:             intent.attemptID.String(),
		PlanID:                intent.mergeUnit.planID.String(),
		MergeUnitID:           intent.mergeUnit.mergeUnitID.String(),
		FeatureRef:            intent.featureRef,
		ExpectedFeatureHead:   intent.expectedFeatureHead.String(),
		ExpectedFeatureMarker: intent.expectedFeatureMarker,
		AttemptWorktreeBinding: attemptWorktreeGitBindingToWire(
			intent.attemptWorktreeBinding,
		),
		AcceptedHead:           intent.acceptedHead.String(),
		AcceptedTree:           intent.acceptedTree.String(),
		AcceptanceMode:         intent.acceptanceMode,
		ReviewReadinessDigest:  intent.reviewReadinessDigest.String(),
		AdoptedHeadEventDigest: intent.adoptedHeadEventDigest.String(),
		Parents: []string{
			intent.parents[0].String(), intent.parents[1].String(),
		},
		Message:        intent.message,
		AuthorName:     intent.authorName,
		AuthorEmail:    intent.authorEmail,
		AuthorAt:       intent.authorAt.UTC().Format(time.RFC3339Nano),
		CommitterName:  intent.committerName,
		CommitterEmail: intent.committerEmail,
		CommitterAt:    intent.committerAt.UTC().Format(time.RFC3339Nano),
		ExpectedMerge:  intent.expectedMerge.String(),
	}
}

func digestMergeUnitIntegrationIntent(
	intent MergeUnitIntegrationIntent,
) (Digest, error) {
	content, err := json.Marshal(integrationIntentDigestValue(intent))
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func gitCommitObjectID(
	algorithm GitHashAlgorithm,
	content []byte,
) (GitObjectID, error) {
	var digest hash.Hash
	switch algorithm {
	case GitHashSHA1:
		digest = sha1.New() // Git object-format compatibility, not security.
	case GitHashSHA256:
		digest = sha256.New()
	default:
		return GitObjectID{}, fmt.Errorf(
			"unsupported Git commit algorithm %q", algorithm,
		)
	}
	_, _ = fmt.Fprintf(digest, "commit %d%c", len(content), byte(0))
	_, _ = digest.Write(content)
	return gitObjectIDFromHash(algorithm, digest)
}

type IntegrationRefState string

const (
	IntegrationRefExpectedHead    IntegrationRefState = "expected_head"
	IntegrationRefExpectedMerge   IntegrationRefState = "expected_merge"
	IntegrationRefAncestorDrift   IntegrationRefState = "ancestor_drift"
	IntegrationRefDescendantDrift IntegrationRefState = "descendant_drift"
	IntegrationRefUnrelatedDrift  IntegrationRefState = "unrelated_drift"
)

func (state IntegrationRefState) valid() bool {
	switch state {
	case IntegrationRefExpectedHead, IntegrationRefExpectedMerge,
		IntegrationRefAncestorDrift, IntegrationRefDescendantDrift,
		IntegrationRefUnrelatedDrift:
		return true
	default:
		return false
	}
}

type IntegrationGitInspection struct {
	featureHead    GitObjectID
	refState       IntegrationRefState
	expectedCommit bool
}

func NewIntegrationGitInspection(
	featureHead GitObjectID,
	refState IntegrationRefState,
	expectedCommit bool,
) (IntegrationGitInspection, error) {
	if featureHead.IsZero() || !refState.valid() {
		return IntegrationGitInspection{}, fmt.Errorf(
			"integration Git inspection requires a feature head and ref state",
		)
	}
	return IntegrationGitInspection{
		featureHead:    featureHead,
		refState:       refState,
		expectedCommit: expectedCommit,
	}, nil
}

func (inspection IntegrationGitInspection) FeatureHead() GitObjectID {
	return inspection.featureHead
}
func (inspection IntegrationGitInspection) RefState() IntegrationRefState {
	return inspection.refState
}
func (inspection IntegrationGitInspection) ExpectedCommitExists() bool {
	return inspection.expectedCommit
}

type IntegrationGitPort interface {
	InspectAttempt(
		context.Context,
		LocalTargetBinding,
		string,
		string,
		GitObjectID,
		GitObjectID,
	) (AttemptGitInspection, error)
	InspectIntegration(
		context.Context,
		LocalTargetBinding,
		string,
		MergeUnitIntegrationIntent,
	) (IntegrationGitInspection, error)
	CreateIntegrationCommit(
		context.Context,
		LocalTargetBinding,
		string,
		MergeUnitIntegrationIntent,
	) error
	PublishIntegration(
		context.Context,
		LocalTargetBinding,
		string,
		MergeUnitIntegrationIntent,
		IntegrationLifecycleFaultInjector,
	) error
	VerifyCompletedIntegration(
		context.Context,
		LocalTargetBinding,
		MergeUnitIntegrationIntent,
	) error
}

type RuntimeIntegrationProjection struct {
	intent           MergeUnitIntegrationIntent
	intentRecord     uint64
	integratedRecord uint64
}

func (projection RuntimeIntegrationProjection) Intent() MergeUnitIntegrationIntent {
	return projection.intent
}
func (projection RuntimeIntegrationProjection) IntentRecord() uint64 {
	return projection.intentRecord
}
func (projection RuntimeIntegrationProjection) Integrated() bool {
	return projection.integratedRecord != 0
}
func (projection RuntimeIntegrationProjection) IntegratedRecord() uint64 {
	return projection.integratedRecord
}
func (projection RuntimeIntegrationProjection) MergeCommit() GitObjectID {
	return projection.intent.expectedMerge
}

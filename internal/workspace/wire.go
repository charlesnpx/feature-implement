package workspace

type workspaceWire struct {
	SchemaVersion    int                        `yaml:"schema_version"`
	ID               string                     `yaml:"id"`
	Repository       repositoryWire             `yaml:"repository"`
	Provider         providerWire               `yaml:"provider"`
	BaseRef          string                     `yaml:"base_ref"`
	Remote           string                     `yaml:"remote"`
	ExecutionConfig  string                     `yaml:"execution_config"`
	Plans            []workspacePlanWire        `yaml:"plans"`
	Dependencies     *[]workspaceDependencyWire `yaml:"dependencies"`
	AuthoritySources *[]authoritySourceWire     `yaml:"authority_sources"`
}

type repositoryWire struct {
	Root     string `yaml:"root"`
	Identity string `yaml:"identity"`
}

type providerWire struct {
	Kind       string `yaml:"kind"`
	Repository string `yaml:"repository"`
}

type workspacePlanWire struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`
}

type workspaceDependencyWire struct {
	Before mergeUnitReferenceWire `yaml:"before"`
	After  mergeUnitReferenceWire `yaml:"after"`
}

type mergeUnitReferenceWire struct {
	PlanID      string `yaml:"plan_id"`
	MergeUnitID string `yaml:"merge_unit_id"`
}

type authoritySourceWire struct {
	ID       string `yaml:"id"`
	Kind     string `yaml:"kind"`
	Location string `yaml:"location"`
}

type planWire struct {
	SchemaVersion int             `yaml:"schema_version"`
	ID            string          `yaml:"id"`
	Title         string          `yaml:"title"`
	Stories       []storyWire     `yaml:"stories"`
	MergeUnits    []mergeUnitWire `yaml:"merge_units"`
}

type storyWire struct {
	ID             string    `yaml:"id"`
	Summary        string    `yaml:"summary"`
	Acceptance     []string  `yaml:"acceptance"`
	Implementation []string  `yaml:"implementation"`
	Testing        []string  `yaml:"testing"`
	Dependencies   *[]string `yaml:"dependencies"`
}

type mergeUnitWire struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	StoryIDs []string `yaml:"story_ids"`
}

type executionConfigWire struct {
	SchemaVersion  int                    `yaml:"schema_version"`
	Policy         executionPolicyWire    `yaml:"policy"`
	Profiles       []executionProfileWire `yaml:"profiles"`
	ReviewProfiles []reviewProfileWire    `yaml:"review_profiles"`
	MergeUnits     []unitExecutionWire    `yaml:"merge_units"`
}

type executionPolicyWire struct {
	RequirePassingChecks  *bool   `yaml:"require_passing_checks"`
	RequireSignedReceipts *bool   `yaml:"require_signed_receipts"`
	AllowWriteNetwork     *bool   `yaml:"allow_write_network"`
	MaxAttempts           *uint16 `yaml:"max_attempts"`
	MaxReviewRounds       *uint16 `yaml:"max_review_rounds"`
	MaxReviewFixes        *uint16 `yaml:"max_review_fixes"`
}

type executionProfileWire struct {
	ID     string              `yaml:"id"`
	Runner string              `yaml:"runner"`
	Policy executionPolicyWire `yaml:"policy"`
}

type reviewProfileWire struct {
	ID             string `yaml:"id"`
	Runner         string `yaml:"runner"`
	ReviewerPolicy string `yaml:"reviewer_policy"`
}

type unitExecutionWire struct {
	PlanID            string                     `yaml:"plan_id"`
	MergeUnitID       string                     `yaml:"merge_unit_id"`
	Profile           string                     `yaml:"profile"`
	Policy            executionPolicyWire        `yaml:"policy"`
	Boundary          *attemptBoundaryPolicyWire `yaml:"boundary"`
	CommitProtocol    *commitProtocolWire        `yaml:"commit_protocol"`
	ReviewFixProtocol *reviewFixProtocolWire     `yaml:"review_fix_protocol"`
	ReviewLoop        *reviewLoopWire            `yaml:"review_loop"`
}

type reviewLoopWire struct {
	Profiles                 []string `yaml:"profiles"`
	MaxInfrastructureRetries *uint16  `yaml:"max_infrastructure_retries"`
}

type attemptBoundaryPolicyWire struct {
	Mode          string  `yaml:"mode"`
	SerialSegment *string `yaml:"serial_segment"`
}

type commitProtocolWire struct {
	Steps []commitStepWire `yaml:"steps"`
}

type commitStepWire struct {
	ID           string             `yaml:"id"`
	Subject      string             `yaml:"subject"`
	BodyPolicy   string             `yaml:"body_policy"`
	ExactBody    *string            `yaml:"exact_body"`
	AllowedPaths *[]string          `yaml:"allowed_paths"`
	FrozenPaths  *[]string          `yaml:"frozen_paths"`
	Checks       *[]commitCheckWire `yaml:"checks"`
}

type reviewFixProtocolWire struct {
	SubjectPrefix string             `yaml:"subject_prefix"`
	BodyPolicy    string             `yaml:"body_policy"`
	AllowedPaths  *[]string          `yaml:"allowed_paths"`
	FrozenPaths   *[]string          `yaml:"frozen_paths"`
	Checks        *[]commitCheckWire `yaml:"checks"`
}

type commitCheckWire struct {
	ID          string               `yaml:"id"`
	Runner      string               `yaml:"runner"`
	Parser      string               `yaml:"parser"`
	Command     []string             `yaml:"command"`
	Expectation checkExpectationWire `yaml:"expectation"`
}

type checkExpectationWire struct {
	Kind       string    `yaml:"kind"`
	FailureIDs *[]string `yaml:"failure_ids"`
}

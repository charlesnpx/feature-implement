package workspace

type workspaceWire struct {
	SchemaVersion   int                        `yaml:"schema_version"`
	ID              string                     `yaml:"id"`
	Mode            string                     `yaml:"mode"`
	Repository      repositoryWire             `yaml:"repository"`
	BaseRef         string                     `yaml:"base_ref"`
	BaseCommit      string                     `yaml:"base_commit"`
	FeatureBranch   string                     `yaml:"feature_branch"`
	ExecutionConfig string                     `yaml:"execution_config"`
	Plans           []workspacePlanWire        `yaml:"plans"`
	Dependencies    *[]workspaceDependencyWire `yaml:"dependencies"`
}

type repositoryWire struct {
	Root string `yaml:"root"`
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
	RequirePassingChecks *bool   `yaml:"require_passing_checks"`
	AllowWriteNetwork    *bool   `yaml:"allow_write_network"`
	MaxAttempts          *uint16 `yaml:"max_attempts"`
	MaxReviewRounds      *uint16 `yaml:"max_review_rounds"`
}

type executionProfileWire struct {
	ID       string                     `yaml:"id"`
	Runner   string                     `yaml:"runner"`
	Policy   executionPolicyWire        `yaml:"policy"`
	Boundary *profileBoundaryPolicyWire `yaml:"boundary"`
}

type reviewProfileWire struct {
	ID             string `yaml:"id"`
	Runner         string `yaml:"runner"`
	ReviewerPolicy string `yaml:"reviewer_policy"`
}

type unitExecutionWire struct {
	PlanID         string                     `yaml:"plan_id"`
	MergeUnitID    string                     `yaml:"merge_unit_id"`
	Profile        string                     `yaml:"profile"`
	Policy         executionPolicyWire        `yaml:"policy"`
	Boundary       *attemptBoundaryPolicyWire `yaml:"boundary"`
	CommitProtocol *commitProtocolWire        `yaml:"commit_protocol"`
	ReviewLoop     *reviewLoopWire            `yaml:"review_loop"`
}

type reviewLoopWire struct {
	Profiles                 []string `yaml:"profiles"`
	MaxInfrastructureRetries *uint16  `yaml:"max_infrastructure_retries"`
}

type attemptBoundaryPolicyWire struct {
	Checkpoint    string  `yaml:"checkpoint"`
	Escalation    string  `yaml:"escalation"`
	SerialSegment *string `yaml:"serial_segment"`
}

type profileBoundaryPolicyWire struct {
	Escalation string `yaml:"escalation"`
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

type commitCheckWire struct {
	ID      string   `yaml:"id"`
	Runner  string   `yaml:"runner"`
	Command []string `yaml:"command"`
}

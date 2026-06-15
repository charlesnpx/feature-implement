package plan

type Manifest struct {
	SchemaVersion int         `yaml:"schema_version" json:"schema_version"`
	ID            string      `yaml:"id" json:"id"`
	Title         string      `yaml:"title" json:"title"`
	OutputName    string      `yaml:"output_name,omitempty" json:"output_name,omitempty"`
	BaseRef       string      `yaml:"base_ref,omitempty" json:"base_ref,omitempty"`
	Remote        string      `yaml:"remote,omitempty" json:"remote,omitempty"`
	MergePolicy   MergePolicy `yaml:"merge_policy,omitempty" json:"merge_policy,omitempty"`
	Epics         []Epic      `yaml:"epics" json:"epics"`
	MergeUnits    []MergeUnit `yaml:"merge_units,omitempty" json:"merge_units,omitempty"`
}

type MergePolicy struct {
	AutoMergeAllowed     bool `yaml:"auto_merge_allowed,omitempty" json:"auto_merge_allowed,omitempty"`
	DeleteBranchAllowed  bool `yaml:"delete_branch_allowed,omitempty" json:"delete_branch_allowed,omitempty"`
	RequirePassingChecks bool `yaml:"require_passing_checks,omitempty" json:"require_passing_checks,omitempty"`
}

type Epic struct {
	ID          string    `yaml:"id" json:"id"`
	Number      int       `yaml:"number" json:"number"`
	Name        string    `yaml:"name" json:"name"`
	Summary     string    `yaml:"summary,omitempty" json:"summary,omitempty"`
	Constraints []string  `yaml:"constraints,omitempty" json:"constraints,omitempty"`
	Features    []Feature `yaml:"features" json:"features"`
}

type Feature struct {
	ID          string   `yaml:"id" json:"id"`
	Number      int      `yaml:"number" json:"number"`
	Name        string   `yaml:"name" json:"name"`
	Summary     string   `yaml:"summary,omitempty" json:"summary,omitempty"`
	Constraints []string `yaml:"constraints,omitempty" json:"constraints,omitempty"`
	Stories     []Story  `yaml:"stories" json:"stories"`
}

type Story struct {
	ID             string   `yaml:"id" json:"id"`
	Number         int      `yaml:"number" json:"number"`
	Name           string   `yaml:"name" json:"name"`
	Summary        string   `yaml:"summary,omitempty" json:"summary,omitempty"`
	Acceptance     []string `yaml:"acceptance,omitempty" json:"acceptance,omitempty"`
	Implementation []string `yaml:"implementation,omitempty" json:"implementation,omitempty"`
	Testing        []string `yaml:"testing,omitempty" json:"testing,omitempty"`
	Dependencies   []string `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

type MergeUnit struct {
	ID                  string   `yaml:"id" json:"id"`
	Name                string   `yaml:"name" json:"name"`
	StoryIDs            []string `yaml:"story_ids" json:"story_ids"`
	AllowFeatureLevelPR bool     `yaml:"allow_feature_level_pr,omitempty" json:"allow_feature_level_pr,omitempty"`
}

type Lock struct {
	SchemaVersion int          `json:"schema_version"`
	ManifestID    string       `json:"manifest_id"`
	Title         string       `json:"title"`
	BaseRef       string       `json:"base_ref,omitempty"`
	Remote        string       `json:"remote,omitempty"`
	MergePolicy   MergePolicy  `json:"merge_policy,omitempty"`
	Epics         []Epic       `json:"epics"`
	MergeUnits    []MergeUnit  `json:"merge_units"`
	Files         []PlanFile   `json:"files"`
	State         RuntimeState `json:"state"`
}

type PlanFile struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Path string `json:"path"`
}

type RuntimeState struct {
	SchemaVersion   int              `json:"schema_version"`
	MergeUnits      []MergeUnitState `json:"merge_units"`
	legacyMergeUnit map[string]MergeUnitState
}

type MergeUnitState struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Branch        string `json:"branch,omitempty"`
	Worktree      string `json:"worktree,omitempty"`
	BaseSHA       string `json:"base_sha,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	PRNumber      int    `json:"pr_number,omitempty"`
	PRURL         string `json:"pr_url,omitempty"`
	ReviewStatus  string `json:"review_status,omitempty"`
	MergeStatus   string `json:"merge_status,omitempty"`
	MergeCommit   string `json:"merge_commit,omitempty"`
	CleanupStatus string `json:"cleanup_status,omitempty"`
}

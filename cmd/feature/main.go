package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charlesnpx/feature-implement/internal/install"
	"github.com/charlesnpx/feature-implement/internal/plan"
	"github.com/charlesnpx/feature-implement/internal/workspace"
)

var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stdout)
		return
	}
	var err error
	switch os.Args[1] {
	case "install-skills":
		err = installSkills(os.Args[2:])
	case "plan":
		err = planCommand(os.Args[2:])
	case "validate":
		err = validateCommand(os.Args[2:])
	case "status":
		err = statusCommand(os.Args[2:])
	case "implement":
		err = implementCommand(os.Args[2:])
	case "workspace":
		err = workspaceCommand(os.Args[2:])
	case "version":
		fmt.Println(Version)
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		err = fmt.Errorf("unknown command: %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature install-skills [--plan|--install|--uninstall] [--target tools|claude|codex|all] [--json] [--install-root <dir>]
  feature plan example
  feature plan schema [--json]
  feature plan materialize --manifest <file> [--out-root <dir>] [--json]
  feature validate <plan-dir> [--write-lock] [--json]
  feature status <plan-dir> [--json]
  feature implement next|start|commit|push|open-pr|review|merge|cleanup <plan-dir> [--merge-unit <id>] [--write-state] [metadata flags] [--json]
  feature workspace init|validate|status|next|heartbeat|release|recover|refresh-branch|publish-refresh|evaluate-gates|gate|queue|attempt|transition|contract|approve|external [args]
  feature version`)
}

func installSkills(args []string) error {
	if hasHelpFlag(args) {
		usageInstallSkills(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("install-skills", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	target := fs.String("target", "all", "tools | claude | codex | all")
	planFlag := fs.Bool("plan", false, "Print intended files without writing")
	doInstall := fs.Bool("install", false, "Install files")
	uninstall := fs.Bool("uninstall", false, "Remove files")
	asJSON := fs.Bool("json", false, "Emit mise-en-place delegated-installer JSON")
	installRoot := fs.String("install-root", "", "Stage install under this directory as if it were HOME")
	if err := parsePermissive(fs, args, "target", "install-root"); err != nil {
		return err
	}
	selected := 0
	for _, value := range []bool{*planFlag, *doInstall, *uninstall} {
		if value {
			selected++
		}
	}
	if selected > 1 {
		return fmt.Errorf("--plan, --install, and --uninstall are mutually exclusive")
	}
	op := "install"
	if *planFlag {
		op = "plan"
	}
	if *uninstall {
		op = "uninstall"
	}
	result, err := install.Run(install.Options{
		Operation:   op,
		Target:      *target,
		InstallRoot: *installRoot,
		Version:     Version,
	})
	if err != nil {
		return err
	}
	if *asJSON || op != "install" {
		return writeJSON(result)
	}
	fmt.Printf("installed feature %s\n", result.Version)
	return nil
}

func planCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("plan requires subcommand: example, schema, or materialize")
	}
	if isHelpCommand(args[0]) {
		usagePlan(os.Stdout)
		return nil
	}
	switch args[0] {
	case "example":
		return planExample(args[1:])
	case "schema":
		return planSchema(args[1:])
	case "materialize":
		return planMaterialize(args[1:])
	default:
		return fmt.Errorf("plan requires subcommand: example, schema, or materialize")
	}
}

func planExample(args []string) error {
	if hasHelpFlag(args) {
		usagePlanExample(os.Stdout)
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("plan example does not accept arguments")
	}
	fmt.Print(plan.ExampleManifestYAML())
	return nil
}

func planSchema(args []string) error {
	if hasHelpFlag(args) {
		usagePlanSchema(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("plan schema", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("json", false, "Emit JSON schema")
	if err := parsePermissive(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("plan schema does not accept arguments")
	}
	return writeJSON(plan.ManifestSchema())
}

func planMaterialize(args []string) error {
	if hasHelpFlag(args) {
		usagePlanMaterialize(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("plan materialize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	manifest := fs.String("manifest", "", "Path to feature.plan.yaml")
	outRoot := fs.String("out-root", "", "Output root; defaults to ~/tmp or system temp")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "manifest", "out-root"); err != nil {
		return err
	}
	result, err := plan.Materialize(plan.MaterializeOptions{ManifestPath: *manifest, OutRoot: *outRoot})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Println(result.PlanDir)
	return nil
}

func validateCommand(args []string) error {
	if hasHelpFlag(args) {
		usageValidate(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	writeLock := fs.Bool("write-lock", false, "Write feature.plan.lock.json")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("validate requires <plan-dir>")
	}
	result, err := plan.Validate(plan.ValidateOptions{PlanDir: fs.Arg(0), WriteLock: *writeLock})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Println(result.Status)
	return nil
}

func statusCommand(args []string) error {
	if hasHelpFlag(args) {
		usageStatus(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("status requires <plan-dir>")
	}
	result, err := plan.Status(fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Println(result.Status)
	return nil
}

func implementCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("implement requires subcommand: next, start, commit, push, open-pr, review, merge, or cleanup")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageImplement(os.Stdout)
		return nil
	}
	if !supportedImplementAction(action) {
		return fmt.Errorf("unsupported implement action: %s", action)
	}
	if hasHelpFlag(args[1:]) {
		usageImplementAction(os.Stdout, action)
		return nil
	}
	fs := flag.NewFlagSet("implement "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnit := fs.String("merge-unit", "", "Merge unit id")
	allowPush := fs.Bool("allow-push", false, "Allow git push")
	allowOpenPR := fs.Bool("allow-open-pr", false, "Allow GitHub PR creation")
	allowMerge := fs.Bool("allow-merge", false, "Allow PR merge")
	allowDeleteBranch := fs.Bool("allow-delete-branch", false, "Allow branch deletion")
	writeState := fs.Bool("write-state", false, "Write lifecycle state to feature.plan.lock.json")
	branch := fs.String("branch", "", "Branch name for start state")
	worktree := fs.String("worktree", "", "Worktree path for start state")
	baseSHA := fs.String("base-sha", "", "Base commit SHA for start state")
	commitSHA := fs.String("commit-sha", "", "Commit SHA for commit state")
	prNumber := fs.Int("pr", 0, "Pull request number for open-pr state")
	prURL := fs.String("pr-url", "", "Pull request URL for open-pr state")
	reviewStatus := fs.String("review-status", "", "Review status: passed | changes-applied")
	mergeCommit := fs.String("merge-commit", "", "Merge commit SHA for merge state")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args[1:], "merge-unit", "branch", "worktree", "base-sha", "commit-sha", "pr", "pr-url", "review-status", "merge-commit"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("implement %s requires <plan-dir>", action)
	}
	result, err := plan.Implement(plan.ImplementOptions{
		PlanDir:           fs.Arg(0),
		Action:            action,
		MergeUnit:         *mergeUnit,
		AllowPush:         *allowPush,
		AllowOpenPR:       *allowOpenPR,
		AllowMerge:        *allowMerge,
		AllowDeleteBranch: *allowDeleteBranch,
		WriteState:        *writeState,
		Branch:            *branch,
		Worktree:          *worktree,
		BaseSHA:           *baseSHA,
		CommitSHA:         *commitSHA,
		PRNumber:          *prNumber,
		PRURL:             *prURL,
		ReviewStatus:      *reviewStatus,
		MergeCommit:       *mergeCommit,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Println(result.Status)
	return nil
}

func workspaceCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace requires subcommand: init, validate, status, next, heartbeat, release, recover, refresh-branch, publish-refresh, evaluate-gates, gate, queue, attempt, transition, contract, approve, or external")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspace(os.Stdout)
		return nil
	}
	if !workspace.IsSupportedAction(action) {
		return fmt.Errorf("unsupported workspace action: %s", action)
	}
	if action != "attempt" && action != "contract" && action != "approve" && action != "external" && action != "gate" && action != "queue" && hasHelpFlag(args[1:]) {
		usageWorkspaceAction(os.Stdout, action)
		return nil
	}
	switch action {
	case "init":
		return workspaceInit(args[1:])
	case "validate":
		return workspaceValidate(args[1:])
	case "status":
		return workspaceStatus(args[1:])
	case "next":
		return workspaceNext(args[1:])
	case "heartbeat":
		return workspaceHeartbeat(args[1:])
	case "release":
		return workspaceRelease(args[1:])
	case "recover":
		return workspaceRecover(args[1:])
	case "refresh-branch":
		return workspaceRefreshBranch(args[1:])
	case "publish-refresh":
		return workspacePublishRefresh(args[1:])
	case "evaluate-gates":
		return workspaceEvaluateGates(args[1:])
	case "gate":
		return workspaceGate(args[1:])
	case "queue":
		return workspaceQueue(args[1:])
	case "attempt":
		return workspaceAttempt(args[1:])
	case "transition":
		return workspaceTransition(args[1:])
	case "contract":
		return workspaceContract(args[1:])
	case "approve":
		return workspaceApprove(args[1:])
	case "external":
		return workspaceExternal(args[1:])
	default:
		return workspace.ErrNotImplemented(action)
	}
}

func workspaceInit(args []string) error {
	fs := flag.NewFlagSet("workspace init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	manifest := fs.String("manifest", "", "Path to feature.workspace.yaml")
	writeLock := fs.Bool("write-lock", false, "Write feature.workspace.lock.json")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "manifest"); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("workspace init accepts only flags")
	}
	result, err := workspace.Init(workspace.InitOptions{ManifestPath: *manifest, WriteLock: *writeLock})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	if result.LockPath != "" {
		fmt.Printf("initialized %s lock=%s view=%s\n", result.WorkspaceDir, result.LockPath, result.ViewPath)
		return nil
	}
	fmt.Printf("initialized %s\n", result.WorkspaceDir)
	return nil
}

func workspaceEvaluateGates(args []string) error {
	fs := flag.NewFlagSet("workspace evaluate-gates", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace evaluate-gates requires <workspace-dir>")
	}
	result, err := workspace.EvaluateGates(workspace.GateEvaluateOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("gates %s attempt=%s input_hash=%s output_hash=%s\n", result.MergeUnitID, result.AttemptID, result.InputHash, result.OutputHash)
	for _, gate := range result.Gates {
		fmt.Printf("gate %s status=%s reason=%s\n", gate.Gate, gate.Status, gate.Reason)
	}
	return nil
}

func workspaceGate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace gate requires subcommand: override or record")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceGate(os.Stdout)
		return nil
	}
	switch action {
	case "override":
		if hasHelpFlag(args[1:]) {
			usageWorkspaceGateAction(os.Stdout, action)
			return nil
		}
		return workspaceGateOverride(args[1:])
	case "record":
		if hasHelpFlag(args[1:]) {
			usageWorkspaceGateAction(os.Stdout, action)
			return nil
		}
		return workspaceGateRecord(args[1:])
	default:
		return fmt.Errorf("unsupported workspace gate action: %s", action)
	}
}

func workspaceGateOverride(args []string) error {
	fs := flag.NewFlagSet("workspace gate override", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	gate := fs.String("gate", "", "Gate name")
	status := fs.String("status", "", "Override status")
	reason := fs.String("reason", "", "Operator reason")
	inputHash := fs.String("input-hash", "", "Evaluator input hash")
	headSHA := fs.String("head-sha", "", "Pinned head SHA")
	baseSHA := fs.String("base-sha", "", "Pinned base SHA")
	operator := fs.String("operator", "", "Operator ID")
	expiresIn := fs.String("expires-in", "", "Duration until expiry")
	expiresAt := fs.String("expires-at", "", "RFC3339 expiry timestamp")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "gate", "status", "reason", "input-hash", "head-sha", "base-sha", "operator", "expires-in", "expires-at"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace gate override requires <workspace-dir>")
	}
	parsedExpiresIn, parsedExpiresAt, err := parseApprovalExpiry(*expiresIn, *expiresAt)
	if err != nil {
		return err
	}
	result, err := workspace.OverrideGate(workspace.GateOverrideOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		Gate:         *gate,
		Status:       *status,
		Reason:       *reason,
		InputHash:    *inputHash,
		HeadSHA:      *headSHA,
		BaseSHA:      *baseSHA,
		Operator:     *operator,
		ExpiresIn:    parsedExpiresIn,
		ExpiresAt:    parsedExpiresAt,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("overrode %s gate=%s status=%s input_hash=%s expires_at=%s\n", result.Override.MergeUnitID, result.Override.Gate, result.Override.Status, result.Override.InputHash, result.Override.ExpiresAt)
	return nil
}

func workspaceGateRecord(args []string) error {
	fs := flag.NewFlagSet("workspace gate record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	gate := fs.String("gate", "", "Gate name")
	status := fs.String("status", "", "Evidence status")
	inputHash := fs.String("input-hash", "", "Evaluator input hash")
	headSHA := fs.String("head-sha", "", "Pinned head SHA")
	baseSHA := fs.String("base-sha", "", "Pinned base SHA")
	command := fs.String("command", "", "Command that produced the evidence")
	reviewer := fs.String("reviewer", "", "Reviewer identity that produced the evidence")
	summary := fs.String("summary", "", "Evidence summary")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "gate", "status", "input-hash", "head-sha", "base-sha", "command", "reviewer", "summary"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace gate record requires <workspace-dir>")
	}
	result, err := workspace.RecordGateEvidence(workspace.GateEvidenceOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
		Gate:         *gate,
		Status:       *status,
		InputHash:    *inputHash,
		HeadSHA:      *headSHA,
		BaseSHA:      *baseSHA,
		Command:      *command,
		Reviewer:     *reviewer,
		Summary:      *summary,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("recorded %s gate=%s status=%s input_hash=%s evidence=%s\n", result.Evidence.MergeUnitID, result.Evidence.Gate, result.Evidence.Status, result.Evidence.InputHash, result.Evidence.EvidenceID)
	return nil
}

func workspaceQueue(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace queue requires subcommand: enter")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceQueue(os.Stdout)
		return nil
	}
	if action != "enter" {
		return fmt.Errorf("unsupported workspace queue action: %s", action)
	}
	if hasHelpFlag(args[1:]) {
		usageWorkspaceQueueAction(os.Stdout, action)
		return nil
	}
	return workspaceQueueEnter(args[1:])
}

func workspaceQueueEnter(args []string) error {
	fs := flag.NewFlagSet("workspace queue enter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	approvalID := fs.String("approval", "", "Approval ID")
	scope := fs.String("scope", "", "Approval scope")
	pr := fs.String("pr", "", "Pull request number or URL")
	branch := fs.String("branch", "", "Branch target")
	headSHA := fs.String("head-sha", "", "Approved head SHA")
	baseSHA := fs.String("base-sha", "", "Approved base SHA")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "approval", "scope", "pr", "branch", "head-sha", "base-sha"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace queue enter requires <workspace-dir>")
	}
	result, err := workspace.QueueMergeUnit(workspace.MergeQueueOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
		ApprovalID:   *approvalID,
		Scope:        *scope,
		PR:           *pr,
		Branch:       *branch,
		HeadSHA:      *headSHA,
		BaseSHA:      *baseSHA,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	if result.Status == "blocked" {
		fmt.Printf("queue blocked conditions=%d\n", len(result.BlockingConditions))
		for _, condition := range result.BlockingConditions {
			fmt.Printf("blocked type=%s resource=%s status=%s required_action=%s\n", condition.Type, condition.Resource, condition.Status, condition.RequiredAction)
		}
		return nil
	}
	fmt.Printf("queued %s attempt=%s position=%d approval=%s\n", result.Queue.MergeUnitID, result.Queue.AttemptID, result.Queue.Position, result.Queue.ApprovalID)
	return nil
}

func workspaceValidate(args []string) error {
	fs := flag.NewFlagSet("workspace validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	writeLock := fs.Bool("write-lock", false, "Write feature.workspace.lock.json")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace validate requires <workspace-dir>")
	}
	result, err := workspace.Validate(workspace.ValidateOptions{WorkspaceDir: fs.Arg(0), WriteLock: *writeLock})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Println(result.Status)
	return nil
}

func workspaceStatus(args []string) error {
	fs := flag.NewFlagSet("workspace status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace status requires <workspace-dir>")
	}
	result, err := workspace.Status(fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	writeWorkspaceStatusText(result)
	return nil
}

func writeWorkspaceStatusText(result workspace.StatusResult) {
	fmt.Printf("workspace %s\n", result.WorkspaceID)
	fmt.Printf("base_ref %s\n", result.BaseRef)
	fmt.Printf(
		"merge_units total=%d pending=%d in_progress=%d completed=%d failed=%d ready=%d blocked=%d\n",
		result.TotalMergeUnits,
		result.Counts[workspace.MergeUnitPending],
		result.Counts[workspace.MergeUnitInProgress],
		result.Counts[workspace.MergeUnitCompleted],
		result.Counts[workspace.MergeUnitFailed],
		len(result.Ready),
		len(result.Blocked),
	)
	if len(result.Ready) > 0 {
		fmt.Printf("ready %s\n", strings.Join(result.Ready, ", "))
	}
	if len(result.Blocked) > 0 {
		fmt.Printf("blocked %s\n", strings.Join(blockedWorkspaceUnitSummaries(result), ", "))
	}
	if len(result.Blockers) > 0 {
		writeWorkspaceBlockerGroups(result.Blockers)
	}
	if len(result.ExternalIntents) > 0 {
		writeWorkspaceExternalIntentSummary(result.ExternalIntents)
	}
}

func blockedWorkspaceUnitSummaries(result workspace.StatusResult) []string {
	blockedByUnit := map[string][]string{}
	for _, unit := range result.MergeUnits {
		blockedByUnit[unit.ID] = unit.BlockedBy
	}
	summaries := []string{}
	for _, id := range result.Blocked {
		blockedBy := blockedByUnit[id]
		if len(blockedBy) == 0 {
			summaries = append(summaries, id)
			continue
		}
		summaries = append(summaries, fmt.Sprintf("%s (blocked_by: %s)", id, strings.Join(blockedBy, ", ")))
	}
	return summaries
}

func workspaceNext(args []string) error {
	fs := flag.NewFlagSet("workspace next", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentID := fs.String("agent", "", "Agent ID claiming the next ready merge unit")
	claim := fs.Bool("claim", false, "Create a lease for the selected merge unit")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "agent"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace next requires <workspace-dir>")
	}
	result, err := workspace.Next(workspace.NextOptions{WorkspaceDir: fs.Arg(0), AgentID: *agentID, Claim: *claim})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	writeWorkspaceNextText(result)
	return nil
}

func writeWorkspaceNextText(result workspace.NextResult) {
	if result.Status == "none" {
		fmt.Println("no ready merge units")
		return
	}
	fmt.Printf(
		"claimed %s lease=%s agent=%s expires_at=%s lifecycle=%s\n",
		result.MergeUnitID,
		result.LeaseID,
		result.AgentID,
		result.LeaseExpiresAt,
		result.Lifecycle,
	)
}

func workspaceHeartbeat(args []string) error {
	return workspaceLeaseCommand("heartbeat", args)
}

func workspaceRelease(args []string) error {
	return workspaceLeaseCommand("release", args)
}

func workspaceLeaseCommand(action string, args []string) error {
	fs := flag.NewFlagSet("workspace "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "agent", "lease"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace %s requires <workspace-dir>", action)
	}
	opts := workspace.LeaseOptions{WorkspaceDir: fs.Arg(0), AgentID: *agentID, LeaseID: *leaseID}
	var (
		result workspace.LeaseResult
		err    error
	)
	if action == "heartbeat" {
		result, err = workspace.Heartbeat(opts)
	} else {
		result, err = workspace.Release(opts)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	writeWorkspaceLeaseText(result)
	return nil
}

func writeWorkspaceLeaseText(result workspace.LeaseResult) {
	if result.Status == "extended" {
		fmt.Printf("extended %s lease=%s agent=%s expires_at=%s lifecycle=%s\n", result.MergeUnitID, result.LeaseID, result.AgentID, result.LeaseExpiresAt, result.Lifecycle)
		return
	}
	fmt.Printf("released %s lease=%s agent=%s lifecycle=%s\n", result.MergeUnitID, result.LeaseID, result.AgentID, result.Lifecycle)
}

func workspaceRecover(args []string) error {
	fs := flag.NewFlagSet("workspace recover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace recover requires <workspace-dir>")
	}
	result, err := workspace.Recover(workspace.RecoverOptions{WorkspaceDir: fs.Arg(0)})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	writeWorkspaceRecoverText(result)
	return nil
}

func workspaceRefreshBranch(args []string) error {
	fs := flag.NewFlagSet("workspace refresh-branch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	local := fs.Bool("local", false, "Refresh an unpublished local branch")
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	worktree := fs.String("worktree", "", "Worktree path; defaults to current attempt worktree")
	newBase := fs.String("new-base", "", "New base ref or SHA")
	backupRef := fs.String("backup-ref", "", "Backup branch ref; defaults to timestamped branch backup")
	var commandResults commandResultFlags
	fs.Var(&commandResults, "command-result", "Validation command result as command=status; repeatable")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "worktree", "new-base", "backup-ref", "command-result"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace refresh-branch requires <workspace-dir>")
	}
	parsedResults, err := parseCommandResultFlags(commandResults)
	if err != nil {
		return err
	}
	result, err := workspace.RefreshBranch(workspace.RefreshBranchOptions{
		WorkspaceDir:   fs.Arg(0),
		MergeUnitID:    *mergeUnitID,
		AttemptID:      *attemptID,
		AgentID:        *agentID,
		LeaseID:        *leaseID,
		Local:          *local,
		Worktree:       *worktree,
		NewBase:        *newBase,
		BackupRef:      *backupRef,
		CommandResults: parsedResults,
	})
	if err != nil {
		var verificationErr workspace.RefreshVerificationError
		if *asJSON && errors.As(err, &verificationErr) {
			if writeErr := writeJSON(verificationErr.Result); writeErr != nil {
				return writeErr
			}
		}
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("refreshed %s attempt=%s branch=%s backup=%s post_head=%s evidence=%s\n", result.MergeUnitID, result.AttemptID, result.Branch, result.Evidence.BackupRef, result.Evidence.PostHead, result.EvidencePath)
	return nil
}

func workspacePublishRefresh(args []string) error {
	fs := flag.NewFlagSet("workspace publish-refresh", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	approvalID := fs.String("approval", "", "Approval ID authorizing the publish")
	branch := fs.String("branch", "", "Branch target; defaults to latest refreshed branch")
	worktree := fs.String("worktree", "", "Worktree path; defaults to current attempt worktree")
	remote := fs.String("remote", "", "Git remote; defaults to origin")
	expectedRemoteSHA := fs.String("expected-remote-sha", "", "Expected current remote branch SHA")
	scope := fs.String("scope", "", "Approval scope; defaults to merge-unit")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "approval", "branch", "worktree", "remote", "expected-remote-sha", "scope"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace publish-refresh requires <workspace-dir>")
	}
	result, err := workspace.PublishRefresh(workspace.PublishRefreshOptions{
		WorkspaceDir:      fs.Arg(0),
		MergeUnitID:       *mergeUnitID,
		AttemptID:         *attemptID,
		AgentID:           *agentID,
		LeaseID:           *leaseID,
		ApprovalID:        *approvalID,
		Branch:            *branch,
		Worktree:          *worktree,
		Remote:            *remote,
		ExpectedRemoteSHA: *expectedRemoteSHA,
		Scope:             *scope,
	})
	if err != nil {
		var remoteMoved workspace.RemoteBranchMovedError
		if *asJSON && errors.As(err, &remoteMoved) {
			if writeErr := writeJSON(remoteMoved.Result); writeErr != nil {
				return writeErr
			}
		}
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	if result.Plan == nil {
		return fmt.Errorf("workspace publish-refresh produced no provider plan")
	}
	for _, command := range result.Plan.Commands {
		fmt.Println(command)
	}
	return nil
}

func writeWorkspaceRecoverText(result workspace.RecoverResult) {
	fmt.Printf("recovered %d leases\n", result.RecoveredCount)
	if len(result.Actions) > 0 {
		for _, action := range result.Actions {
			fmt.Printf("action %s merge_unit=%s lease=%s agent=%s status=%s\n", action.Type, action.MergeUnitID, action.LeaseID, action.AgentID, action.Status)
		}
	}
	if len(result.Ready) > 0 {
		fmt.Printf("ready %s\n", strings.Join(result.Ready, ", "))
	}
	if len(result.Leased) > 0 {
		fmt.Printf("leased %s\n", strings.Join(result.Leased, ", "))
	}
	if len(result.Blocked) > 0 {
		fmt.Printf("blocked %s\n", strings.Join(result.Blocked, ", "))
	}
	if len(result.RemainingBlockers) > 0 {
		writeWorkspaceBlockerGroups(result.RemainingBlockers)
	}
	if len(result.ExternalIntents) > 0 {
		writeWorkspaceExternalIntentSummary(result.ExternalIntents)
	}
}

func writeWorkspaceBlockerGroups(groups []workspace.WorkspaceBlockerGroup) {
	fmt.Println("blockers")
	for _, group := range groups {
		requiredAction := group.RequiredAction
		if requiredAction == "" {
			requiredAction = "none"
		}
		fmt.Printf("  %s required_action=%s count=%d", group.Type, requiredAction, group.Count)
		if len(group.MergeUnits) > 0 {
			fmt.Printf(" merge_units=%s", strings.Join(group.MergeUnits, ","))
		}
		fmt.Println()
	}
}

func writeWorkspaceExternalIntentSummary(intents []workspace.ExternalIntentReport) {
	counts := map[string]int{}
	for _, intent := range intents {
		key := intent.ResultSource
		if intent.ResultStatus != "" {
			key += ":" + intent.ResultStatus
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	fmt.Printf("external_intents %s\n", strings.Join(parts, " "))
}

func workspaceTransition(args []string) error {
	fs := flag.NewFlagSet("workspace transition", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	from := fs.String("from", "", "Expected current lifecycle")
	to := fs.String("to", "", "Target lifecycle")
	var evidence evidenceFlags
	fs.Var(&evidence, "evidence", "Transition evidence as key=value; repeatable")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "from", "to", "evidence"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace transition requires <workspace-dir>")
	}
	parsedEvidence, err := parseEvidenceFlags(evidence)
	if err != nil {
		return err
	}
	result, err := workspace.Transition(workspace.TransitionOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
		From:         *from,
		To:           *to,
		Evidence:     parsedEvidence,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("transitioned %s attempt=%s from=%s to=%s event=%s\n", result.MergeUnitID, result.AttemptID, result.From, result.To, result.EventType)
	return nil
}

func workspaceAttempt(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace attempt requires subcommand: start or abandon")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceAttempt(os.Stdout)
		return nil
	}
	if action != "start" && action != "abandon" {
		return fmt.Errorf("unsupported workspace attempt action: %s", action)
	}
	if hasHelpFlag(args[1:]) {
		usageWorkspaceAttemptAction(os.Stdout, action)
		return nil
	}
	switch action {
	case "start":
		return workspaceAttemptStart(args[1:])
	case "abandon":
		return workspaceAttemptAbandon(args[1:])
	default:
		return workspace.ErrNotImplemented(action)
	}
}

func workspaceAttemptStart(args []string) error {
	fs := flag.NewFlagSet("workspace attempt start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	baseSHA := fs.String("base-sha", "", "Base commit SHA")
	mode := fs.String("mode", "", "Start mode; defaults to fresh-from-base")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "agent", "lease", "base-sha", "mode"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace attempt start requires <workspace-dir>")
	}
	result, err := workspace.StartAttempt(workspace.AttemptStartOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
		BaseSHA:      *baseSHA,
		Mode:         *mode,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("started %s attempt=%s branch=%s worktree=%s mode=%s\n", result.MergeUnitID, result.AttemptID, result.Branch, result.Worktree, result.Mode)
	for _, command := range result.Commands {
		fmt.Printf("command %s\n", command)
	}
	return nil
}

func workspaceAttemptAbandon(args []string) error {
	fs := flag.NewFlagSet("workspace attempt abandon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	reason := fs.String("reason", "", "Reason for abandoning the attempt")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "reason"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace attempt abandon requires <workspace-dir>")
	}
	result, err := workspace.AbandonAttempt(workspace.AttemptAbandonOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
		Reason:       *reason,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("abandoned %s attempt=%s reason=%s\n", result.MergeUnitID, result.AttemptID, result.Reason)
	return nil
}

func workspaceContract(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace contract requires subcommand: publish, verify, bind, or check-contracts")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceContract(os.Stdout)
		return nil
	}
	if action != "publish" && action != "verify" && action != "bind" && action != "check-contracts" {
		return fmt.Errorf("unsupported workspace contract action: %s", action)
	}
	if hasHelpFlag(args[1:]) {
		usageWorkspaceContractAction(os.Stdout, action)
		return nil
	}
	switch action {
	case "publish":
		return workspaceContractPublish(args[1:])
	case "verify":
		return workspaceContractVerify(args[1:])
	case "bind":
		return workspaceContractBind(args[1:])
	case "check-contracts":
		return workspaceContractCheckContracts(args[1:])
	default:
		return workspace.ErrNotImplemented("contract " + action)
	}
}

func workspaceContractPublish(args []string) error {
	fs := flag.NewFlagSet("workspace contract publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	contractID := fs.String("contract", "", "Contract gate ID")
	version := fs.String("version", "", "Published contract version")
	artifactID := fs.String("artifact", "", "Artifact ID; required when the contract has multiple artifacts")
	producerMergeUnitID := fs.String("producer-merge-unit", "", "Explicit producer merge unit ID")
	producerCommit := fs.String("producer-commit", "", "Producer commit SHA")
	attemptID := fs.String("attempt", "", "Current producer attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the producer lease")
	leaseID := fs.String("lease", "", "Producer lease ID")
	var commandResults commandResultFlags
	fs.Var(&commandResults, "command-result", "Validation command result as command=status; repeatable")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "contract", "version", "artifact", "producer-merge-unit", "producer-commit", "attempt", "agent", "lease", "command-result"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace contract publish requires <workspace-dir>")
	}
	parsedResults, err := parseCommandResultFlags(commandResults)
	if err != nil {
		return err
	}
	result, err := workspace.PublishContract(workspace.ContractPublishOptions{
		WorkspaceDir:        fs.Arg(0),
		ContractID:          *contractID,
		Version:             *version,
		ArtifactID:          *artifactID,
		ProducerMergeUnitID: *producerMergeUnitID,
		ProducerCommit:      *producerCommit,
		AttemptID:           *attemptID,
		AgentID:             *agentID,
		LeaseID:             *leaseID,
		CommandResults:      parsedResults,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("published %s version=%s artifact=%s hash=%s producer=%s commit=%s\n", result.ContractID, result.Version, result.ArtifactID, result.ArtifactHash, result.ProducerMergeUnitID, result.ProducerCommit)
	return nil
}

func workspaceContractVerify(args []string) error {
	fs := flag.NewFlagSet("workspace contract verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	contractID := fs.String("contract", "", "Contract gate ID")
	artifactID := fs.String("artifact", "", "Artifact ID; required when the contract has multiple artifacts")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "contract", "artifact"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace contract verify requires <workspace-dir>")
	}
	result, err := workspace.VerifyContract(workspace.ContractVerifyOptions{
		WorkspaceDir: fs.Arg(0),
		ContractID:   *contractID,
		ArtifactID:   *artifactID,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	writeWorkspaceContractVerifyText(result)
	return nil
}

func writeWorkspaceContractVerifyText(result workspace.ContractVerifyResult) {
	if result.Status == "unpublished" {
		fmt.Printf("unpublished %s artifact=%s exists=%t\n", result.ContractID, result.ArtifactID, result.ArtifactExists)
		return
	}
	fmt.Printf("verified %s status=%s artifact=%s hash_matches=%t\n", result.ContractID, result.Status, result.ArtifactID, result.HashMatches)
}

func workspaceContractBind(args []string) error {
	fs := flag.NewFlagSet("workspace contract bind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	contractID := fs.String("contract", "", "Contract gate ID")
	artifactID := fs.String("artifact", "", "Artifact ID; required when the contract has multiple artifacts")
	mergeUnitID := fs.String("merge-unit", "", "Consumer merge unit ID")
	attemptID := fs.String("attempt", "", "Current consumer attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the consumer lease")
	leaseID := fs.String("lease", "", "Consumer lease ID")
	var commandResults commandResultFlags
	fs.Var(&commandResults, "command-result", "Validation command result as command=status; repeatable")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "contract", "artifact", "merge-unit", "attempt", "agent", "lease", "command-result"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace contract bind requires <workspace-dir>")
	}
	parsedResults, err := parseCommandResultFlags(commandResults)
	if err != nil {
		return err
	}
	result, err := workspace.BindContract(workspace.ContractBindOptions{
		WorkspaceDir:   fs.Arg(0),
		ContractID:     *contractID,
		ArtifactID:     *artifactID,
		MergeUnitID:    *mergeUnitID,
		AttemptID:      *attemptID,
		AgentID:        *agentID,
		LeaseID:        *leaseID,
		CommandResults: parsedResults,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("bound %s merge_unit=%s attempt=%s artifact=%s version=%s hash=%s\n", result.ContractID, result.MergeUnitID, result.AttemptID, result.ArtifactID, result.Version, result.ArtifactHash)
	return nil
}

func workspaceContractCheckContracts(args []string) error {
	fs := flag.NewFlagSet("workspace contract check-contracts", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Consumer merge unit ID")
	attemptID := fs.String("attempt", "", "Current consumer attempt ID")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace contract check-contracts requires <workspace-dir>")
	}
	result, err := workspace.CheckContracts(workspace.ContractCheckOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	writeWorkspaceContractCheckText(result)
	return nil
}

func writeWorkspaceContractCheckText(result workspace.ContractCheckResult) {
	fmt.Printf("contracts %s attempt=%s status=%s\n", result.MergeUnitID, result.AttemptID, result.Status)
	for _, binding := range result.Bindings {
		fmt.Printf("binding %s artifact=%s status=%s version=%s bound_version=%s\n", binding.ContractID, binding.ArtifactID, binding.Status, binding.Version, binding.BoundVersion)
	}
}

func workspaceApprove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace approve requires subcommand: grant, check, or consume")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceApprove(os.Stdout)
		return nil
	}
	if action != "grant" && action != "check" && action != "consume" {
		return fmt.Errorf("unsupported workspace approve action: %s", action)
	}
	if hasHelpFlag(args[1:]) {
		usageWorkspaceApproveAction(os.Stdout, action)
		return nil
	}
	switch action {
	case "grant":
		return workspaceApproveGrant(args[1:])
	case "check":
		return workspaceApproveCheck(args[1:])
	case "consume":
		return workspaceApproveConsume(args[1:])
	default:
		return workspace.ErrNotImplemented("approve " + action)
	}
}

func workspaceApproveGrant(args []string) error {
	fs := flag.NewFlagSet("workspace approve grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	var actions stringFlags
	fs.Var(&actions, "action", "Allowed action; repeatable or comma-separated")
	scope := fs.String("scope", "", "Approval scope; defaults to merge-unit")
	pr := fs.String("pr", "", "Optional PR number or URL")
	branch := fs.String("branch", "", "Optional branch constraint")
	headSHA := fs.String("head-sha", "", "Optional head SHA constraint")
	baseSHA := fs.String("base-sha", "", "Optional base SHA constraint")
	maxUses := fs.Int("max-uses", 1, "Maximum number of uses")
	expiresIn := fs.String("expires-in", "", "Duration until expiry, for example 30m")
	expiresAt := fs.String("expires-at", "", "RFC3339 expiry timestamp")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "action", "scope", "pr", "branch", "head-sha", "base-sha", "max-uses", "expires-in", "expires-at"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace approve grant requires <workspace-dir>")
	}
	if *maxUses <= 0 {
		return fmt.Errorf("--max-uses must be greater than zero")
	}
	parsedExpiresIn, parsedExpiresAt, err := parseApprovalExpiry(*expiresIn, *expiresAt)
	if err != nil {
		return err
	}
	result, err := workspace.GrantApproval(workspace.ApprovalGrantOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		AgentID:      *agentID,
		LeaseID:      *leaseID,
		Actions:      actions,
		Scope:        *scope,
		PR:           *pr,
		Branch:       *branch,
		HeadSHA:      *headSHA,
		BaseSHA:      *baseSHA,
		MaxUses:      *maxUses,
		ExpiresIn:    parsedExpiresIn,
		ExpiresAt:    parsedExpiresAt,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("approved %s actions=%s expires_at=%s max_uses=%d\n", result.Approval.ApprovalID, strings.Join(result.Approval.Actions, ","), result.Approval.ExpiresAt, result.Approval.MaxUses)
	return nil
}

func workspaceApproveCheck(args []string) error {
	fs := flag.NewFlagSet("workspace approve check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	action := fs.String("action", "", "Action to check")
	scope := fs.String("scope", "", "Approval scope; defaults to merge-unit")
	pr := fs.String("pr", "", "Optional PR number or URL")
	branch := fs.String("branch", "", "Optional branch constraint")
	headSHA := fs.String("head-sha", "", "Optional head SHA constraint")
	baseSHA := fs.String("base-sha", "", "Optional base SHA constraint")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "action", "scope", "pr", "branch", "head-sha", "base-sha"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace approve check requires <workspace-dir>")
	}
	result, err := workspace.CheckApproval(workspace.ApprovalCheckOptions{
		WorkspaceDir: fs.Arg(0),
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		Action:       *action,
		Scope:        *scope,
		PR:           *pr,
		Branch:       *branch,
		HeadSHA:      *headSHA,
		BaseSHA:      *baseSHA,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("approval %s action=%s matches=%d\n", result.Status, result.Action, len(result.Approvals))
	return nil
}

func workspaceApproveConsume(args []string) error {
	fs := flag.NewFlagSet("workspace approve consume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	approvalID := fs.String("approval", "", "Approval ID")
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	action := fs.String("action", "", "Action to consume")
	scope := fs.String("scope", "", "Approval scope; defaults to merge-unit")
	pr := fs.String("pr", "", "Optional PR number or URL")
	branch := fs.String("branch", "", "Optional branch constraint")
	headSHA := fs.String("head-sha", "", "Optional head SHA constraint")
	baseSHA := fs.String("base-sha", "", "Optional base SHA constraint")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "approval", "merge-unit", "attempt", "action", "scope", "pr", "branch", "head-sha", "base-sha"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace approve consume requires <workspace-dir>")
	}
	result, err := workspace.ConsumeApproval(workspace.ApprovalConsumeOptions{
		WorkspaceDir: fs.Arg(0),
		ApprovalID:   *approvalID,
		MergeUnitID:  *mergeUnitID,
		AttemptID:    *attemptID,
		Action:       *action,
		Scope:        *scope,
		PR:           *pr,
		Branch:       *branch,
		HeadSHA:      *headSHA,
		BaseSHA:      *baseSHA,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("consumed %s used=%d/%d\n", result.Approval.ApprovalID, result.Approval.UsedCount, result.Approval.MaxUses)
	return nil
}

func workspaceExternal(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace external requires subcommand: intent or plan")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceExternal(os.Stdout)
		return nil
	}
	if action == "plan" {
		if hasHelpFlag(args[1:]) {
			usageWorkspaceExternalPlan(os.Stdout)
			return nil
		}
		return workspaceExternalPlan(args[1:])
	}
	if action != "intent" {
		return fmt.Errorf("unsupported workspace external action: %s", action)
	}
	return workspaceExternalIntent(args[1:])
}

func workspaceExternalPlan(args []string) error {
	fs := flag.NewFlagSet("workspace external plan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	approvalID := fs.String("approval", "", "Approval ID authorizing this intent")
	action := fs.String("action", "", "External action: push, open-pr, merge, or remote-delete")
	scope := fs.String("scope", "", "Approval scope; defaults to merge-unit")
	branch := fs.String("branch", "", "Branch target")
	pr := fs.String("pr", "", "PR number or URL target")
	headSHA := fs.String("head-sha", "", "Requested head SHA")
	baseSHA := fs.String("base-sha", "", "Expected base SHA")
	remote := fs.String("remote", "", "Git remote; defaults to origin")
	worktree := fs.String("worktree", "", "Worktree for git commands; defaults to attempt worktree")
	title := fs.String("title", "", "PR title for open-pr")
	body := fs.String("body", "", "PR body text before workspace markers")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "approval", "action", "scope", "branch", "pr", "head-sha", "base-sha", "remote", "worktree", "title", "body"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace external plan requires <workspace-dir>")
	}
	result, err := workspace.PlanExternalProviderCommand(workspace.ExternalProviderPlanOptions{
		WorkspaceDir:     fs.Arg(0),
		MergeUnitID:      *mergeUnitID,
		AttemptID:        *attemptID,
		AgentID:          *agentID,
		LeaseID:          *leaseID,
		ApprovalID:       *approvalID,
		Action:           *action,
		Scope:            *scope,
		Branch:           *branch,
		PR:               *pr,
		RequestedHeadSHA: *headSHA,
		ExpectedBaseSHA:  *baseSHA,
		Remote:           *remote,
		Worktree:         *worktree,
		Title:            *title,
		Body:             *body,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	for _, command := range result.Plan.Commands {
		fmt.Println(command)
	}
	return nil
}

func workspaceExternalIntent(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace external intent requires subcommand: reserve, result, or reconcile")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceExternalIntent(os.Stdout)
		return nil
	}
	if action != "reserve" && action != "result" && action != "reconcile" {
		return fmt.Errorf("unsupported workspace external intent action: %s", action)
	}
	if hasHelpFlag(args[1:]) {
		usageWorkspaceExternalIntentAction(os.Stdout, action)
		return nil
	}
	switch action {
	case "reserve":
		return workspaceExternalIntentReserve(args[1:])
	case "result":
		return workspaceExternalIntentResult(args[1:])
	case "reconcile":
		return workspaceExternalIntentReconcile(args[1:])
	default:
		return fmt.Errorf("unsupported workspace external intent action: %s", action)
	}
}

func workspaceExternalIntentReserve(args []string) error {
	fs := flag.NewFlagSet("workspace external intent reserve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	approvalID := fs.String("approval", "", "Approval ID authorizing this intent")
	action := fs.String("action", "", "External action: push, open-pr, merge, or remote-delete")
	scope := fs.String("scope", "", "Approval scope; defaults to merge-unit")
	branch := fs.String("branch", "", "Branch target")
	pr := fs.String("pr", "", "PR number or URL target")
	headSHA := fs.String("head-sha", "", "Requested head SHA")
	baseSHA := fs.String("base-sha", "", "Expected base SHA")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "approval", "action", "scope", "branch", "pr", "head-sha", "base-sha"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace external intent reserve requires <workspace-dir>")
	}
	result, err := workspace.ReserveExternalIntent(workspace.ExternalIntentReserveOptions{
		WorkspaceDir:     fs.Arg(0),
		MergeUnitID:      *mergeUnitID,
		AttemptID:        *attemptID,
		AgentID:          *agentID,
		LeaseID:          *leaseID,
		ApprovalID:       *approvalID,
		Action:           *action,
		Scope:            *scope,
		Branch:           *branch,
		PR:               *pr,
		RequestedHeadSHA: *headSHA,
		ExpectedBaseSHA:  *baseSHA,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("reserved %s action=%s target=%s idempotency_key=%s\n", result.Intent.IntentID, result.Intent.Action, result.Intent.Target, result.Intent.IdempotencyKey)
	return nil
}

func workspaceExternalIntentResult(args []string) error {
	fs := flag.NewFlagSet("workspace external intent result", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnitID := fs.String("merge-unit", "", "Merge unit ID")
	attemptID := fs.String("attempt", "", "Attempt ID")
	agentID := fs.String("agent", "", "Agent ID that owns the lease")
	leaseID := fs.String("lease", "", "Lease ID")
	intentID := fs.String("intent", "", "External intent ID")
	status := fs.String("status", "", "External result status")
	policyAccepted := fs.Bool("policy-accepted", false, "Mark this non-success result accepted by policy")
	details := fs.String("details", "", "Operator or provider result details")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "merge-unit", "attempt", "agent", "lease", "intent", "status", "details"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace external intent result requires <workspace-dir>")
	}
	result, err := workspace.RecordExternalIntentResult(workspace.ExternalIntentResultRecordOptions{
		WorkspaceDir:   fs.Arg(0),
		MergeUnitID:    *mergeUnitID,
		AttemptID:      *attemptID,
		AgentID:        *agentID,
		LeaseID:        *leaseID,
		IntentID:       *intentID,
		Status:         *status,
		PolicyAccepted: *policyAccepted,
		Details:        *details,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("recorded %s status=%s accepted=%t\n", result.Intent.IntentID, result.Result.Status, result.Result.Accepted)
	return nil
}

func workspaceExternalIntentReconcile(args []string) error {
	fs := flag.NewFlagSet("workspace external intent reconcile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	intentID := fs.String("intent", "", "External intent ID")
	operator := fs.String("operator", "", "Operator identity")
	details := fs.String("details", "", "Reconciliation details")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args, "intent", "operator", "details"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("workspace external intent reconcile requires <workspace-dir>")
	}
	result, err := workspace.ReconcileExternalIntent(workspace.ExternalIntentReconcileOptions{
		WorkspaceDir: fs.Arg(0),
		IntentID:     *intentID,
		Operator:     *operator,
		Details:      *details,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(result)
	}
	fmt.Printf("reconciled %s operator=%s\n", result.Intent.IntentID, result.Result.Operator)
	return nil
}

func writeJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func isHelpCommand(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func supportedImplementAction(action string) bool {
	switch action {
	case "next", "start", "commit", "push", "open-pr", "review", "merge", "cleanup":
		return true
	default:
		return false
	}
}

func parsePermissive(fs *flag.FlagSet, args []string, valueFlags ...string) error {
	flags, positionals := reorderFlags(args, valueFlags...)
	return fs.Parse(append(flags, positionals...))
}

type evidenceFlags []string

func (f *evidenceFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *evidenceFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseEvidenceFlags(values []string) (map[string]any, error) {
	evidence := map[string]any{}
	for _, value := range values {
		key, text, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("--evidence must use key=value")
		}
		key = strings.TrimSpace(key)
		text = strings.TrimSpace(text)
		if key == "" || text == "" {
			return nil, fmt.Errorf("--evidence must use non-empty key=value")
		}
		if _, exists := evidence[key]; exists {
			return nil, fmt.Errorf("duplicate evidence key: %s", key)
		}
		evidence[key] = text
	}
	return evidence, nil
}

type commandResultFlags []string

func (f *commandResultFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *commandResultFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseCommandResultFlags(values []string) ([]workspace.ContractCommandResult, error) {
	results := make([]workspace.ContractCommandResult, 0, len(values))
	for _, value := range values {
		index := strings.LastIndex(value, "=")
		if index < 0 {
			return nil, fmt.Errorf("--command-result must use command=status")
		}
		command := strings.TrimSpace(value[:index])
		status := strings.TrimSpace(value[index+1:])
		if command == "" || status == "" {
			return nil, fmt.Errorf("--command-result must use non-empty command=status")
		}
		results = append(results, workspace.ContractCommandResult{Command: command, Status: status})
	}
	return results, nil
}

type stringFlags []string

func (f *stringFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *stringFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseApprovalExpiry(expiresIn string, expiresAt string) (time.Duration, time.Time, error) {
	expiresIn = strings.TrimSpace(expiresIn)
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresIn != "" && expiresAt != "" {
		return 0, time.Time{}, fmt.Errorf("--expires-in and --expires-at are mutually exclusive")
	}
	if expiresIn == "" && expiresAt == "" {
		return 0, time.Time{}, nil
	}
	if expiresIn != "" {
		duration, err := time.ParseDuration(expiresIn)
		if err != nil {
			return 0, time.Time{}, fmt.Errorf("parse --expires-in: %w", err)
		}
		return duration, time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse --expires-at: %w", err)
	}
	return 0, parsed, nil
}

func reorderFlags(args []string, valueFlags ...string) ([]string, []string) {
	valueFlag := map[string]bool{}
	for _, name := range valueFlags {
		valueFlag[name] = true
	}
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
			if valueFlag[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return flags, positionals
}

func usageInstallSkills(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature install-skills [--plan|--install|--uninstall] [--target tools|claude|codex|all] [--json] [--install-root <dir>]

Installs or stages the delegated mise-en-place skill files and feature CLI.`)
}

func usagePlan(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature plan example
  feature plan schema [--json]
  feature plan materialize --manifest <file> [--out-root <dir>] [--json]

Use "feature plan example" for a valid feature.plan.yaml template.
Use "feature plan schema --json" for the machine-readable manifest schema.`)
}

func usagePlanExample(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature plan example

Prints a valid feature.plan.yaml example.`)
}

func usagePlanSchema(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature plan schema [--json]

Prints the feature.plan.yaml JSON schema. The --json flag is accepted for consistency.`)
}

func usagePlanMaterialize(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature plan materialize --manifest <file> [--out-root <dir>] [--json]

Materializes a feature.plan.yaml manifest into epic, feature, and story Markdown folders.
If --out-root is omitted, output defaults to ~/tmp when it exists, otherwise the system temp directory.`)
}

func usageValidate(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature validate <plan-dir> [--write-lock] [--json]

Validates a materialized plan directory. Use --write-lock before feature:implement.`)
}

func usageStatus(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature status <plan-dir> [--json]

Reports whether a plan is materialized or validated.`)
}

func usageImplement(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature implement next|start|commit|push|open-pr|review|merge|cleanup <plan-dir> [--merge-unit <id>] [--write-state] [metadata flags] [--json]

Plans guarded implementation actions for the next or selected merge unit.`)
}

func usageImplementAction(w io.Writer, action string) {
	fmt.Fprintf(w, `Usage:
  feature implement %s <plan-dir> [--merge-unit <id>] [--write-state] [--branch <name>] [--worktree <path>] [--base-sha <sha>] [--commit-sha <sha>] [--pr <number>] [--pr-url <url>] [--review-status passed|changes-applied] [--merge-commit <sha>] [--allow-push] [--allow-open-pr] [--allow-merge] [--allow-delete-branch] [--json]

Reads feature.plan.lock.json and returns the guarded next action for the selected merge unit.
`, action)
}

func usageWorkspace(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace init --manifest <file> [--write-lock] [--json]
  feature workspace validate <workspace-dir> [--write-lock] [--json]
  feature workspace status <workspace-dir> [--json]
  feature workspace next <workspace-dir> --agent <id> --claim [--json]
  feature workspace heartbeat <workspace-dir> --agent <id> --lease <id> [--json]
  feature workspace release <workspace-dir> --agent <id> --lease <id> [--json]
  feature workspace recover <workspace-dir> [--json]
  feature workspace refresh-branch <workspace-dir> --local --merge-unit <id> --attempt <id> --agent <id> --lease <id> --new-base <ref> [--worktree <path>] [--backup-ref <ref>] [--command-result <command=status>] [--json]
  feature workspace publish-refresh <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --expected-remote-sha <sha> [--branch <name>] [--remote <name>] [--worktree <path>] [--scope <scope>] [--json]
  feature workspace evaluate-gates <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> [--json]
  feature workspace gate override <workspace-dir> --merge-unit <id> --attempt <id> --gate <gate> --status retained_by_operator --reason <text> --input-hash <hash> --head-sha <sha> --base-sha <sha> --operator <id> (--expires-in <duration> | --expires-at <timestamp>) [--json]
  feature workspace gate record <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --gate <review|security|test> --status <passed|blocked> --input-hash <hash> --head-sha <sha> --base-sha <sha> (--command <cmd> | --reviewer <id>) --summary <text> [--json]
  feature workspace queue enter <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--approval <id>] [--scope <scope>] [--json]
  feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> [--mode fresh-from-base] [--json]
  feature workspace attempt abandon <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --reason <text> [--json]
  feature workspace transition <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --from <status> --to <status> --evidence <key=value> [--evidence <key=value>] [--json]
  feature workspace contract publish <workspace-dir> --contract <id> --version <version> --producer-commit <sha> (--producer-merge-unit <id> | --attempt <id> --agent <id> --lease <id>) --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]
  feature workspace contract verify <workspace-dir> --contract <id> [--artifact <id>] [--json]
  feature workspace contract bind <workspace-dir> --contract <id> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]
  feature workspace contract check-contracts <workspace-dir> --merge-unit <id> --attempt <id> [--json]
  feature workspace approve grant <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --action <action> (--expires-in <duration> | --expires-at <timestamp>) [scope and target flags] [--json]
  feature workspace approve check <workspace-dir> --merge-unit <id> --attempt <id> --action <action> [scope and target flags] [--json]
  feature workspace approve consume <workspace-dir> --approval <id> --merge-unit <id> --attempt <id> --action <action> [scope and target flags] [--json]
  feature workspace external intent reserve <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --action <push|open-pr|merge|remote-delete> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--scope <scope>] [--json]

Coordinates validated feature plans through a workspace-level orchestration layer.`)
}

func usageWorkspaceAction(w io.Writer, action string) {
	switch action {
	case "init":
		fmt.Fprintln(w, `Usage:
  feature workspace init --manifest <file> [--write-lock] [--json]

Initializes a feature workspace from feature.workspace.yaml.`)
	case "validate":
		fmt.Fprintln(w, `Usage:
  feature workspace validate <workspace-dir> [--write-lock] [--json]

Validates a feature workspace and optionally writes feature.workspace.lock.json.`)
	case "status":
		fmt.Fprintln(w, `Usage:
  feature workspace status <workspace-dir> [--json]

Reports feature workspace scheduler status.`)
	case "next":
		fmt.Fprintln(w, `Usage:
  feature workspace next <workspace-dir> --agent <id> --claim [--json]

Claims the next dependency-ready workspace merge unit for an agent.`)
	case "heartbeat":
		fmt.Fprintln(w, `Usage:
  feature workspace heartbeat <workspace-dir> --agent <id> --lease <id> [--json]

Extends an active lease owned by an agent.`)
	case "release":
		fmt.Fprintln(w, `Usage:
  feature workspace release <workspace-dir> --agent <id> --lease <id> [--json]

Releases an active lease owned by an agent.`)
	case "recover":
		fmt.Fprintln(w, `Usage:
  feature workspace recover <workspace-dir> [--json]

Recovers expired leases and rebuilds the scheduler view.`)
	case "refresh-branch":
		fmt.Fprintln(w, `Usage:
  feature workspace refresh-branch <workspace-dir> --local --merge-unit <id> --attempt <id> --agent <id> --lease <id> --new-base <ref> [--worktree <path>] [--backup-ref <ref>] [--command-result <command=status>] [--json]

Refreshes an unpublished local attempt branch with backup, rebase evidence, contribution preservation checks, and validation command results.`)
	case "publish-refresh":
		fmt.Fprintln(w, `Usage:
  feature workspace publish-refresh <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --expected-remote-sha <sha> [--branch <name>] [--remote <name>] [--worktree <path>] [--scope <scope>] [--json]

Plans an approved force-with-lease publish of the latest successful local refresh.`)
	case "evaluate-gates":
		fmt.Fprintln(w, `Usage:
  feature workspace evaluate-gates <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> [--json]

Computes and records attempt-scoped review, contract, security, test, and merge approval gates.`)
	case "gate":
		usageWorkspaceGate(w)
	case "queue":
		usageWorkspaceQueue(w)
	case "transition":
		fmt.Fprintln(w, `Usage:
  feature workspace transition <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --from <status> --to <status> --evidence <key=value> [--evidence <key=value>] [--json]

Records local lifecycle movement for the current active attempt.`)
	case "attempt":
		usageWorkspaceAttempt(w)
	case "contract":
		usageWorkspaceContract(w)
	case "approve":
		usageWorkspaceApprove(w)
	case "external":
		usageWorkspaceExternal(w)
	default:
		usageWorkspace(w)
	}
}

func usageWorkspaceGate(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace gate override <workspace-dir> --merge-unit <id> --attempt <id> --gate <gate> --status retained_by_operator --reason <text> --input-hash <hash> --head-sha <sha> --base-sha <sha> --operator <id> (--expires-in <duration> | --expires-at <timestamp>) [--json]
  feature workspace gate record <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --gate <review|security|test> --status <passed|blocked> --input-hash <hash> --head-sha <sha> --base-sha <sha> (--command <cmd> | --reviewer <id>) --summary <text> [--json]

Records scoped operator overrides and tool-proven gate evidence. Both are pinned to evaluator input hash, head SHA, and base SHA.`)
}

func usageWorkspaceGateAction(w io.Writer, action string) {
	switch action {
	case "override":
		fmt.Fprintln(w, `Usage:
  feature workspace gate override <workspace-dir> --merge-unit <id> --attempt <id> --gate <gate> --status retained_by_operator --reason <text> --input-hash <hash> --head-sha <sha> --base-sha <sha> --operator <id> (--expires-in <duration> | --expires-at <timestamp>) [--json]

Records a retained_by_operator override for an overridable gate.`)
	case "record":
		fmt.Fprintln(w, `Usage:
  feature workspace gate record <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --gate <review|security|test> --status <passed|blocked> --input-hash <hash> --head-sha <sha> --base-sha <sha> (--command <cmd> | --reviewer <id>) --summary <text> [--json]

Records tool-proven review, security, or test evidence for the current attempt.`)
	default:
		usageWorkspaceGate(w)
	}
}

func usageWorkspaceQueue(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace queue enter <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--approval <id>] [--scope <scope>] [--json]

Adds an attempt to the CAS-protected global merge queue after dependencies, contracts, gates, approvals, and blocking conditions are clear.`)
}

func usageWorkspaceQueueAction(w io.Writer, action string) {
	switch action {
	case "enter":
		fmt.Fprintln(w, `Usage:
  feature workspace queue enter <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--approval <id>] [--scope <scope>] [--json]

Queues a ready attempt for merge using a current merge approval and current gate evaluation.`)
	default:
		usageWorkspaceQueue(w)
	}
}

func usageWorkspaceAttempt(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> [--mode fresh-from-base] [--json]
  feature workspace attempt abandon <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --reason <text> [--json]

Manages concrete workspace implementation attempts.`)
}

func usageWorkspaceAttemptAction(w io.Writer, action string) {
	switch action {
	case "start":
		usageWorkspaceAttemptStart(w)
	case "abandon":
		usageWorkspaceAttemptAbandon(w)
	default:
		usageWorkspaceAttempt(w)
	}
}

func usageWorkspaceContract(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace contract publish <workspace-dir> --contract <id> --version <version> --producer-commit <sha> (--producer-merge-unit <id> | --attempt <id> --agent <id> --lease <id>) --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]
  feature workspace contract verify <workspace-dir> --contract <id> [--artifact <id>] [--json]
  feature workspace contract bind <workspace-dir> --contract <id> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]
  feature workspace contract check-contracts <workspace-dir> --merge-unit <id> --attempt <id> [--json]

Publishes, verifies, binds, and checks repository-owned contract artifacts through workspace runtime state.`)
}

func usageWorkspaceContractAction(w io.Writer, action string) {
	switch action {
	case "publish":
		fmt.Fprintln(w, `Usage:
  feature workspace contract publish <workspace-dir> --contract <id> --version <version> --producer-commit <sha> (--producer-merge-unit <id> | --attempt <id> --agent <id> --lease <id>) --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]

Records the current repository artifact hash and validation command results for a producer-owned contract.`)
	case "verify":
		fmt.Fprintln(w, `Usage:
  feature workspace contract verify <workspace-dir> --contract <id> [--artifact <id>] [--json]

Reports whether the repository artifact exists and matches the latest published hash.`)
	case "bind":
		fmt.Fprintln(w, `Usage:
  feature workspace contract bind <workspace-dir> --contract <id> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]

Binds the current consumer attempt to the latest published contract artifact.`)
	case "check-contracts":
		fmt.Fprintln(w, `Usage:
  feature workspace contract check-contracts <workspace-dir> --merge-unit <id> --attempt <id> [--json]

Reports missing, current, and stale contract bindings for a consumer attempt.`)
	default:
		usageWorkspaceContract(w)
	}
}

func usageWorkspaceApprove(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace approve grant <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --action <action> (--expires-in <duration> | --expires-at <timestamp>) [--scope <scope>] [--pr <id>] [--branch <name>] [--head-sha <sha>] [--base-sha <sha>] [--max-uses <n>] [--json]
  feature workspace approve check <workspace-dir> --merge-unit <id> --attempt <id> --action <action> [--scope <scope>] [--pr <id>] [--branch <name>] [--head-sha <sha>] [--base-sha <sha>] [--json]
  feature workspace approve consume <workspace-dir> --approval <id> --merge-unit <id> --attempt <id> --action <action> [--scope <scope>] [--pr <id>] [--branch <name>] [--head-sha <sha>] [--base-sha <sha>] [--json]

Grants, checks, and consumes scoped external-write approval capabilities. Merge approvals require a PR or branch plus head and base SHAs.`)
}

func usageWorkspaceApproveAction(w io.Writer, action string) {
	switch action {
	case "grant":
		fmt.Fprintln(w, `Usage:
  feature workspace approve grant <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --action <action> (--expires-in <duration> | --expires-at <timestamp>) [--scope <scope>] [--pr <id>] [--branch <name>] [--head-sha <sha>] [--base-sha <sha>] [--max-uses <n>] [--json]

Grants a scoped, attempt-local approval capability.`)
	case "check":
		fmt.Fprintln(w, `Usage:
  feature workspace approve check <workspace-dir> --merge-unit <id> --attempt <id> --action <action> [--scope <scope>] [--pr <id>] [--branch <name>] [--head-sha <sha>] [--base-sha <sha>] [--json]

Reports active approvals matching a target action and scope.`)
	case "consume":
		fmt.Fprintln(w, `Usage:
  feature workspace approve consume <workspace-dir> --approval <id> --merge-unit <id> --attempt <id> --action <action> [--scope <scope>] [--pr <id>] [--branch <name>] [--head-sha <sha>] [--base-sha <sha>] [--json]

Consumes one use of a matching approval capability.`)
	default:
		usageWorkspaceApprove(w)
	}
}

func usageWorkspaceExternal(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace external plan <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --action <push|open-pr|merge|remote-delete> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--scope <scope>] [--remote <name>] [--worktree <path>] [--title <text>] [--body <text>] [--json]
  feature workspace external intent reserve <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --action <push|open-pr|merge|remote-delete> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--scope <scope>] [--json]
  feature workspace external intent result <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --intent <id> --status <status> [--policy-accepted] [--details <text>] [--json]
  feature workspace external intent reconcile <workspace-dir> --intent <id> --operator <id> --details <text> [--json]

Reserves external provider-write intents, records provider results, and reconciles ambiguous outcomes without executing provider commands.`)
}

func usageWorkspaceExternalIntent(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace external intent reserve <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --action <push|open-pr|merge|remote-delete> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--scope <scope>] [--json]
  feature workspace external intent result <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --intent <id> --status <succeeded|not_performed|failed_before_side_effect|failed_after_side_effect|ambiguous> [--policy-accepted] [--details <text>] [--json]
  feature workspace external intent reconcile <workspace-dir> --intent <id> --operator <id> --details <text> [--json]

Manages external provider-write intent records.`)
}

func usageWorkspaceExternalIntentAction(w io.Writer, action string) {
	switch action {
	case "reserve":
		fmt.Fprintln(w, `Usage:
  feature workspace external intent reserve <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --action <push|open-pr|merge|remote-delete> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--scope <scope>] [--json]

Reserves an external write intent after validating the current attempt and required approval.`)
	case "result":
		fmt.Fprintln(w, `Usage:
  feature workspace external intent result <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --intent <id> --status <succeeded|not_performed|failed_before_side_effect|failed_after_side_effect|ambiguous> [--policy-accepted] [--details <text>] [--json]

Records the observed provider result for a reserved external write intent.`)
	case "reconcile":
		fmt.Fprintln(w, `Usage:
  feature workspace external intent reconcile <workspace-dir> --intent <id> --operator <id> --details <text> [--json]

Records operator reconciliation for an ambiguous external write intent.`)
	default:
		usageWorkspaceExternalIntent(w)
	}
}

func usageWorkspaceExternalPlan(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace external plan <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --approval <id> --action <push|open-pr|merge|remote-delete> (--branch <name> | --pr <id>) --head-sha <sha> --base-sha <sha> [--scope <scope>] [--remote <name>] [--worktree <path>] [--title <text>] [--body <text>] [--json]

Builds workspace-aware planned provider commands. The plan includes approval checking, intent reservation, provider execution, result recording, and PR body markers where applicable.`)
}

func usageWorkspaceAttemptStart(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> [--mode fresh-from-base] [--json]

Starts a fresh-from-base attempt for a leased merge unit.`)
}

func usageWorkspaceAttemptAbandon(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace attempt abandon <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --reason <text> [--json]

Abandons the current active attempt for a leased merge unit.`)
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
  feature workspace init|validate|status|next|heartbeat|release|recover|attempt|transition|contract [args]
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
		return fmt.Errorf("workspace requires subcommand: init, validate, status, next, heartbeat, release, recover, attempt, transition, or contract")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspace(os.Stdout)
		return nil
	}
	if !workspace.IsSupportedAction(action) {
		return fmt.Errorf("unsupported workspace action: %s", action)
	}
	if action != "attempt" && action != "contract" && hasHelpFlag(args[1:]) {
		usageWorkspaceAction(os.Stdout, action)
		return nil
	}
	switch action {
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
	case "attempt":
		return workspaceAttempt(args[1:])
	case "transition":
		return workspaceTransition(args[1:])
	case "contract":
		return workspaceContract(args[1:])
	default:
		return workspace.ErrNotImplemented(action)
	}
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

func writeWorkspaceRecoverText(result workspace.RecoverResult) {
	fmt.Printf("recovered %d leases\n", result.RecoveredCount)
	if len(result.Ready) > 0 {
		fmt.Printf("ready %s\n", strings.Join(result.Ready, ", "))
	}
	if len(result.Leased) > 0 {
		fmt.Printf("leased %s\n", strings.Join(result.Leased, ", "))
	}
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
		return fmt.Errorf("workspace contract requires subcommand: publish or verify")
	}
	action := args[0]
	if isHelpCommand(action) {
		usageWorkspaceContract(os.Stdout)
		return nil
	}
	if action != "publish" && action != "verify" {
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
  feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> [--mode fresh-from-base] [--json]
  feature workspace attempt abandon <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --reason <text> [--json]
  feature workspace transition <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --from <status> --to <status> --evidence <key=value> [--evidence <key=value>] [--json]
  feature workspace contract publish <workspace-dir> --contract <id> --version <version> --producer-commit <sha> (--producer-merge-unit <id> | --attempt <id> --agent <id> --lease <id>) --command-result <command=status> [--command-result <command=status>] [--artifact <id>] [--json]
  feature workspace contract verify <workspace-dir> --contract <id> [--artifact <id>] [--json]

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
	case "transition":
		fmt.Fprintln(w, `Usage:
  feature workspace transition <workspace-dir> --merge-unit <id> --attempt <id> --agent <id> --lease <id> --from <status> --to <status> --evidence <key=value> [--evidence <key=value>] [--json]

Records local lifecycle movement for the current active attempt.`)
	case "attempt":
		usageWorkspaceAttempt(w)
	case "contract":
		usageWorkspaceContract(w)
	default:
		usageWorkspace(w)
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

Publishes and verifies repository-owned contract artifacts through workspace runtime state.`)
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
	default:
		usageWorkspaceContract(w)
	}
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

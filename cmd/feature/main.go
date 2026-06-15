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
  feature implement next|start|commit|push|open-pr|merge <plan-dir> [--merge-unit <id>] [--allow-push] [--allow-open-pr] [--allow-merge] [--allow-delete-branch] [--json]
  feature version`)
}

func installSkills(args []string) error {
	if hasHelp(args) {
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
	if isHelp(args[0]) {
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
	if hasHelp(args) {
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
	if hasHelp(args) {
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
	if hasHelp(args) {
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
	if hasHelp(args) {
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
	if hasHelp(args) {
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
		return fmt.Errorf("implement requires subcommand: next, start, commit, push, open-pr, or merge")
	}
	action := args[0]
	if isHelp(action) {
		usageImplement(os.Stdout)
		return nil
	}
	if hasHelp(args[1:]) {
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
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := parsePermissive(fs, args[1:], "merge-unit"); err != nil {
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

func writeJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func hasHelp(args []string) bool {
	for _, arg := range args {
		if isHelp(arg) {
			return true
		}
	}
	return false
}

func isHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func parsePermissive(fs *flag.FlagSet, args []string, valueFlags ...string) error {
	flags, positionals := reorderFlags(args, valueFlags...)
	return fs.Parse(append(flags, positionals...))
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
  feature implement next|start|commit|push|open-pr|merge <plan-dir> [--merge-unit <id>] [--allow-push] [--allow-open-pr] [--allow-merge] [--allow-delete-branch] [--json]

Plans guarded implementation actions for the next or selected merge unit.`)
}

func usageImplementAction(w io.Writer, action string) {
	fmt.Fprintf(w, `Usage:
  feature implement %s <plan-dir> [--merge-unit <id>] [--allow-push] [--allow-open-pr] [--allow-merge] [--allow-delete-branch] [--json]

Reads feature.plan.lock.json and returns the guarded next action for the selected merge unit.
`, action)
}

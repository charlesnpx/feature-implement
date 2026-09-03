package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charlesnpx/feature-implement/internal/install"
	"github.com/charlesnpx/feature-implement/internal/plan"
	"github.com/charlesnpx/feature-implement/internal/workspacecmd"
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
	case "workspace":
		err = workspaceCommand(os.Args[2:])
	case "status", "implement":
		err = fmt.Errorf("feature %s was removed; use feature workspace with a schema-version-2 bundle", os.Args[1])
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
  feature workspace schema [bundle|requests|reports] [--json]
  feature workspace example
  feature workspace validate --bundle <dir> [--write-locks] [--json]
  feature workspace <action> [<subaction>] --bundle <dir> [--input <json-file|->] [--json]
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

func workspaceCommand(args []string) error {
	if len(args) == 0 || isHelpCommand(args[0]) {
		usageWorkspace(os.Stdout)
		return nil
	}
	action := args[0]
	remaining := args[1:]
	subaction := ""
	switch action {
	case "queue", "receipts", "reconcile", "control", "provider":
		return fmt.Errorf(
			"workspace %s was removed from the local-only workflow",
			action,
		)
	}
	if hasHelpFlag(remaining) {
		usageWorkspace(os.Stdout)
		return nil
	}
	if workspaceActionRequiresSubaction(action) {
		if len(remaining) == 0 || strings.HasPrefix(remaining[0], "-") {
			return fmt.Errorf("workspace %s requires a subaction", action)
		}
		subaction, remaining = remaining[0], remaining[1:]
	}
	if action == "schema" && len(remaining) > 0 && !strings.HasPrefix(remaining[0], "-") {
		subaction, remaining = remaining[0], remaining[1:]
	}
	fs := flag.NewFlagSet("workspace "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bundle := fs.String("bundle", "", "Directory containing feature.workspace.bundle.json")
	inputPath := fs.String("input", "", "Strict JSON request file, or - for stdin")
	writeLocks := fs.Bool("write-locks", false, "Write the canonical workspace lock")
	fs.Bool("json", false, "Emit JSON (workspace commands always emit JSON)")
	if err := parsePermissive(fs, remaining, "bundle", "input"); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("workspace %s accepts only flags", action)
	}
	if action == "example" {
		if subaction != "" {
			return fmt.Errorf("workspace example does not accept a subaction")
		}
		fmt.Print(workspacecmd.BundleExample())
		return nil
	}
	input, err := readWorkspaceInput(*inputPath)
	if err != nil {
		return err
	}
	result, err := workspacecmd.Execute(context.Background(), workspacecmd.Options{
		Action: action, Subaction: subaction, BundleDir: *bundle,
		Input: input, WriteLocks: *writeLocks, GeneratorVersion: Version,
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func workspaceActionRequiresSubaction(action string) bool {
	switch action {
	case "attempt", "review", "integrate", "complete":
		return true
	default:
		return false
	}
}

func readWorkspaceInput(path string) ([]byte, error) {
	switch strings.TrimSpace(path) {
	case "":
		return nil, nil
	case "-":
		return readBoundedWorkspaceInput(os.Stdin)
	default:
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read workspace input: %w", err)
		}
		defer file.Close()
		if info, statErr := file.Stat(); statErr != nil {
			return nil, fmt.Errorf("read workspace input: %w", statErr)
		} else if info.Size() > workspacecmd.MaxCommandInputBytes {
			return nil, fmt.Errorf("workspace command input exceeds %d bytes", workspacecmd.MaxCommandInputBytes)
		}
		return readBoundedWorkspaceInput(file)
	}
}

func readBoundedWorkspaceInput(reader io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, int64(workspacecmd.MaxCommandInputBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read workspace input: %w", err)
	}
	if len(content) > workspacecmd.MaxCommandInputBytes {
		return nil, fmt.Errorf("workspace command input exceeds %d bytes", workspacecmd.MaxCommandInputBytes)
	}
	return content, nil
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

Validates a standalone materialized plan directory. Workspace execution uses feature workspace validate with a schema-version-2 bundle.`)
}

func usageWorkspace(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  feature workspace schema [bundle|requests|reports] [--json]
  feature workspace example
  feature workspace validate --bundle <dir> [--write-locks] [--json]
  feature workspace init|recover --bundle <dir> --input <json-file|-> [--json]
  feature workspace status --bundle <dir> [--json]
  feature workspace attempt start|adopt-head|pause|resume|abandon --bundle <dir> --input <json-file|-> [--json]
  feature workspace review dispatch|record|record-document|ready --bundle <dir> --input <json-file|-> [--json]
  feature workspace integrate merge-unit --bundle <dir> --input <json-file|-> [--json]
  feature workspace complete verify --bundle <dir> --input <json-file|-> [--json]

The attempt pause request requires kind: checkpoint or escalation.

All mutations accept one strict schema-version-2 JSON request and record typed local journal events.`)
}

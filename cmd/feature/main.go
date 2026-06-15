package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

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
  feature plan materialize --manifest <file> [--out-root <dir>] [--json]
  feature validate <plan-dir> [--write-lock] [--json]
  feature status <plan-dir> [--json]
  feature implement next|start|commit|push|open-pr|merge <plan-dir> [--merge-unit <id>] [--allow-push] [--allow-open-pr] [--allow-merge] [--allow-delete-branch] [--json]
  feature version`)
}

func installSkills(args []string) error {
	fs := flag.NewFlagSet("install-skills", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	target := fs.String("target", "all", "tools | claude | codex | all")
	planFlag := fs.Bool("plan", false, "Print intended files without writing")
	doInstall := fs.Bool("install", false, "Install files")
	uninstall := fs.Bool("uninstall", false, "Remove files")
	asJSON := fs.Bool("json", false, "Emit mise-en-place delegated-installer JSON")
	installRoot := fs.String("install-root", "", "Stage install under this directory as if it were HOME")
	if err := fs.Parse(args); err != nil {
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
	if len(args) == 0 || args[0] != "materialize" {
		return fmt.Errorf("plan requires subcommand: materialize")
	}
	fs := flag.NewFlagSet("plan materialize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	manifest := fs.String("manifest", "", "Path to feature.plan.yaml")
	outRoot := fs.String("out-root", "", "Output root; defaults to ~/tmp or system temp")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := fs.Parse(args[1:]); err != nil {
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
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	writeLock := fs.Bool("write-lock", false, "Write feature.plan.lock.json")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := fs.Parse(args); err != nil {
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
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := fs.Parse(args); err != nil {
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
	fs := flag.NewFlagSet("implement "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mergeUnit := fs.String("merge-unit", "", "Merge unit id")
	allowPush := fs.Bool("allow-push", false, "Allow git push")
	allowOpenPR := fs.Bool("allow-open-pr", false, "Allow GitHub PR creation")
	allowMerge := fs.Bool("allow-merge", false, "Allow PR merge")
	allowDeleteBranch := fs.Bool("allow-delete-branch", false, "Allow branch deletion")
	asJSON := fs.Bool("json", false, "Emit JSON result")
	if err := fs.Parse(args[1:]); err != nil {
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

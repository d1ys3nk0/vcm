package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

var version = "0.0.1-dev"
var commit = "unknown"

const helpText = `VCM manages a Git workspace root and its child repositories as one Change.

Usage:
  vcm [global options] <command> [arguments]

Commands:
  validate             Check vcm.yml and the repository dependency graph.
  bootstrap            Clone missing configured repositories and verify existing origins.
  sync                 Fast-forward configured workspace and child trunks from origin.
  create <slug>        Synchronize origins and create an isolated Change workspace.
  list                 List recorded Changes.
  status [change]      Show a Change's lifecycle, hooks, merge, and recovery state.
  merge [change]       Run merge hooks and squash-merge child repositories, then the root.
  drop [change]        Remove the resources owned by a completed or discarded Change.
  version              Print the VCM version and source commit.

Global options:
  --workspace PATH     Workspace root. By default, VCM searches upward from the current
                       directory for vcm.yml at a Git root.
  --json               Emit machine-readable JSON to stdout.
  --dry-run            Show the operation plan without changing files or running hooks.
                       Supported by bootstrap, sync, create, merge, and drop.
  --force              For sync, reset divergent child trunks after creating recovery backups.
                       For drop, preserve recovery backups before discarding changes.
  -h, --help           Show this help.

Change selection:
  The optional [change] is a managed Change tag or its root workspace path. If it is
  omitted, run status, merge, or drop from inside that Change's root workspace.

Examples:
  vcm validate
  vcm bootstrap --workspace /work/product
  vcm create improve-search
  vcm status 260910120000-improve-search
  vcm merge --dry-run
  vcm drop 260910120000-improve-search --force

Notes:
  Flags may appear before or after the command. Slugs use lowercase kebab-case letters
  and digits. Commands that change state should be previewed with --dry-run; merge does
  not fetch, pull, or push.
`

func run(args []string) error {
	flags := flag.NewFlagSet("vcm", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	workspace := ""
	jsonOutput, dry, force := false, false, false
	flags.StringVar(&workspace, "workspace", "", "workspace root")
	flags.BoolVar(&jsonOutput, "json", false, "machine-readable output")
	flags.BoolVar(&dry, "dry-run", false, "show operation plan")
	flags.BoolVar(&force, "force", false, "preserve recovery backups and discard changes")
	// Permit global options before or after the command using the standard flag parser.
	options := []string{}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			options = append(options, args[i])
			if args[i] == "--workspace" || args[i] == "-workspace" {
				i++
				if i == len(args) {
					return fmt.Errorf("--workspace requires a path")
				}
				options = append(options, args[i])
			}
		} else {
			positionals = append(positionals, args[i])
		}
	}
	if err := flags.Parse(options); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(os.Stderr, helpText)
			return nil
		}
		return err
	}
	if len(positionals) == 0 {
		fmt.Fprint(os.Stderr, helpText)
		return fmt.Errorf("command required")
	}
	command := positionals[0]
	output := func(result any) error {
		if jsonOutput {
			return renderJSON(os.Stdout, result)
		}
		return renderHuman(os.Stdout, result)
	}
	if len(positionals) > 2 {
		return fmt.Errorf("too many arguments")
	}
	if command != "create" && command != "merge" && command != "drop" && command != "status" && len(positionals) > 1 {
		return fmt.Errorf("%s takes no arguments", command)
	}
	if force && command != "sync" && command != "drop" {
		return fmt.Errorf("--force is only supported by sync and drop")
	}
	if dry && command != "bootstrap" && command != "sync" && command != "create" && command != "merge" && command != "drop" {
		return fmt.Errorf("--dry-run is only supported by bootstrap, sync, create, merge, and drop")
	}
	if command == "version" {
		return output(versionResult{Version: version, Commit: commit})
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if workspace == "" {
		workspace = cwd
	}
	engine, err := vcm.Open(workspace, os.Stderr)
	if err != nil {
		return err
	}
	engine.DryRun = dry
	engine.Force = force
	arg := ""
	if len(positionals) > 2 {
		return fmt.Errorf("too many arguments")
	}
	if len(positionals) == 2 {
		arg = positionals[1]
	}
	var result any
	switch command {
	case "validate":
		result = validateResult{Valid: true, Workspace: engine.Root}
	case "list":
		var manifests []*vcm.Manifest
		manifests, err = engine.All()
		if err == nil {
			result = newListResults(manifests, cwd)
		}
	case "status":
		var m *vcm.Manifest
		m, err = engine.Select(arg, cwd)
		if err == nil {
			result = newStatusResult(m, engine.Status(m))
		}
	case "bootstrap", "sync", "create", "merge", "drop":
		var m *vcm.Manifest
		if command == "merge" || command == "drop" {
			m, err = engine.Select(arg, cwd)
			if err != nil {
				return err
			}
		}
		if command == "create" && arg == "" {
			return fmt.Errorf("create requires a slug")
		}
		if dry {
			if command == "create" {
				plan, err := engine.CreatePlan(arg)
				if err != nil {
					return err
				}
				return output(newDryRunResult(plan))
			}
			return output(newDryRunResult(engine.Plan(command, m)))
		}
		err = engine.Mutate(func() error {
			if command == "merge" || command == "drop" {
				m, err = engine.Select(arg, cwd)
				if err != nil {
					return err
				}
			}
			switch command {
			case "bootstrap":
				return engine.Bootstrap()
			case "sync":
				return engine.Sync()
			case "create":
				m, err = engine.Create(arg)
				return err
			case "merge":
				return engine.Merge(m)
			case "drop":
				return engine.Drop(m)
			}
			return nil
		})
		switch command {
		case "create":
			if m != nil {
				result = newCreateResult(m)
			}
		case "merge":
			if m != nil {
				result = newMergeResult(m)
			}
		case "drop":
			if m != nil {
				result = newDropResult(m)
			}
		default:
			result = commandResult{Command: command, Complete: err == nil, Workspace: engine.Root}
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		return err
	}
	return output(result)
}

func requestsJSON(args []string) bool {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" || arg == "-json" {
			jsonOutput = true
			continue
		}
		for _, prefix := range []string{"--json=", "-json="} {
			if strings.HasPrefix(arg, prefix) {
				value, err := strconv.ParseBool(strings.TrimPrefix(arg, prefix))
				if err != nil {
					return true
				}
				jsonOutput = value
			}
		}
	}
	return jsonOutput
}

func runMain(args []string) int {
	if err := run(args); err != nil {
		writeError(os.Stderr, requestsJSON(args), err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(runMain(os.Args[1:]))
}

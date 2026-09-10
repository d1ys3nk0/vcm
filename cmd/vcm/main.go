package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/d1ys3nk0/vcm/internal/vcm"
	"os"
	"strings"
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
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), helpText) }
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
			return nil
		}
		return err
	}
	if len(positionals) == 0 {
		return fmt.Errorf("command required; use --help")
	}
	command := positionals[0]
	output := func(v any) error {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(v)
		}
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			return e
		}
		fmt.Println(string(b))
		return nil
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
	if command == "version" {
		return output(map[string]string{"version": version, "commit": commit})
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
	if force && command != "sync" && command != "drop" {
		return fmt.Errorf("--force is only supported by sync and drop")
	}
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
		result = map[string]any{"valid": true, "workspace": engine.Root}
	case "list":
		result, err = engine.All()
	case "status":
		var m *vcm.Manifest
		m, err = engine.Select(arg, cwd)
		if err == nil {
			result = engine.Status(m)
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
				return output(plan)
			}
			return output(engine.Plan(command, m))
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
		if m != nil {
			result = m
		} else {
			result = map[string]any{"command": command, "complete": err == nil}
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		return err
	}
	return output(result)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vcm:", err)
		os.Exit(1)
	}
}

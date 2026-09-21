package main

import (
	"flag"
	"fmt"
	"github.com/d1ys3nk0/vcm/internal/vcm"
	"io"
	"strings"
)

type commandDefinition struct {
	name, arguments, description string
	options                      []string
	min, max                     int
}

var commands = []commandDefinition{
	{"add", "[change]", "Expand a Change with selected repositories.", []string{"only", "dry-run"}, 0, 1},
	{"diff", "[change]", "Inspect a combined Change diff.", []string{"only", "stat", "committed", "patch"}, 0, 1},
	{"publish", "[change]", "Publish Change branches, children before root.", []string{"dry-run"}, 0, 1},
	{"export", "[change]", "Export a portable JSON Change snapshot.", nil, 0, 1},
	{"restore", "<snapshot-file>", "Restore exact snapshot revisions without hooks.", []string{"name", "fetch", "dry-run"}, 1, 1},
	{"cleanup", "[change]", "Remove retained integrated worktrees and finalize.", []string{"dry-run", "no-cd"}, 0, 1},
	{"create", "[name]", "Create a managed workspace from local trunks or an existing root.", []string{"existing-root", "only", "except", "dry-run", "no-cd"}, 0, 1},
	{"integrate", "<codex|claude|opencode>", "Install or remove project-local harness integration.", []string{"dry-run", "remove"}, 1, 1},
	{"switch", "[change]", "Enter an existing Change or the base workspace.", []string{"base", "no-cd"}, 0, 1},
	{"status", "[change]", "Inspect working trees, divergence, and operation progress.", []string{"verbose"}, 0, 1},
	{"list", "", "List active Changes and their working state.", []string{"all", "verbose"}, 0, 0},
	{"refresh", "[change]", "Merge local trunks into a Change.", []string{"dry-run"}, 0, 1},
	{"merge", "[change]", "Integrate a Change, remove worktrees, and finalize.", []string{"keep", "dry-run", "message", "ignore-hook-failures", "skip-hooks", "skip-hook-git-hooks", "no-cd"}, 0, 1},
	{"drop", "[change]", "Remove owned Change resources.", []string{"dry-run", "force", "f", "no-cd"}, 0, 1},
	{"fetch", "", "Update cached remote trunk refs.", []string{"dry-run"}, 0, 0},
	{"pull", "", "Fetch and rebase canonical local trunks.", []string{"dry-run"}, 0, 0},
	{"push", "", "Publish child trunks, then the root trunk.", []string{"dry-run"}, 0, 0},
	{"init", "", "Create minimal configuration in a canonical Git checkout.", []string{"trunk", "dry-run"}, 0, 0},
	{"bootstrap", "", "Clone missing configured repositories.", []string{"dry-run"}, 0, 0},
	{"check", "", "Audit configuration, ownership, and Git resources.", []string{"config-only"}, 0, 0},
	{"prune", "", "Interactively remove unexpected Git resources.", []string{"dry-run"}, 0, 0},
	{"recover", "[change]", "Authorize retry of an inspected interrupted hook.", []string{"adopt-config", "retry-hook", "acknowledge-effects", "dry-run"}, 0, 1},
	{"path", "[change]", "Print the absolute checkout path.", []string{"base"}, 0, 1},
	{"shell", "init <bash|zsh>", "Print shell integration and completions.", nil, 2, 2},
	{"version", "", "Print version and source commit.", nil, 0, 0},
	{"_integrate-adapter", "<codex|claude|opencode>", "", nil, 1, 1},
	{"_complete", "[kind]", "", nil, 0, 1},
}
var optionDescriptions = map[string]string{
	"name": "SLUG  Name for restored Change", "fetch": "Fetch missing snapshot objects from trusted origins", "stat": "Show tracked diff statistics (default)", "committed": "Compare baseline to HEAD", "patch": "Include tracked patches", "keep": "Retain integrated worktrees for later cleanup", "adopt-config": "Adopt inspected execution configuration without running hooks",
	"workspace": "PATH  Effective workspace context (default: current directory)", "existing-root": "PATH  Adopt a harness-created linked root worktree", "remove": "Remove the exact VCM-managed integration", "json": "Emit structured JSON", "color": "MODE  auto, always, or never", "help": "Show command help",
	"dry-run": "Preview ordered effects and local blockers without mutation", "only": "NAMES  Select exact comma-separated children", "except": "NAMES  Exclude comma-separated children", "message": "SUBJECT  Squash commit subject", "ignore-hook-failures": "Continue after clean lifecycle hook command failures", "skip-hooks": "PHASES  Skip merge-before and/or merge-after", "skip-hook-git-hooks": "Disable Git hooks only inside merge lifecycle hooks", "force": "Back up and discard working content", "f": "Alias for --force", "no-cd": "Do not change the invoking shell directory", "verbose": "Show paths, checkpoints, and recovery detail", "all": "Include completed Changes", "config-only": "Validate configuration without inspecting child checkouts", "base": "Select the canonical workspace", "retry-hook": "KEY  Exact repository/phase/id of an interrupted hook", "acknowledge-effects": "Confirm external hook effects have been inspected", "trunk": "BRANCH  Existing local trunk (default: current branch)",
}

const helpText = "VCM manages one workspace across multiple Git repositories.\n\nUsage: vcm [global options] <command> [arguments]\n"

func commandHelp(command string) string {
	var b strings.Builder
	if command == "" {
		b.WriteString(helpText)
		for i, c := range commands {
			if c.description == "" {
				continue
			}
			if i == 0 {
				b.WriteString("\nEveryday workflow:\n")
			}
			if c.name == "init" {
				b.WriteString("\nSetup and maintenance:\n")
			}
			fmt.Fprintf(&b, "  %-12s %-20s %s\n", c.name, c.arguments, c.description)
		}
	} else {
		for _, c := range commands {
			if c.name == command {
				fmt.Fprintf(&b, "Usage: vcm %s %s\n\n%s\n\nOptions:\n", c.name, c.arguments, c.description)
				for _, o := range c.options {
					prefix := "--"
					if len(o) == 1 {
						prefix = "-"
					}
					fmt.Fprintf(&b, "  %-26s %s\n", prefix+o, optionDescriptions[o])
				}
			}
		}
	}
	b.WriteString("\nGlobal options:\n")
	for _, o := range []string{"workspace", "json", "color", "help"} {
		prefix := "--"
		if len(o) == 1 {
			prefix = "-"
		}
		fmt.Fprintf(&b, "  %-26s %s\n", prefix+o, optionDescriptions[o])
	}
	if command != "" {
		b.WriteString("\nWorkspace selectors accept an exact workspace ID or path. Omitted selectors use\n--workspace, otherwise the current directory. Flags may follow arguments.\n")
	}
	if command == "merge" {
		b.WriteString("\nExample: vcm merge improve-search --message \"feat: improve search\"\nMerge integrates locally, then cleans up and runs finalization hooks.\n--keep retains integrated worktrees until vcm cleanup; cleanup deletes ignored\ncontent without backups. Repair a failure and rerun the same command to resume.\n")
	}
	return b.String()
}

type options struct {
	workspace, existingRoot, only, except, message, skipHooks, retryHook, trunk, name                                                                                    string
	color                                                                                                                                                                colorMode
	stat, committed, patch, keep, adoptConfig, fetch, json, dry, force, remove, skipHookGit, ignoreHookFailures, noCD, verbose, all, configOnly, base, acknowledge, help bool
}

func parseArguments(args []string) (options, string, string, error) {
	o := options{color: colorAuto}
	fs := flag.NewFlagSet("vcm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for name, p := range map[string]*string{"name": &o.name, "workspace": &o.workspace, "existing-root": &o.existingRoot, "only": &o.only, "except": &o.except, "message": &o.message, "skip-hooks": &o.skipHooks, "retry-hook": &o.retryHook, "trunk": &o.trunk} {
		fs.StringVar(p, name, "", optionDescriptions[name])
	}
	for name, p := range map[string]*bool{"stat": &o.stat, "committed": &o.committed, "patch": &o.patch, "keep": &o.keep, "adopt-config": &o.adoptConfig, "fetch": &o.fetch, "json": &o.json, "dry-run": &o.dry, "force": &o.force, "f": &o.force, "remove": &o.remove, "skip-hook-git-hooks": &o.skipHookGit, "ignore-hook-failures": &o.ignoreHookFailures, "no-cd": &o.noCD, "verbose": &o.verbose, "all": &o.all, "config-only": &o.configOnly, "base": &o.base, "acknowledge-effects": &o.acknowledge, "help": &o.help} {
		fs.BoolVar(p, name, false, optionDescriptions[name])
	}
	fs.Var(&o.color, "color", optionDescriptions["color"])
	flags, pos := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
			f := fs.Lookup(name)
			if f != nil && !strings.Contains(arg, "=") {
				if _, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok {
					i++
					if i == len(args) {
						return o, "", "", fmt.Errorf("--%s requires a value", name)
					}
					flags = append(flags, args[i])
				}
			}
		} else {
			pos = append(pos, arg)
		}
	}
	if err := fs.Parse(flags); err != nil {
		return o, "", "", err
	}
	if len(pos) == 0 {
		if o.help {
			return o, "", "", nil
		}
		return o, "", "", fmt.Errorf("command required; run vcm --help")
	}
	command := pos[0]
	var def *commandDefinition
	for i := range commands {
		if commands[i].name == command {
			def = &commands[i]
			break
		}
	}
	if def == nil {
		return o, command, "", fmt.Errorf("unknown command %q", command)
	}
	if o.help {
		return o, command, "", nil
	}
	if len(pos)-1 < def.min || len(pos)-1 > def.max {
		return o, command, "", fmt.Errorf("usage: vcm %s %s", command, def.arguments)
	}
	allowed := map[string]bool{"workspace": true, "json": true, "color": true, "help": true, "h": true}
	for _, name := range def.options {
		allowed[name] = true
	}
	var validation error
	fs.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			validation = fmt.Errorf("--%s is not supported by %s", f.Name, command)
		}
	})
	if validation != nil {
		return o, command, "", validation
	}
	if fs.Lookup("only").Value.String() != "" && fs.Lookup("except").Value.String() != "" {
		return o, command, "", fmt.Errorf("--only and --except are mutually exclusive")
	}
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "only" || f.Name == "except" || f.Name == "skip-hooks") && f.Value.String() == "" {
			validation = fmt.Errorf("--%s requires a nonblank value", f.Name)
		}
	})
	if validation != nil {
		return o, command, "", validation
	}
	arg := ""
	if len(pos) > 1 {
		arg = pos[1]
	}
	if command == "shell" {
		if o.json {
			return o, command, arg, fmt.Errorf("shell init does not support --json")
		}
		if arg != "init" {
			return o, command, "", fmt.Errorf("usage: vcm shell init bash|zsh")
		}
		arg = pos[2]
	}
	if o.base && arg != "" {
		return o, command, arg, fmt.Errorf("--base and a Change selector are mutually exclusive")
	}
	if command == "recover" && o.adoptConfig && (o.retryHook != "" || o.acknowledge) {
		return o, command, arg, fmt.Errorf("--adopt-config is mutually exclusive with hook retry options")
	}
	if command == "restore" && o.name == "" {
		return o, command, arg, fmt.Errorf("restore requires --name")
	}
	if command == "add" && o.only == "" {
		return o, command, arg, fmt.Errorf("add requires --only")
	}
	if command == "recover" && o.retryHook == "" && !o.adoptConfig {
		return o, command, arg, fmt.Errorf("recover requires --retry-hook repository/phase/id")
	}
	if o.skipHooks != "" {
		if _, err := parseMergeHookPhases(o.skipHooks); err != nil {
			return o, command, arg, err
		}
	}
	if o.message != "" {
		if err := vcm.ValidateMergeMessage(o.message); err != nil {
			return o, command, arg, err
		}
	}
	return o, command, arg, nil
}

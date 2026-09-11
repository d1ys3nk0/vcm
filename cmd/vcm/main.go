package main

import (
	"bufio"
	"errors"
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
  check                Audit repository cleanliness, worktrees, and local branches.
  bootstrap            Clone missing configured repositories and verify existing origins.
  sync                 Fast-forward configured workspace and child trunks from origin.
  push                 Validate and publish child trunks, then the workspace root trunk.
  create <slug>        Synchronize origins and create an isolated Change workspace.
  list                 List recorded Changes.
  status [change]      Show a Change's lifecycle, hooks, merge, and recovery state.
  refresh [change]     Merge advanced local trunks into a ready Change.
  merge [change]       Gate, squash-merge, then remove the owned Change worktrees.
  drop [change]        Remove the resources owned by a completed or discarded Change.
  prune                Interactively clean retained checkouts and remove unexpected Git resources.
  version              Print the VCM version and source commit.

Global options:
  --workspace PATH     Workspace root. By default, VCM searches upward from the current
                       directory for vcm.yml at a Git root.
  --json               Emit machine-readable JSON to stdout.
  --color MODE         Color human output: auto, always, or never (default: auto).
  --dry-run            Show the operation plan without changing files or running hooks.
                       Supported by bootstrap, sync, push, create, refresh, merge, drop, and prune.
  -f, --force          For merge, ignore clean lifecycle hook command failures. For sync,
                       reset divergent child trunks after creating recovery backups. For drop,
                       preserve recovery backups before discarding changes.
  --only NAMES         For create, include exactly these comma-separated child repositories.
  --except NAMES       For create, exclude these comma-separated child repositories.
  --message SUBJECT    Merge commit subject; defaults to "feat: <manifest slug>".
  --skip-hooks PHASES  For merge, skip exact comma-separated merge-before and/or merge-after phases.
  --skip-git-hooks     For merge lifecycle hooks, run Git with core.hooksPath=/dev/null.
  -h, --help           Show this help.

Change selection:
  The optional [change] is a managed Change tag or its root workspace path. If it is
  omitted, status, refresh, merge, and drop infer the Change when run anywhere inside
  its managed worktree. Outside a managed Change worktree, [change] is required.

Examples:
  vcm validate
  vcm bootstrap --workspace /work/product
  vcm create improve-search
  vcm create improve-search --only core,web
  vcm create improve-search --except devtools
  vcm status 260910120000-improve-search
  vcm check
  vcm push --dry-run
  vcm push
  vcm prune --dry-run
  vcm prune
  vcm refresh 260910120000-improve-search
  vcm merge --dry-run --force --skip-hooks merge-after --skip-git-hooks
  vcm drop 260910120000-improve-search --force

Notes:
  Flags may appear before or after the command. Slugs use lowercase kebab-case letters
  and digits. Commands that change state should be previewed with --dry-run; merge does
  not fetch, pull, or push. After local integration is checkpointed, merge deletes ignored
  content with the owned worktrees without requiring --force or creating recovery backups.
`

var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
var commandInput io.Reader = os.Stdin

type incompleteResultError struct{ command string }

func (e incompleteResultError) Error() string {
	return e.command + " found unresolved workspace issues"
}

func run(args []string) error {
	flags := flag.NewFlagSet("vcm", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	workspace, only, except, message, skipHooks := "", "", "", "", ""
	color := colorAuto
	jsonOutput, dry, force, skipGitHooks := false, false, false, false
	flags.StringVar(&workspace, "workspace", "", "workspace root")
	flags.BoolVar(&jsonOutput, "json", false, "machine-readable output")
	flags.Var(&color, "color", "color human output: auto, always, or never")
	flags.BoolVar(&dry, "dry-run", false, "show operation plan")
	flags.BoolVar(&force, "force", false, "preserve recovery backups and discard changes")
	flags.BoolVar(&force, "f", false, "force the selected operation")
	flags.StringVar(&only, "only", "", "selected child repositories")
	flags.StringVar(&except, "except", "", "excluded child repositories")
	flags.StringVar(&message, "message", "", "merge commit subject")
	flags.StringVar(&skipHooks, "skip-hooks", "", "merge hook phases to skip")
	flags.BoolVar(&skipGitHooks, "skip-git-hooks", false, "suppress Git hooks inside merge lifecycle hooks")
	// Permit global options before or after the command using the standard flag parser.
	options := []string{}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			options = append(options, args[i])
			if args[i] == "--workspace" || args[i] == "-workspace" || args[i] == "--color" || args[i] == "-color" || args[i] == "--only" || args[i] == "-only" || args[i] == "--except" || args[i] == "-except" || args[i] == "--message" || args[i] == "-message" || args[i] == "--skip-hooks" || args[i] == "-skip-hooks" {
				i++
				if i == len(args) {
					return fmt.Errorf("%s requires a value", options[len(options)-1])
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
	stdoutStyle := humanStyle{enabled: colorEnabled(color, stdoutIsTerminal(), jsonOutput)}
	stderrStyle := humanStyle{enabled: colorEnabled(color, stderrIsTerminal(), jsonOutput)}
	output := func(result any) error {
		if jsonOutput {
			return renderJSON(os.Stdout, result)
		}
		return renderHumanStyled(os.Stdout, result, stdoutStyle)
	}
	if len(positionals) > 2 {
		return fmt.Errorf("too many arguments")
	}
	if command != "create" && command != "refresh" && command != "merge" && command != "drop" && command != "status" && len(positionals) > 1 {
		return fmt.Errorf("%s takes no arguments", command)
	}
	selectionFlags := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { selectionFlags[f.Name] = true })
	forceSet := selectionFlags["force"] || selectionFlags["f"]
	if forceSet && command != "sync" && command != "merge" && command != "drop" {
		return fmt.Errorf("--force is only supported by sync, merge, and drop")
	}
	if (selectionFlags["only"] || selectionFlags["except"]) && command != "create" {
		return fmt.Errorf("--only and --except are only supported by create")
	}
	if selectionFlags["only"] && selectionFlags["except"] {
		return fmt.Errorf("--only and --except are mutually exclusive")
	}
	if selectionFlags["message"] && command != "merge" {
		return fmt.Errorf("--message is only supported by merge")
	}
	if selectionFlags["skip-hooks"] && command != "merge" {
		return fmt.Errorf("--skip-hooks is only supported by merge")
	}
	if selectionFlags["skip-git-hooks"] && command != "merge" {
		return fmt.Errorf("--skip-git-hooks is only supported by merge")
	}
	if selectionFlags["only"] && only == "" {
		return fmt.Errorf("--only contains a blank repository name")
	}
	if selectionFlags["except"] && except == "" {
		return fmt.Errorf("--except contains a blank repository name")
	}
	if dry && command != "bootstrap" && command != "sync" && command != "push" && command != "create" && command != "refresh" && command != "merge" && command != "drop" && command != "prune" {
		return fmt.Errorf("--dry-run is only supported by bootstrap, sync, push, create, refresh, merge, drop, and prune")
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
	engine.Style = func(semantic vcm.LogSemantic, text string) string {
		color := semanticNone
		switch semantic {
		case vcm.LogContext:
			color = semanticCyanBold
		case vcm.LogChanged, vcm.LogWarning:
			color = semanticYellow
		case vcm.LogSuccess:
			color = semanticGreen
		case vcm.LogFailure:
			color = semanticRed
		}
		return stderrStyle.paint(color, text)
	}
	engine.Force = force && command != "merge"
	engine.MergeForce = force && command == "merge"
	engine.SkipGitHooks = skipGitHooks
	if selectionFlags["skip-hooks"] {
		phases, parseErr := parseMergeHookPhases(skipHooks)
		if parseErr != nil {
			return parseErr
		}
		engine.SkipMergeHooks = phases
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
	case "check":
		var report vcm.AuditReport
		report, err = engine.Check()
		if err == nil {
			result = report
		}
	case "prune":
		var report vcm.PruneReport
		if dry {
			report, err = engine.PruneDryRun()
		} else {
			if !stdinIsTerminal() {
				return fmt.Errorf("prune requires an interactive terminal; use --dry-run for a non-interactive audit")
			}
			reader := bufio.NewReader(commandInput)
			confirm := func(action vcm.PruneAction) bool {
				fmt.Fprintf(os.Stderr, "%s %s in repository %s? [y/N] ", stderrStyle.paint(semanticYellow, pruneActionPrompt(action.Action)), action.Target, action.Repository)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(answer)
				return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
			}
			err = engine.Mutate(func() error {
				var pruneErr error
				report, pruneErr = engine.Prune(confirm)
				return pruneErr
			})
		}
		if err == nil {
			result = report
		}
	case "bootstrap", "sync", "push", "create", "refresh", "merge", "drop":
		var m *vcm.Manifest
		if command == "refresh" || command == "merge" || command == "drop" {
			m, err = engine.Select(arg, cwd)
			if err != nil {
				return err
			}
		}
		if command == "merge" {
			if message != "" {
				if err := vcm.ValidateMergeMessage(message); err != nil {
					return err
				}
			}
			if m.MergeMessage != "" && message != "" && message != m.MergeMessage {
				return fmt.Errorf("merge already started with message %q; retries must reuse it", m.MergeMessage)
			}
		}
		if command == "create" && arg == "" {
			return fmt.Errorf("create requires a slug")
		}
		if dry {
			if command == "create" {
				plan, err := engine.CreatePlanSelected(arg, only, except)
				if err != nil {
					return err
				}
				return output(newDryRunResult(plan))
			}
			return output(newDryRunResult(engine.Plan(command, m)))
		}
		err = engine.Mutate(func() error {
			if command == "refresh" || command == "merge" || command == "drop" {
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
			case "push":
				return engine.Push()
			case "create":
				m, err = engine.CreateSelected(arg, only, except)
				return err
			case "refresh":
				return engine.Refresh(m)
			case "merge":
				return engine.Merge(m, message)
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
		case "refresh":
			if m != nil {
				result = newRefreshResult(m)
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
	if err = output(result); err != nil {
		return err
	}
	switch value := result.(type) {
	case vcm.AuditReport:
		if !value.Clean {
			return incompleteResultError{command: "check"}
		}
	case vcm.PruneReport:
		if !value.Complete {
			return incompleteResultError{command: "prune"}
		}
	}
	return nil
}

func parseMergeHookPhases(value string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, phase := range strings.Split(value, ",") {
		if phase == "" || phase != strings.TrimSpace(phase) {
			return nil, fmt.Errorf("--skip-hooks contains a blank or whitespace-padded phase")
		}
		if phase != vcm.HookMergeBefore && phase != vcm.HookMergeAfter {
			return nil, fmt.Errorf("--skip-hooks contains unsupported phase %q", phase)
		}
		if result[phase] {
			return nil, fmt.Errorf("--skip-hooks contains duplicate phase %s", phase)
		}
		result[phase] = true
	}
	return result, nil
}

func pruneActionPrompt(action string) string {
	switch action {
	case vcm.PruneReset:
		return "Discard tracked and untracked changes from"
	case vcm.PruneRemoveWorktree:
		return "Delete unexpected worktree"
	case vcm.PruneDeleteBranch:
		return "Delete unexpected branch"
	default:
		return "Apply prune action to"
	}
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
		var incomplete incompleteResultError
		if errors.As(err, &incomplete) {
			return 1
		}
		jsonOutput := requestsJSON(args)
		style := humanStyle{enabled: colorEnabled(parseRequestedColorMode(args), stderrIsTerminal(), jsonOutput)}
		writeErrorStyled(os.Stderr, jsonOutput, err, style)
		return 1
	}
	return 0
}

func main() {
	os.Exit(runMain(os.Args[1:]))
}

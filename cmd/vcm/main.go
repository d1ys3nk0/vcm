package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

var version = "0.0.1-dev"
var commit = "unknown"

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
	opts, command, arg, err := parseArguments(args)
	if err != nil {
		return err
	}
	if opts.help {
		fmt.Fprint(os.Stderr, commandHelp(command))
		return nil
	}
	workspace, only, except, message, skipHooks := opts.workspace, opts.only, opts.except, opts.message, opts.skipHooks
	color, jsonOutput, dry, force, skipGitHooks := opts.color, opts.json, opts.dry, opts.force, opts.skipHookGit
	stdoutStyle := humanStyle{enabled: colorEnabled(color, stdoutIsTerminal(), jsonOutput)}
	stderrStyle := humanStyle{enabled: colorEnabled(color, stderrIsTerminal(), jsonOutput)}
	output := func(result any) error {
		if jsonOutput {
			return renderJSON(os.Stdout, result)
		}
		return renderHumanStyled(os.Stdout, result, stdoutStyle)
	}
	if command == "version" {
		return output(versionResult{Version: version, Commit: commit})
	}
	cwd, err := os.Getwd()
	if err != nil && workspace == "" {
		return err
	}
	if workspace == "" {
		workspace = cwd
	}
	if command == "shell" {
		return printShell(arg)
	}
	if command == "init" {
		result, err := vcm.Init(workspace, opts.trunk, dry)
		if err != nil {
			return err
		}
		return output(result)
	}
	if command == "_complete" && (arg == "commands" || strings.HasPrefix(arg, "flags:")) {
		return complete(nil, arg)
	}
	if command == "_integrate-adapter" {
		return runIntegrationAdapter(arg, opts.adapterPath)
	}
	engine, err := vcm.Open(workspace, os.Stderr)
	if err != nil {
		return &vcm.Error{Code: "configuration", Err: err}
	}
	engine.DryRun = dry
	engine.Keep = opts.keep
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
	engine.IgnoreHookFailures = opts.ignoreHookFailures
	engine.SkipHookGitHooks = skipGitHooks
	if command == "integrate" {
		result, integrateErr := integrateHarness(engine.Root, arg, opts.remove, dry)
		if integrateErr != nil {
			return integrateErr
		}
		return output(result)
	}
	if skipHooks != "" {
		phases, parseErr := parseMergeHookPhases(skipHooks)
		if parseErr != nil {
			return parseErr
		}
		engine.SkipMergeHooks = phases
	}
	if handled, err := runExtended(engine, command, arg, workspace, cwd, opts, output); handled {
		return err
	}
	var result any
	switch command {
	case "_complete":
		return complete(engine, arg)
	case "path", "switch":
		path, pathErr := engine.Path(arg, workspace, opts.base)
		if pathErr != nil {
			return pathErr
		}
		if command == "switch" && !opts.noCD && !jsonOutput {
			if err := navigate(path); err != nil {
				return err
			}
		}
		if jsonOutput {
			return output(pathResult{Path: path})
		}
		fmt.Fprintln(os.Stdout, path)
		return nil
	case "recover":
		if opts.adoptConfig {
			result, err = engine.AdoptConfiguration(arg, workspace)
		} else {
			result, err = engine.Recover(arg, workspace, opts.retryHook, opts.acknowledge)
		}
	case "list":
		result, err = inspectList(engine, workspace, opts.all, opts.verbose)
	case "status":
		result, err = inspectStatus(engine, arg, workspace, opts.verbose)
	case "check":
		if opts.configOnly {
			result = validateResult{Valid: true, Workspace: engine.Root}
		} else {
			result, err = engine.Check()
		}
	case "prune":
		var report vcm.PruneReport
		if dry {
			report, err = engine.PruneDryRun()
		} else {
			preview, previewErr := engine.PruneDryRun()
			if previewErr != nil {
				return previewErr
			}
			for _, issue := range preview.RemainingIssues {
				if issue.Kind == vcm.IssueInspectionError {
					if outputErr := output(preview); outputErr != nil {
						return outputErr
					}
					return incompleteResultError{command: "prune"}
				}
			}
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
	case "bootstrap", "fetch", "pull", "push", "create", "refresh", "merge", "drop":
		var m *vcm.Manifest
		selector := arg
		if command == "refresh" || command == "merge" || command == "drop" {
			m, err = engine.Select(selector, workspace)
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
		if command == "create" && arg == "" && opts.existingRoot == "" {
			return fmt.Errorf("create requires a workspace name unless --existing-root is supplied")
		}
		if dry {
			if command == "create" {
				if opts.existingRoot != "" {
					return fmt.Errorf("--dry-run with --existing-root is not supported")
				}
				plan, err := engine.CreatePlanSelected(arg, only, except)
				if err != nil {
					return err
				}
				if err := output(newDryRunResult(plan)); err != nil {
					return err
				}
				if len(plan.Blockers) > 0 {
					return incompleteResultError{command: command}
				}
				return nil
			}
			plan := engine.Plan(command, m)
			if err := output(newDryRunResult(plan)); err != nil {
				return err
			}
			if len(plan.Blockers) > 0 {
				return incompleteResultError{command: command}
			}
			return nil
		}
		err = engine.Mutate(func() error {
			if command == "refresh" || command == "merge" || command == "drop" {
				m, err = engine.Select(selector, workspace)
				if err != nil {
					return err
				}
			}
			switch command {
			case "bootstrap":
				return engine.Bootstrap()
			case "fetch":
				return engine.Fetch()
			case "pull":
				return engine.Pull()
			case "push":
				return engine.Push()
			case "create":
				if opts.existingRoot != "" {
					m, err = engine.CreateExisting(arg, opts.existingRoot, only, except)
				} else {
					m, err = engine.CreateSelected(arg, only, except)
				}
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
		if !opts.noCD && !jsonOutput {
			if command == "create" && err == nil && m != nil {
				if navErr := navigate(m.Workspace); navErr != nil {
					return navErr
				}
			}
			if (command == "merge" || command == "drop") && m != nil && withinPath(cwd, m.Workspace) {
				if _, statErr := os.Stat(cwd); os.IsNotExist(statErr) {
					if navErr := navigate(engine.Root); navErr != nil && err == nil {
						err = navErr
					}
				}
			}
		}
		if err != nil {
			return operationFailure(command, m, err)
		}
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
	case inspectionResult:
		if inspectionFailed(value.Repositories) {
			return incompleteResultError{command: "status"}
		}
	case overviewResult:
		for _, c := range value.Workspaces {
			if inspectionFailed(c.Inspection) {
				return incompleteResultError{command: "list"}
			}
		}
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
		if arg == "--" {
			break
		}
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

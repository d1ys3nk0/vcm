package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

const ansiEscape = "\x1b["

func withoutANSI(text string) string {
	for _, sequence := range []string{"\x1b[1;36m", "\x1b[33m", "\x1b[32m", "\x1b[31m", "\x1b[0m"} {
		text = strings.ReplaceAll(text, sequence, "")
	}
	return text
}

func TestColorModeValidationAndPlacement(t *testing.T) {
	root := cliWorkspace(t)
	for _, args := range [][]string{
		{"--color", "always", "validate", "--workspace", root},
		{"validate", "--color", "always", "--workspace", root},
		{"validate", "--color=always", "--workspace", root},
	} {
		output, err := runOutput(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(output, ansiEscape) {
			t.Fatalf("%v did not enable color: %q", args, output)
		}
	}
	for _, value := range []string{"", "ALWAYS", "yes", "automatic"} {
		if _, err := runOutput(t, "version", "--color="+value); err == nil {
			t.Fatalf("invalid color mode %q accepted", value)
		}
	}
}

func TestColorAutoDetectionAndOverrides(t *testing.T) {
	root := cliWorkspace(t)
	oldStdoutTerminal := stdoutIsTerminal
	defer func() { stdoutIsTerminal = oldStdoutTerminal }()
	stdoutIsTerminal = func() bool { return true }
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")

	output, err := runOutput(t, "validate", "--workspace", root)
	if err != nil || !strings.Contains(output, ansiEscape) {
		t.Fatalf("auto terminal output = %q, %v", output, err)
	}
	for name, environment := range map[string]map[string]string{
		"dumb terminal": {"TERM": "dumb", "NO_COLOR": ""},
		"NO_COLOR":      {"TERM": "xterm-256color", "NO_COLOR": "1"},
	} {
		t.Run(name, func(t *testing.T) {
			for key, value := range environment {
				t.Setenv(key, value)
			}
			plain, runErr := runOutput(t, "validate", "--workspace", root)
			if runErr != nil || strings.Contains(plain, ansiEscape) {
				t.Fatalf("auto output = %q, %v", plain, runErr)
			}
			forced, runErr := runOutput(t, "validate", "--workspace", root, "--color=always")
			if runErr != nil || !strings.Contains(forced, ansiEscape) {
				t.Fatalf("always output = %q, %v", forced, runErr)
			}
		})
	}
	stdoutIsTerminal = func() bool { return false }
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	plain, err := runOutput(t, "validate", "--workspace", root)
	if err != nil || strings.Contains(plain, ansiEscape) {
		t.Fatalf("redirected auto output = %q, %v", plain, err)
	}
	forced, err := runOutput(t, "validate", "--workspace", root, "--color=always")
	if err != nil || !strings.Contains(forced, ansiEscape) {
		t.Fatalf("redirected always output = %q, %v", forced, err)
	}
	never, err := runOutput(t, "validate", "--workspace", root, "--color=never")
	if err != nil || strings.Contains(never, ansiEscape) {
		t.Fatalf("never output = %q, %v", never, err)
	}
}

func TestJSONSuppressesColorOnBothStreams(t *testing.T) {
	stdout, err := runOutput(t, "version", "--json", "--color=always")
	if err != nil || strings.Contains(stdout, ansiEscape) {
		t.Fatalf("JSON stdout = %q, %v", stdout, err)
	}
	var payload versionResult
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	reader, writer, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = writer
	exitCode := runMain([]string{"version", "--json", "--color=always", "--unknown"})
	os.Stderr = oldStderr
	_ = writer.Close()
	var stderr bytes.Buffer
	_, _ = stderr.ReadFrom(reader)
	_ = reader.Close()
	if exitCode == 0 || strings.Contains(stderr.String(), ansiEscape) {
		t.Fatalf("JSON stderr = %q, exit %d", stderr.String(), exitCode)
	}
	var errorPayload errorResult
	if err := json.Unmarshal(stderr.Bytes(), &errorPayload); err != nil {
		t.Fatal(err)
	}
}

func TestStdoutAndStderrResolveColorIndependentlyAndHelpStaysPlain(t *testing.T) {
	oldStdoutTerminal, oldStderrTerminal := stdoutIsTerminal, stderrIsTerminal
	defer func() { stdoutIsTerminal, stderrIsTerminal = oldStdoutTerminal, oldStderrTerminal }()
	stdoutIsTerminal = func() bool { return false }
	stderrIsTerminal = func() bool { return true }
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")

	stdout, err := runOutput(t, "version")
	if err != nil || strings.Contains(stdout, ansiEscape) {
		t.Fatalf("redirected stdout = %q, %v", stdout, err)
	}
	stderr, runErr := runErrorOutput(t, "unknown")
	if runErr == nil {
		t.Fatal("unknown command succeeded")
	}
	// run returns errors to runMain, so exercise the styled error boundary directly.
	var styled bytes.Buffer
	writeErrorStyled(&styled, false, runErr, humanStyle{enabled: colorEnabled(colorAuto, stderrIsTerminal(), false)})
	if !strings.HasPrefix(styled.String(), "\x1b[31mvcm:\x1b[0m ") {
		t.Fatalf("terminal stderr error = %q (captured run stderr %q)", styled.String(), stderr)
	}

	help, err := runErrorOutput(t, "--color=always", "--help")
	if err != nil || strings.Contains(help, ansiEscape) {
		t.Fatalf("help output = %q, %v", help, err)
	}
}

func TestColoredPresentationPreservesPlainTextAndTableAlignment(t *testing.T) {
	report := vcm.AuditReport{
		Repositories: []vcm.AuditRepository{{Name: "root", Clean: false, Origin: "/work/root"}, {Name: "long-repository", Clean: true, Origin: "/work/child"}},
		Issues:       []vcm.AuditIssue{{Repository: "root", Kind: "dirty", Path: "/work/root", Detail: "details remain plain"}},
	}
	var plain, colored bytes.Buffer
	if err := renderHuman(&plain, report); err != nil {
		t.Fatal(err)
	}
	if err := renderHumanStyled(&colored, report, humanStyle{enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(colored.String(), "\x1b[1;36mRepository") || !strings.Contains(colored.String(), "\x1b[31missues") || !strings.Contains(colored.String(), "\x1b[32mclean") {
		t.Fatalf("palette missing from report:\n%s", colored.String())
	}
	if strings.Contains(colored.String(), "\xff") {
		t.Fatalf("tabwriter escape marker leaked: %q", colored.String())
	}
	if got := withoutANSI(colored.String()); got != plain.String() {
		t.Fatalf("stripped colored output differs:\ngot  %q\nwant %q", got, plain.String())
	}
}

func TestPrunePromptAndLogUseSemanticColor(t *testing.T) {
	root := cliCleanWorkspace(t)
	if output, err := exec.Command("git", "-C", root, "branch", "unexpected").CombinedOutput(); err != nil {
		t.Fatalf("create branch: %s: %v", output, err)
	}
	oldInput, oldTerminal := commandInput, stdinIsTerminal
	defer func() { commandInput, stdinIsTerminal = oldInput, oldTerminal }()
	commandInput = strings.NewReader("n\n")
	stdinIsTerminal = func() bool { return true }

	stdout, stderr, err := runCapturedOutput(t, "prune", "--color=always", "--workspace", root)
	if err == nil {
		t.Fatal("declined prune unexpectedly completed")
	}
	if !strings.Contains(stderr, "\x1b[33mDelete unexpected branch\x1b[0m unexpected") {
		t.Fatalf("prompt lacks warning color or colors target:\n%s", stderr)
	}
	if !strings.Contains(stderr, "\x1b[33mdeclined\x1b[0m") || strings.Contains(stderr, "\x1b[33munexpected\x1b[0m") {
		t.Fatalf("prune log did not isolate outcome from target:\n%s", stderr)
	}
	if !strings.Contains(stdout, "\x1b[33mdeclined\x1b[0m") {
		t.Fatalf("prune report lacks declined status color:\n%s", stdout)
	}
}

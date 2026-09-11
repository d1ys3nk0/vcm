package main

import (
	"fmt"
	"os"
)

type colorMode string

const (
	colorAuto   colorMode = "auto"
	colorAlways colorMode = "always"
	colorNever  colorMode = "never"
)

func (m *colorMode) String() string { return string(*m) }

func (m *colorMode) Set(value string) error {
	switch colorMode(value) {
	case colorAuto, colorAlways, colorNever:
		*m = colorMode(value)
		return nil
	default:
		return fmt.Errorf("must be auto, always, or never")
	}
}

var stdoutIsTerminal = func() bool { return fileIsTerminal(os.Stdout) }
var stderrIsTerminal = func() bool { return fileIsTerminal(os.Stderr) }

func fileIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func colorEnabled(mode colorMode, terminal, jsonOutput bool) bool {
	if jsonOutput || mode == colorNever {
		return false
	}
	if mode == colorAlways {
		return true
	}
	return terminal && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
}

type semanticColor uint8

const (
	semanticNone semanticColor = iota
	semanticYellow
	semanticGreen
	semanticRed
	semanticCyanBold
)

type humanStyle struct{ enabled bool }

func (s humanStyle) paint(color semanticColor, text string) string {
	if !s.enabled || color == semanticNone || text == "" {
		return text
	}
	code := ""
	switch color {
	case semanticYellow:
		code = "33"
	case semanticGreen:
		code = "32"
	case semanticRed:
		code = "31"
	case semanticCyanBold:
		code = "1;36"
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s humanStyle) tablePaint(color semanticColor, text string) string {
	return s.paint(color, text)
}

func statusColor(status string) semanticColor {
	switch status {
	case "changed", "target changed", "dirty", "create", "refresh", "merge", "remove", "creating", "refreshing", "merging", "merge-finalizing", "dropping", "running", "warning", "blocked", "declined":
		return semanticYellow
	case "clean", "unchanged", "skipped", "complete", "completed", "success", "ready", "merged", "removed", "dropped":
		return semanticGreen
	case "failed", "error", "issues", "findings":
		return semanticRed
	default:
		return semanticNone
	}
}

func parseRequestedColorMode(args []string) colorMode {
	mode := colorAuto
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--color" || arg == "-color" {
			if i+1 < len(args) {
				_ = mode.Set(args[i+1])
				i++
			}
			continue
		}
		for _, prefix := range []string{"--color=", "-color="} {
			if len(arg) >= len(prefix) && arg[:len(prefix)] == prefix {
				_ = mode.Set(arg[len(prefix):])
			}
		}
	}
	return mode
}

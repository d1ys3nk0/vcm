package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

func withinPath(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
func navigate(path string) error {
	destination := os.Getenv("VCM_CD_FILE")
	if destination == "" {
		fmt.Fprintln(os.Stderr, "Shell navigation: enable with eval \"$(vcm shell init bash)\" (or zsh).")
		return nil
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Nlink != 1 {
		return fmt.Errorf("unsafe shell navigation file")
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	_, err = f.WriteString(path + "\n")
	return err
}
func printShell(shell string) error {
	if shell != "bash" && shell != "zsh" {
		return fmt.Errorf("supported shells: bash, zsh")
	}
	fmt.Fprint(os.Stdout, `vcm() {
    local vcm_cd_file vcm_exit_code=0 vcm_destination
    vcm_cd_file=$(mktemp "${TMPDIR:-/tmp}/vcm-cd.XXXXXXXX") || return
    VCM_CD_FILE="$vcm_cd_file" command vcm "$@" || vcm_exit_code=$?
    if [ -s "$vcm_cd_file" ]; then
        IFS= read -r vcm_destination < "$vcm_cd_file"
        cd -- "$vcm_destination" || { [ "$vcm_exit_code" -ne 0 ] || vcm_exit_code=1; }
    fi
    rm -f -- "$vcm_cd_file"
    return "$vcm_exit_code"
}
`)
	if shell == "bash" {
		fmt.Fprint(os.Stdout, `_vcm_complete() {
    local vcm_word="${COMP_WORDS[COMP_CWORD]}" vcm_previous="${COMP_WORDS[COMP_CWORD-1]}" vcm_workspace="" vcm_i vcm_kind="changes" vcm_prefix="" vcm_item vcm_command=""
    local -a vcm_context=()
    for ((vcm_i=1; vcm_i<COMP_CWORD; vcm_i++)); do
        case "${COMP_WORDS[vcm_i]}" in
            --workspace) vcm_workspace="${COMP_WORDS[vcm_i+1]}"; ((vcm_i++)) ;;
            --color) ((vcm_i++)) ;;
            --workspace=*) vcm_workspace="${COMP_WORDS[vcm_i]#*=}" ;;
            -*) ;;
            *) [[ -z "$vcm_command" ]] && vcm_command="${COMP_WORDS[vcm_i]}" ;;
        esac
    done
    [[ -n "$vcm_workspace" ]] && vcm_context=(--workspace "$vcm_workspace")
    if [[ "$vcm_previous" == --only || "$vcm_previous" == --except || "$vcm_word" == --only=* || "$vcm_word" == --except=* ]]; then
        vcm_kind=repositories
        [[ "$vcm_word" == *=* ]] && vcm_prefix="${vcm_word%%=*}=" && vcm_word="${vcm_word#*=}"
        [[ "$vcm_word" == *,* ]] && vcm_prefix="$vcm_prefix${vcm_word%,*}," && vcm_word="${vcm_word##*,}"
    elif [[ "$vcm_word" == -* ]]; then
        vcm_kind="flags:$vcm_command"
    elif [[ -z "$vcm_command" ]]; then vcm_kind=commands
    fi
    COMPREPLY=()
    while IFS= read -r vcm_item; do
        [[ "$vcm_item" == "$vcm_word"* ]] && COMPREPLY+=("$vcm_prefix$vcm_item")
    done < <(command vcm "${vcm_context[@]}" _complete "$vcm_kind" 2>/dev/null)
}
complete -F _vcm_complete vcm
`)
	} else {
		fmt.Fprint(os.Stdout, `_vcm_complete() {
    local vcm_word="${words[CURRENT]}" vcm_previous="${words[CURRENT-1]}" vcm_workspace="" vcm_i vcm_kind=changes vcm_prefix="" vcm_command=""
    local -a vcm_values vcm_context
    for ((vcm_i=2; vcm_i<CURRENT; vcm_i++)); do
        case "${words[vcm_i]}" in
            --workspace) vcm_workspace="${words[vcm_i+1]}"; ((vcm_i++)) ;;
            --color) ((vcm_i++)) ;;
            --workspace=*) vcm_workspace="${words[vcm_i]#*=}" ;;
            -*) ;;
            *) [[ -z "$vcm_command" ]] && vcm_command="${words[vcm_i]}" ;;
        esac
    done
    [[ -n "$vcm_workspace" ]] && vcm_context=(--workspace "$vcm_workspace")
    if [[ "$vcm_previous" == --only || "$vcm_previous" == --except || "$vcm_word" == --only=* || "$vcm_word" == --except=* ]]; then
        vcm_kind=repositories
        [[ "$vcm_word" == *=* ]] && vcm_prefix="${vcm_word%%=*}=" && vcm_word="${vcm_word#*=}"
        [[ "$vcm_word" == *,* ]] && vcm_prefix="$vcm_prefix${vcm_word%,*},"
    elif [[ "$vcm_word" == -* ]]; then vcm_kind="flags:$vcm_command"
    elif [[ -z "$vcm_command" ]]; then vcm_kind=commands
    fi
    vcm_values=("${(@f)$(command vcm "${vcm_context[@]}" _complete "$vcm_kind" 2>/dev/null)}")
    [[ -n "$vcm_prefix" ]] && compset -P "$vcm_prefix"
    compadd -- "${vcm_values[@]}"
}
if (( $+functions[compdef] )); then compdef _vcm_complete vcm; fi
`)
	}
	return nil
}
func complete(engine *vcm.Engine, kind string) error {
	switch {
	case kind == "commands":
		for _, c := range commands {
			if c.description != "" {
				fmt.Fprintln(os.Stdout, c.name)
			}
		}
	case strings.HasPrefix(kind, "flags:"):
		if kind == "flags:" {
			for _, f := range []string{"workspace", "json", "color", "help"} {
				fmt.Fprintln(os.Stdout, "--"+f)
			}
			return nil
		}
		for _, c := range commands {
			if c.name == strings.TrimPrefix(kind, "flags:") {
				for _, f := range append([]string{"workspace", "json", "color", "help"}, c.options...) {
					if f == "f" {
						fmt.Fprintln(os.Stdout, "-f")
					} else {
						fmt.Fprintln(os.Stdout, "--"+f)
					}
				}
			}
		}
	case kind == "repositories":
		for _, r := range engine.Config.Children {
			fmt.Fprintln(os.Stdout, r.Name)
		}
	default:
		all, err := engine.All()
		if err != nil {
			return err
		}
		for _, m := range all {
			fmt.Fprintln(os.Stdout, m.Slug)
			fmt.Fprintln(os.Stdout, m.Tag)
		}
	}
	return nil
}

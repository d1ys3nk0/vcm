package vcm

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw preserves NUL-delimited path output, including leading whitespace.
func gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	if e := cmd.Run(); e != nil {
		return "", fmt.Errorf("%s: %w", redactURLCredentials(fmt.Sprintf("git %s in %s: %s", strings.Join(args, " "), dir, strings.TrimSpace(errout.String()))), e)
	}
	return out.String(), nil
}

var urlCredentials = regexp.MustCompile(`([[:alpha:]][[:alnum:]+.-]*://)[^/\s]*@`)

func redactURLCredentials(s string) string {
	return urlCredentials.ReplaceAllString(s, "${1}[redacted]@")
}
func clean(path string) error {
	s, e := git(path, "status", "--porcelain", "--untracked-files=all")
	if e != nil {
		return e
	}
	if s != "" {
		return fmt.Errorf("dirty checkout %s; commit or preserve changes before retry", path)
	}
	for _, p := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		v, e := git(path, "rev-parse", "--git-path", p)
		if e == nil {
			if !filepath.IsAbs(v) {
				v = filepath.Join(path, v)
			}
			if _, e = os.Stat(v); e == nil {
				return fmt.Errorf("unfinished Git operation in %s; resolve it before retry", path)
			}
		}
	}
	return nil
}
func head(path string) (string, error)   { return git(path, "rev-parse", "HEAD") }
func branch(path string) (string, error) { return git(path, "symbolic-ref", "--short", "HEAD") }
func ancestor(path, a, b string) bool {
	_, e := git(path, "merge-base", "--is-ancestor", a, b)
	return e == nil
}
func discover(start string) (string, error) {
	p, e := filepath.Abs(start)
	if e != nil {
		return "", e
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return "", e
	}
	for {
		if _, e := os.Stat(filepath.Join(p, "vcm.yml")); e == nil {
			top, e := git(p, "rev-parse", "--show-toplevel")
			if e != nil {
				return "", e
			}
			if top != p {
				return "", fmt.Errorf("vcm.yml must be at Git root")
			}
			return p, nil
		}
		next := filepath.Dir(p)
		if next == p {
			return "", fmt.Errorf("vcm.yml not found above %s", start)
		}
		p = next
	}
}
func commonDir(root string) (string, error) {
	p, e := git(root, "rev-parse", "--git-common-dir")
	if e != nil {
		return "", e
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return filepath.Clean(p), nil
}
func originRoot(root string) (string, error) {
	common, e := commonDir(root)
	if e != nil {
		return "", e
	}
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("workspace requires a non-bare primary checkout")
	}
	return filepath.Dir(common), nil
}
func validateOrigin(path string, r Repository) error {
	top, e := git(path, "rev-parse", "--show-toplevel")
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	if top != actual {
		return fmt.Errorf("repository %s: path is not a Git checkout root", r.Name)
	}
	u, e := git(path, "remote", "get-url", "origin")
	if e != nil {
		return e
	}
	if u != r.URL {
		return fmt.Errorf("repository %s: origin mismatch: got %q, expected %q", r.Name, redactURLCredentials(u), redactURLCredentials(r.URL))
	}
	return nil
}

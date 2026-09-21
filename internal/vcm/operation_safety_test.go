package vcm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitErrorsRedactURLCredentials(t *testing.T) {
	e := fixture(t, 0)
	for _, remote := range []string{"https://secret-user:secret-password@example.invalid/project.git", "https://secret-token@example.invalid/project.git"} {
		// Disable the transport so clone fails locally, without contacting a server.
		_, err := git(e.Root, "-c", "protocol.https.allow=never", "clone", remote, filepath.Join(t.TempDir(), "clone"))
		if err == nil || strings.Contains(err.Error(), "secret-") || !strings.Contains(err.Error(), "example.invalid/project.git") {
			t.Fatalf("unsafe or unhelpful clone error: %v", err)
		}
	}
	// A local Git alias supplies stderr independently of the command arguments.
	mustGit(t, e.Root, "config", "alias.report-error", "!printf '%s\\n' 'https://stderr-user:stderr-password@example.invalid/failure.git' >&2; exit 1")
	_, stderrErr := git(e.Root, "report-error")
	if stderrErr == nil || strings.Contains(stderrErr.Error(), "stderr-user") || strings.Contains(stderrErr.Error(), "stderr-password") || !strings.Contains(stderrErr.Error(), "failure.git") {
		t.Fatalf("unsafe or unhelpful Git stderr: %v", stderrErr)
	}
	mustGit(t, e.Root, "remote", "set-url", "origin", "https://actual-user:actual-password@example.invalid/actual.git")
	err := validateOrigin(e.Root, Repository{Name: "root", URL: "https://expected-user:expected-password@example.invalid/expected.git"})
	if err == nil || strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "password") || !strings.Contains(err.Error(), "actual.git") || !strings.Contains(err.Error(), "expected.git") {
		t.Fatalf("unsafe or unhelpful origin mismatch: %v", err)
	}
}

func TestMergePreservesIgnoredOriginCollisions(t *testing.T) {
	for _, tc := range []struct{ name, local, incoming string }{
		{"file", "local", "local"},
		{"leading-whitespace", " local", " local"},
		{"local-directory", "local/data", "local"},
		{"incoming-directory", "local", "local/data"},
		{"case-alias-file", "local", "LOCAL"},
		{"case-alias-local-directory", "local/data", "LOCAL"},
		{"case-alias-incoming-directory", "local", "LOCAL/data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t, 0)
			commitFile(t, e.Root, ".gitignore", "local\n local\nunrelated\n")
			mustGit(t, e.Root, "push", "origin", "main")
			m, err := e.Create("collision")
			if err != nil {
				t.Fatal(err)
			}
			local := filepath.Join(e.Root, tc.local)
			incoming := filepath.Join(m.Workspace, tc.incoming)
			for _, p := range []string{local, incoming} {
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
			}
			put(t, local, "precious local content")
			if strings.HasPrefix(tc.name, "case-alias-") {
				if _, err := os.Lstat(filepath.Join(e.Root, "LOCAL")); os.IsNotExist(err) {
					t.Skip("filesystem distinguishes case")
				}
			}
			put(t, filepath.Join(e.Root, "unrelated"), "keep unrelated")
			put(t, incoming, "incoming content")
			mustGit(t, m.Workspace, "add", "-f", tc.incoming)
			mustGit(t, m.Workspace, "commit", "-m", "feat: track incoming file")
			before := mustGit(t, e.Root, "rev-parse", "HEAD")
			index := mustGit(t, e.Root, "write-tree")
			for attempt := 0; attempt < 2; attempt++ {
				if err = e.Merge(m, "feat: test change"); err == nil {
					t.Fatal("merge accepted ignored collision")
				}
				data, readErr := os.ReadFile(local)
				if readErr != nil || string(data) != "precious local content" {
					t.Fatalf("local data lost: %q %v", data, readErr)
				}
				if mustGit(t, e.Root, "rev-parse", "HEAD") != before || mustGit(t, e.Root, "write-tree") != index {
					t.Fatal("merge changed origin ref or index")
				}
				m, err = e.store.load(m.WorkspaceID)
				if err != nil {
					t.Fatal(err)
				}
			}
			preserved := filepath.Join(t.TempDir(), "preserved")
			if err = os.Rename(local, preserved); err != nil {
				t.Fatal(err)
			}
			if err = e.Merge(m, "feat: test change"); err != nil {
				t.Fatalf("retry after preserving collision: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(e.Root, tc.incoming))
			if err != nil || string(data) != "incoming content" {
				t.Fatalf("incoming file missing: %q %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(e.Root, "unrelated"))
			if err != nil || string(data) != "keep unrelated" {
				t.Fatalf("unrelated ignored data lost: %q %v", data, err)
			}
			data, err = os.ReadFile(preserved)
			if err != nil || string(data) != "precious local content" {
				t.Fatalf("preserved data lost: %q %v", data, err)
			}
		})
	}
}

func TestDropRechecksDownstreamHookSafety(t *testing.T) {
	for _, tc := range []struct {
		name, command, path, message string
		force                        bool
	}{
		{"ignored-file", "echo precious > local", "local", "ignored filesystem content", false},
		{"foreign-repository", "git init foreign && echo precious > foreign/data", "foreign/data", "unrelated nested Git repository", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t, 1)
			origin := filepath.Join(e.Root, "repo0")
			commitFile(t, origin, ".gitignore", "local\nforeign/\n")
			mustGit(t, origin, "push", "origin", "main")
			e.Config.Children[0].Hooks = Hooks{HookDropBefore: {{ID: "generate", Shell: tc.command}}}
			saveContractConfig(t, e)
			m, err := e.Create("hook-safety")
			if err != nil {
				t.Fatal(err)
			}
			e.Force = tc.force
			err = e.Drop(m)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected safety rejection: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(m.Repositories[1].Path, tc.path))
			if err != nil || string(data) != "precious\n" {
				t.Fatalf("hook data lost: %q %v", data, err)
			}
		})
	}
}

func TestRefreshRejectsWrongCanonicalBranchBeforeMutation(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("refresh-branch")
	if err != nil {
		t.Fatal(err)
	}
	first := mustGit(t, m.Repositories[1].Path, "rev-parse", "HEAD")
	commitFile(t, m.Repositories[1].Origin, "advance", "advance")
	mustGit(t, m.Repositories[2].Origin, "checkout", "-b", "unrelated")
	commitFile(t, m.Repositories[2].Origin, "unrelated", "unrelated")
	if err := e.Refresh(m); err == nil {
		t.Fatal("expected canonical branch rejection")
	}
	if m.State != "ready" {
		t.Fatalf("state changed: %s", m.State)
	}
	if got := mustGit(t, m.Repositories[1].Path, "rev-parse", "HEAD"); got != first {
		t.Fatal("earlier repository changed")
	}
}

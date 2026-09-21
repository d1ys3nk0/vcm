package vcm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffIncludesCommittedStagedUnstagedAndUntracked(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.CreateSelected("diff", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.Workspace, "repo0")
	commitFile(t, path, "committed.txt", "committed\n")
	put(t, filepath.Join(path, "staged.txt"), "staged\n")
	mustGit(t, path, "add", "staged.txt")
	put(t, filepath.Join(path, "file.txt"), "unstaged\n")
	put(t, filepath.Join(path, "untracked.txt"), "private contents\n")
	report, err := e.Diff(m, DiffOptions{Only: "repo0", Patch: true})
	if err != nil {
		t.Fatal(err)
	}
	r := report.Repositories[0]
	if len(r.Files) != 3 || len(r.Commits) != 1 || len(r.Untracked) != 1 || r.Untracked[0] != "untracked.txt" || r.Source == r.Base {
		t.Fatalf("unexpected diff: %+v", r)
	}
	if !strings.Contains(r.Patch, "+unstaged") || strings.Contains(r.Patch, "private contents") {
		t.Fatalf("unexpected patch: %s", r.Patch)
	}
	committed, err := e.Diff(m, DiffOptions{Only: "repo0", Committed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(committed.Repositories[0].Files) != 1 || len(committed.Repositories[0].Untracked) != 0 {
		t.Fatalf("unexpected committed diff: %+v", committed)
	}
}

func TestDiffRenameBinaryMissingAndNoExternalDriver(t *testing.T) {
	e := fixture(t, 1)
	commitFile(t, e.Root, "original.txt", "original content\n")
	m, err := e.CreateSelected("diff-binary", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, m.Workspace, "mv", "original.txt", "renamed\tfile.txt")
	put(t, filepath.Join(m.Workspace, "binary.dat"), "\x00binary\x00")
	mustGit(t, m.Workspace, "add", "binary.dat")
	mustGit(t, m.Workspace, "config", "diff.external", "false")
	put(t, filepath.Join(m.Workspace, ".gitattributes"), "*.dat diff=custom\n")
	mustGit(t, m.Workspace, "config", "diff.custom.textconv", "false")
	report, err := e.Diff(m, DiffOptions{Only: "root", Patch: true})
	if err != nil {
		t.Fatal(err)
	}
	var renamed, binary bool
	for _, f := range report.Repositories[0].Files {
		if f.OldPath == "original.txt" && f.Path == "renamed\tfile.txt" {
			renamed = true
		}
		if f.Path == "binary.dat" && f.Binary {
			binary = true
		}
	}
	if !renamed || !binary {
		t.Fatalf("unexpected stats: %+v", report)
	}
	if err := os.Rename(filepath.Join(m.Workspace, "repo0"), filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	report, err = e.Diff(m, DiffOptions{})
	if err == nil || len(report.Repositories) != 2 || !report.Repositories[0].Available || report.Repositories[1].Error == "" {
		t.Fatalf("missing repository lost partial result: %+v %v", report, err)
	}
}

func TestTreeManifestDoesNotReadStore(t *testing.T) {
	e := fixture(t, 0)
	m, err := e.CreateSelected("tree-loaded", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// A malformed extra manifest must not affect inspection of an already loaded Change.
	if err := os.WriteFile(filepath.Join(e.store.dir, "invalid.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := e.TreeManifest(m)
	if err != nil || report.WorkspaceID != m.WorkspaceID {
		t.Fatalf("loaded manifest inspection failed: %+v %v", report, err)
	}
}

func TestDiffMissingConfigurationAndStrictSelection(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("missing-config")
	if err != nil {
		t.Fatal(err)
	}
	m.Missing = []string{"repo0"}
	report, err := e.Diff(m, DiffOptions{})
	if err == nil || len(report.Repositories) != 2 || report.Repositories[1].Error == "" {
		t.Fatalf("missing config result: %+v %v", report, err)
	}
	report, err = e.Diff(m, DiffOptions{Only: "repo0"})
	if err == nil || len(report.Repositories) != 1 {
		t.Fatalf("missing selected result: %+v %v", report, err)
	}
	for _, only := range []string{"root,root", " root", "repo0,", ",root"} {
		if _, err := e.Diff(m, DiffOptions{Only: only}); err == nil {
			t.Fatalf("accepted invalid selection %q", only)
		}
	}
}

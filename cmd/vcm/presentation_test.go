package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

func renderHumanForTest(t *testing.T, result any) string {
	t.Helper()
	var output bytes.Buffer
	if err := renderHuman(&output, result); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestListHumanOutputSortsAndMarksCurrentChange(t *testing.T) {
	parent := t.TempDir()
	olderWorkspace := filepath.Join(parent, "workspace.260901100000-older")
	newerWorkspace := filepath.Join(parent, "workspace.260902100000-newer")
	manifests := []*vcm.Manifest{
		{Tag: "260901100000-older", Slug: "older", Workspace: olderWorkspace, State: "ready", Repositories: []vcm.RepoState{{Merged: false}, {Merged: true}}},
		{Tag: "260902100000-newer", Slug: "newer", Workspace: newerWorkspace, State: "dropping", Repositories: []vcm.RepoState{{Removed: true}, {Removed: false}}},
	}

	output := renderHumanForTest(t, newListResults(manifests, filepath.Join(newerWorkspace, "repos", "api")))
	if strings.Index(output, "260902100000-newer") > strings.Index(output, "260901100000-older") {
		t.Fatalf("Changes are not newest first:\n%s", output)
	}
	for _, expected := range []string{"Change", "State", "Repos", "Age", "Workspace", "@", "1/2 removed", "1/2 merged"} {
		if !strings.Contains(output, expected) {
			t.Errorf("list output is missing %q:\n%s", expected, output)
		}
	}
	outside := newListResults(manifests, newerWorkspace+"-other")
	if outside[0].current || outside[1].current {
		t.Fatal("path prefix without a separator was marked current")
	}
}

func TestListEmptyHumanAndJSONOutput(t *testing.T) {
	root := cliWorkspace(t)
	human, err := runOutput(t, "list", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if human != "No Changes.\n" {
		t.Fatalf("unexpected empty list: %q", human)
	}
	result, err := runJSON[[]listResult](t, "list", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty JSON array, got %+v", result)
	}
}

func TestStatusUsesFullJSONHashesAndAbbreviatedHumanHashes(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"
	const target = "2222222222222222222222222222222222222222"
	manifest := &vcm.Manifest{
		Tag: "260902100000-readable", Slug: "readable", Workspace: "/tmp/readable", State: "ready", Backups: []string{"/tmp/recovery.tar.gz"},
		Hooks: map[string]vcm.HookState{"root/merge-before/check": {Status: "failed", Error: "hook detail"}},
	}
	report := vcm.StatusReport{
		Repositories:      []vcm.RepositoryStatus{{Name: "root", Path: manifest.Workspace, Source: head, Target: target, RecordedTarget: strings.Repeat("3", 40), RecoveryState: "committed_resolution", DirtyOrInterrupted: "uncommitted files"}},
		RecoveryDirectory: "/tmp/recovery",
		PendingSync:       []vcm.PendingSyncStatus{{Path: "/tmp/root.sync", Error: "repair required"}},
	}
	result := newStatusResult(manifest, report)
	human := renderHumanForTest(t, result)
	for _, expected := range []string{"1111111", "2222222", "3333333", "committed_resolution", "dirty", "uncommitted files", "Hooks:", "Backups:", "Pending synchronization:"} {
		if !strings.Contains(human, expected) {
			t.Errorf("status output is missing %q:\n%s", expected, human)
		}
	}
	if strings.Contains(human, head) || strings.Contains(human, target) {
		t.Fatalf("human output contains full revision:\n%s", human)
	}
	var encoded bytes.Buffer
	if err := renderJSON(&encoded, result); err != nil {
		t.Fatal(err)
	}
	var decoded statusResult
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Repositories[0].Head != head || decoded.Repositories[0].Target != target || decoded.Repositories[0].RecordedTarget != strings.Repeat("3", 40) || decoded.Repositories[0].RecoveryState != "committed_resolution" {
		t.Fatalf("JSON revisions were abbreviated: %+v", decoded.Repositories[0])
	}
	if !strings.Contains(encoded.String(), `"created_at":"2026-09-02T10:00:00Z"`) {
		t.Fatalf("created_at is not RFC 3339 UTC: %s", encoded.String())
	}
}

func TestLifecycleResultsExposeCommandContractsWithoutManifestInternals(t *testing.T) {
	manifest := &vcm.Manifest{
		Tag: "260902100000-contract", Slug: "contract", Workspace: "/tmp/change", Origin: "/tmp/origin", State: "ready",
		Repositories: []vcm.RepoState{{
			Repository: vcm.Repository{Name: "root"}, Path: "/tmp/change", Base: strings.Repeat("a", 40), Source: strings.Repeat("b", 40), Target: strings.Repeat("c", 40), Merged: true, Removed: true,
		}},
		Backups: []string{"/tmp/backup.tar.gz"},
	}

	created := newCreateResult(manifest)
	if created.Tag != manifest.Tag || len(created.Repositories) != 1 || created.Repositories[0].Base != manifest.Repositories[0].Base {
		t.Fatalf("invalid create result: %+v", created)
	}
	merged := newMergeResult(manifest)
	if !merged.Repositories[0].Merged || merged.Repositories[0].Source != manifest.Repositories[0].Source || len(merged.Backups) != 1 {
		t.Fatalf("invalid merge result: %+v", merged)
	}
	dropped := newDropResult(manifest)
	if !dropped.Repositories[0].Removed || len(dropped.Backups) != 1 {
		t.Fatalf("invalid drop result: %+v", dropped)
	}

	for name, result := range map[string]any{"create": created, "merge": merged, "drop": dropped} {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := renderJSON(&encoded, result); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encoded.String(), `"config"`) || strings.Contains(encoded.String(), `"origin"`) {
				t.Fatalf("persisted manifest internals leaked into %s result: %s", name, encoded.String())
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(encoded.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload["repositories"]) == 0 || len(payload["tag"]) == 0 || len(payload["state"]) == 0 {
				t.Fatalf("%s result omitted lifecycle contract fields: %s", name, encoded.String())
			}
		})
	}

	var encoded bytes.Buffer
	command := commandResult{Command: "sync", Complete: true, Workspace: "/tmp/workspace"}
	if err := renderJSON(&encoded, command); err != nil {
		t.Fatal(err)
	}
	var decodedCommand commandResult
	if err := json.Unmarshal(encoded.Bytes(), &decodedCommand); err != nil {
		t.Fatal(err)
	}
	if !decodedCommand.Complete || decodedCommand.Workspace != command.Workspace {
		t.Fatalf("invalid command result round trip: %+v", decodedCommand)
	}
}

func TestAgeBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{{59 * time.Second, "now"}, {time.Minute, "1m"}, {time.Hour, "1h"}, {24 * time.Hour, "1d"}, {30 * 24 * time.Hour, "1mo"}, {365 * 24 * time.Hour, "1y"}} {
		if got := age(now, now.Add(-test.duration)); got != test.want {
			t.Errorf("age(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestTreePresentationContract(t *testing.T) {
	report := vcm.TreeReport{Workspace: "/workspace", Context: "change", Change: "260910120000-example", Repositories: []vcm.TreeRepository{
		{Name: "root", Path: "/change", Available: true, Branch: "change", Clean: false, TrackedChanges: 2, UntrackedFiles: 1, SyncState: "diverged", SyncTarget: "main", Ahead: 3, Behind: 4},
		{Name: "api", Path: "/change/api", SyncState: "unavailable"},
	}}
	result := newTreeResult(report)
	human := renderHumanForTest(t, result)
	for _, expected := range []string{"Repository", "Branch", "Tree", "Sync", "Path", "2 changed, 1 untracked", "diverged +3/-4", "unavailable"} {
		if !strings.Contains(human, expected) {
			t.Fatalf("tree output omitted %q:\n%s", expected, human)
		}
	}
	var encoded bytes.Buffer
	if err := renderJSON(&encoded, result); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["workspace"] != report.Workspace || payload["context"] != "change" || payload["change"] != report.Change {
		t.Fatalf("unexpected tree JSON: %s", encoded.String())
	}
	repositories := payload["repositories"].([]any)
	available := repositories[0].(map[string]any)
	for _, field := range []string{"branch", "clean", "tracked_changes", "untracked_files", "sync_state", "sync_target", "ahead", "behind", "cached"} {
		if _, ok := available[field]; !ok {
			t.Fatalf("available repository omitted %s: %s", field, encoded.String())
		}
	}
	unavailable := repositories[1].(map[string]any)
	if _, ok := unavailable["branch"]; ok {
		t.Fatalf("unavailable repository exposed available-only fields: %s", encoded.String())
	}
}

func TestDryRunHumanOutputShowsForceOnlyWhenApplicable(t *testing.T) {
	bootstrap := renderHumanForTest(t, dryRunResult{Command: "bootstrap", DryRun: true, Resources: []string{"/workspace"}})
	if strings.Contains(bootstrap, "Force:") {
		t.Fatalf("bootstrap shows irrelevant force state:\n%s", bootstrap)
	}
	drop := renderHumanForTest(t, dryRunResult{Command: "drop", DryRun: true, Force: true, Resources: []string{"/change"}})
	if !strings.Contains(drop, "Force: true") {
		t.Fatalf("drop omits force state:\n%s", drop)
	}
	if strings.Contains(drop, "Delete ignored content:") {
		t.Fatalf("drop implies automatic ignored-content deletion:\n%s", drop)
	}
	merge := renderHumanForTest(t, dryRunResult{Command: "merge", DryRun: true, DeletesIgnoredContent: true, Resources: []string{"/change"}})
	if !strings.Contains(merge, "Delete ignored content: true") {
		t.Fatalf("merge omits ignored-content deletion:\n%s", merge)
	}
}

func TestRefreshPresentationExposesGenericLifecycleContract(t *testing.T) {
	m := &vcm.Manifest{Tag: "260910120000-refresh", State: "ready", Repositories: []vcm.RepoState{{Repository: vcm.Repository{Name: "root"}, Base: strings.Repeat("a", 40), Source: strings.Repeat("b", 40)}}}
	result := newRefreshResult(m)
	if result.Tag != m.Tag || result.State != m.State || len(result.Repositories) != len(m.Repositories) {
		t.Fatalf("invalid refresh result: %+v", result)
	}
	human := renderHumanForTest(t, result)
	if want := "Refreshed Change 260910120000-refresh (1 repositories).\n"; human != want {
		t.Fatalf("unexpected refresh output: %q, want %q", human, want)
	}
	var encoded bytes.Buffer
	if err := renderJSON(&encoded, result); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"tag", "state", "repositories"} {
		if len(payload[field]) == 0 {
			t.Errorf("refresh JSON omitted %q: %s", field, encoded.String())
		}
	}
	if len(payload) != 3 {
		t.Fatalf("refresh JSON exposed fields outside its generic lifecycle contract: %s", encoded.String())
	}
}

func TestStructuredErrorOutputAndJSONFlagSelection(t *testing.T) {
	var output bytes.Buffer
	writeError(&output, true, errors.New("bad input"))
	var result errorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON error %q: %v", output.String(), err)
	}
	if result.Error.Message != "bad input" {
		t.Fatalf("unexpected error payload: %+v", result)
	}
	if !requestsJSON([]string{"status", "--json"}) || requestsJSON([]string{"--json=false", "status"}) ||
		!requestsJSON([]string{"--json=false", "--json", "status"}) || requestsJSON([]string{"--json", "--json=false", "status"}) {
		t.Fatal("JSON flag selection does not honor explicit false")
	}

	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	exitCode := runMain([]string{"--json", "--unknown", "status"})
	os.Stderr = old
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if exitCode == 0 {
		t.Fatal("parse failure returned a successful exit code")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse failure was not a single JSON error object: %q: %v", data, err)
	}
}

package vcm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	IssueDirtyCheckout      = "dirty_checkout"
	IssueUnexpectedBranch   = "unexpected_branch"
	IssueUnexpectedWorktree = "unexpected_worktree"
	IssueMissingBranch      = "missing_branch"
	IssueMissingWorktree    = "missing_worktree"
	IssueOwnershipMismatch  = "ownership_mismatch"
	IssueInspectionError    = "inspection_error"
)

type AuditRepository struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Clean  bool   `json:"clean"`
}

type AuditIssue struct {
	Repository string `json:"repository"`
	Kind       string `json:"kind"`
	Path       string `json:"path,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Detail     string `json:"detail"`
}

type AuditReport struct {
	Clean        bool              `json:"clean"`
	Workspace    string            `json:"workspace"`
	Repositories []AuditRepository `json:"repositories"`
	Issues       []AuditIssue      `json:"issues"`
}

type worktreeInfo struct {
	Path     string
	Head     string
	Branch   string
	Locked   bool
	Prunable bool
}

type repositoryAudit struct {
	Name                string
	Origin              string
	ExpectedPaths       map[string]string
	ProtectedPaths      map[string]bool
	AllowedBranches     map[string]bool
	OwnershipMismatches map[string]bool
	BranchObjects       map[string]string
	Worktrees           []worktreeInfo
	DirtyExpected       map[string]bool
	UnexpectedBranches  map[string]string
	UnexpectedWorktrees map[string]worktreeInfo
}

type auditInventory struct {
	Report       AuditReport
	Repositories map[string]*repositoryAudit
	Unsafe       bool
}

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

func parseWorktrees(raw string) []worktreeInfo {
	fields := strings.Split(raw, "\x00")
	result := []worktreeInfo{}
	current := worktreeInfo{}
	flush := func() {
		if current.Path != "" {
			current.Path = canonicalPath(current.Path)
			result = append(result, current)
		}
		current = worktreeInfo{}
	}
	for _, field := range fields {
		if field == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			current.Path = value
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	flush()
	return result
}

func inventoryWorktrees(origin string) ([]worktreeInfo, error) {
	raw, err := gitRaw(origin, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(raw), nil
}

func inventoryBranches(origin string) (map[string]string, error) {
	raw, err := gitRaw(origin, "for-each-ref", "--format=%(refname:strip=2)%00%(objectname)%00", "refs/heads")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(raw, "\x00")
	result := map[string]string{}
	for i := 0; i+1 < len(fields); i += 2 {
		name := strings.TrimSpace(fields[i])
		object := strings.TrimSpace(fields[i+1])
		if name != "" && object != "" {
			result[name] = object
		}
	}
	return result, nil
}

func appendIssue(inv *auditInventory, issue AuditIssue) {
	inv.Report.Issues = append(inv.Report.Issues, issue)
}

func (e *Engine) auditManifests() ([]*Manifest, []AuditIssue, error) {
	if err := secureDirectory(e.store.dir, false); os.IsNotExist(err) {
		return []*Manifest{}, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(e.store.dir)
	if err != nil {
		return nil, nil, err
	}
	manifests := []*Manifest{}
	issues := []AuditIssue{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(e.store.dir, entry.Name())
		manifest, loadErr := e.store.load(strings.TrimSuffix(entry.Name(), ".json"))
		if loadErr != nil {
			issues = append(issues, AuditIssue{Repository: "root", Kind: IssueInspectionError, Path: path, Detail: "invalid VCM state: " + loadErr.Error()})
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, issues, nil
}

func (e *Engine) audit() (auditInventory, error) {
	manifests, stateIssues, err := e.auditManifests()
	if err != nil {
		return auditInventory{}, err
	}
	inv := auditInventory{
		Report:       AuditReport{Workspace: e.Root},
		Repositories: map[string]*repositoryAudit{},
		Unsafe:       len(stateIssues) > 0,
	}
	inv.Report.Issues = append(inv.Report.Issues, stateIssues...)
	repositories := []Repository{{Name: "root", Trunk: e.Config.Root.Trunk}}
	repositories = append(repositories, e.Config.Children...)
	configured := map[string]bool{}
	for _, repository := range repositories {
		configured[repository.Name] = true
		origin := e.Root
		if repository.Name != "root" {
			origin = filepath.Join(e.Root, repository.Path)
		}
		repo := &repositoryAudit{
			Name: repository.Name, Origin: canonicalPath(origin),
			ExpectedPaths:       map[string]string{canonicalPath(origin): repository.Trunk},
			ProtectedPaths:      map[string]bool{},
			AllowedBranches:     map[string]bool{repository.Trunk: true},
			OwnershipMismatches: map[string]bool{},
			DirtyExpected:       map[string]bool{}, UnexpectedBranches: map[string]string{}, UnexpectedWorktrees: map[string]worktreeInfo{},
		}
		inv.Repositories[repository.Name] = repo
	}

	for _, manifest := range manifests {
		for _, state := range manifest.Repositories {
			if !state.Owned || state.Removed {
				continue
			}
			repo := inv.Repositories[state.Repository.Name]
			if repo == nil {
				appendIssue(&inv, AuditIssue{Repository: state.Repository.Name, Kind: IssueOwnershipMismatch, Branch: manifest.Tag, Detail: "live state references a repository absent from current configuration; its resources are protected from pruning"})
				continue
			}
			expectedPath := state.Path
			if expectedPath == "" {
				expectedPath = manifest.Workspace
				if state.Repository.Name != "root" {
					expectedPath = filepath.Join(manifest.Workspace, state.Repository.Path)
				}
			}
			rawExpectedPath := expectedPath
			expectedPath = canonicalPath(expectedPath)
			if _, statErr := os.Lstat(rawExpectedPath); statErr == nil {
				if ownershipErr := e.owned(manifest, &state); ownershipErr != nil {
					repo.OwnershipMismatches[expectedPath] = true
					appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueOwnershipMismatch, Path: rawExpectedPath, Branch: manifest.Tag, Detail: ownershipErr.Error() + "; worktree is protected from pruning"})
				}
			}
			repo.ExpectedPaths[expectedPath] = manifest.Tag
			repo.ProtectedPaths[expectedPath] = true
			repo.AllowedBranches[manifest.Tag] = true
		}
	}

	for _, repository := range repositories {
		repo := inv.Repositories[repository.Name]
		branches, branchErr := inventoryBranches(repo.Origin)
		worktrees, worktreeErr := inventoryWorktrees(repo.Origin)
		if branchErr != nil || worktreeErr != nil {
			detail := ""
			if branchErr != nil {
				detail = branchErr.Error()
			} else {
				detail = worktreeErr.Error()
			}
			appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueInspectionError, Path: repo.Origin, Detail: detail})
			continue
		}
		repo.BranchObjects = branches
		repo.Worktrees = worktrees
		byPath := map[string]worktreeInfo{}
		for _, worktree := range worktrees {
			byPath[worktree.Path] = worktree
			if repo.AllowedBranches[worktree.Branch] {
				repo.ProtectedPaths[worktree.Path] = true
			}
		}
		for path, expectedBranch := range repo.ExpectedPaths {
			worktree, ok := byPath[path]
			if !ok {
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueMissingWorktree, Path: path, Branch: expectedBranch, Detail: "expected worktree is not registered"})
				continue
			}
			if worktree.Branch != expectedBranch && !repo.OwnershipMismatches[path] {
				actual := worktree.Branch
				if actual == "" {
					actual = "detached HEAD"
				}
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueOwnershipMismatch, Path: path, Branch: expectedBranch, Detail: fmt.Sprintf("expected branch %s, found %s; worktree is protected from pruning", expectedBranch, actual)})
			}
			if err := clean(path); err != nil {
				if worktree.Branch == expectedBranch && !repo.OwnershipMismatches[path] {
					repo.DirtyExpected[path] = true
				}
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueDirtyCheckout, Path: path, Branch: worktree.Branch, Detail: err.Error()})
			}
		}
		for branch := range repo.AllowedBranches {
			if _, ok := branches[branch]; !ok {
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueMissingBranch, Branch: branch, Detail: "expected local branch is missing"})
			}
		}
		for branch, object := range branches {
			if !repo.AllowedBranches[branch] {
				repo.UnexpectedBranches[branch] = object
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueUnexpectedBranch, Branch: branch, Detail: "local branch is not present in current VCM state"})
			}
		}
		for _, worktree := range worktrees {
			if _, ok := repo.ExpectedPaths[worktree.Path]; ok {
				continue
			}
			if repo.ProtectedPaths[worktree.Path] {
				branch := worktree.Branch
				if branch == "" {
					branch = "detached HEAD"
				}
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueOwnershipMismatch, Path: worktree.Path, Branch: branch, Detail: "live-state or trunk branch is registered at an unexpected path; worktree is protected from pruning"})
				if err := clean(worktree.Path); err != nil {
					appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueDirtyCheckout, Path: worktree.Path, Branch: worktree.Branch, Detail: err.Error()})
				}
				continue
			}
			repo.UnexpectedWorktrees[worktree.Path] = worktree
			appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueUnexpectedWorktree, Path: worktree.Path, Branch: worktree.Branch, Detail: "registered worktree is not present in current VCM state"})
			if err := clean(worktree.Path); err != nil {
				appendIssue(&inv, AuditIssue{Repository: repo.Name, Kind: IssueDirtyCheckout, Path: worktree.Path, Branch: worktree.Branch, Detail: err.Error()})
			}
		}
	}

	for name := range configured {
		repo := inv.Repositories[name]
		cleanRepo := true
		for _, issue := range inv.Report.Issues {
			if issue.Repository == name {
				cleanRepo = false
				break
			}
		}
		inv.Report.Repositories = append(inv.Report.Repositories, AuditRepository{Name: name, Origin: repo.Origin, Clean: cleanRepo})
	}
	for _, issue := range inv.Report.Issues {
		if configured[issue.Repository] {
			continue
		}
		configured[issue.Repository] = true
		inv.Report.Repositories = append(inv.Report.Repositories, AuditRepository{Name: issue.Repository, Clean: false})
	}
	sort.Slice(inv.Report.Repositories, func(i, j int) bool { return inv.Report.Repositories[i].Name < inv.Report.Repositories[j].Name })
	sort.Slice(inv.Report.Issues, func(i, j int) bool {
		a, b := inv.Report.Issues[i], inv.Report.Issues[j]
		return a.Repository+"\x00"+a.Kind+"\x00"+a.Path+"\x00"+a.Branch < b.Repository+"\x00"+b.Kind+"\x00"+b.Path+"\x00"+b.Branch
	})
	inv.Report.Clean = len(inv.Report.Issues) == 0
	return inv, nil
}

func (e *Engine) Check() (AuditReport, error) {
	inv, err := e.audit()
	return inv.Report, err
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

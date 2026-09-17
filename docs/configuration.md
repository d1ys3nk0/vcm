# Configuration and hooks

`vcm.yml` uses configuration version `1`. The machine-readable reference is [vcm.schema.json](../schema/vcm.schema.json). Unknown fields are errors. `children` must be an explicit list; use `[]` for a root without child repositories. The CLI additionally validates filesystem path safety, Git branch names, hook bodies, runner values, and the dependency graph.

| Field | Meaning |
| --- | --- |
| `version` | Required integer `1` |
| `runners.shell` | Optional shell executable or path; defaults to `bash` |
| `runners.python` | Optional Python executable or path; defaults to `python` |
| `root.trunk` | Root integration branch |
| `root.hooks` | Optional root lifecycle hooks |
| `children` | Ordered child repository declarations |
| `children[].name` | Unique child identity; `root` is reserved |
| `children[].path` | Explicit path relative to the root |
| `children[].url` | Expected Git origin URL |
| `children[].trunk` | Child integration branch |
| `children[].depends_on` | Optional list of child names |
| `children[].hooks` | Optional child lifecycle hooks |

Runner values are single executable names or paths. VCM does not parse them as shell command lines. Explicit blank or null runner values are invalid. Paths must remain inside the root and must not overlap or escape through symlinks. Child identities must be unique; dependencies must exist and form an acyclic graph. Dependencies precede dependents during bootstrap, creation, and merging; declaration order resolves ties. Cleanup reverses that order.

By default, a Change includes the root and every configured child. `vcm create <slug> --only core,web` selects exactly the named children, while `--except devtools` selects every configured child other than those named. The root is always selected. The two flags are mutually exclusive and accept exact, comma-separated child identities without blanks or duplicates. A partial selection is valid only when every dependency of each selected child is also selected; VCM does not add dependencies automatically. Excluding all children creates a root-only Change.

```yaml
version: 1
runners:
  shell: /usr/local/bin/bash
  python: /usr/local/bin/python3
root:
  trunk: main
  hooks:
    merge-before:
      - id: verify
        shell: |
          ./scripts/verify-change.sh
      - id: archive
        python: |
          from archive import archive_change
          archive_change()
children:
  - name: api
    path: repos/api
    url: git@github.com:example/api.git
    trunk: main
    hooks:
      create-after:
        - id: prepare
          shell: ./scripts/prepare-change.sh
  - name: web
    path: repos/web
    url: git@github.com:example/web.git
    trunk: main
    depends_on: [api]
```

Each hook has a stable `id` and exactly one nonblank `shell` or `python` body. Root and child repositories use the same phases: `create-before`, `create-after`, `merge-before`, `merge-after`, `drop-before`, and `drop-after`. Entries execute in declaration order. Shell bodies run as `<runners.shell> -eu -o pipefail -c <body>`; Python bodies run as `<runners.python> -c <body>`.

| Phase | Working directory |
| --- | --- |
| `create-before` | Base repository |
| `create-after` | Change worktree |
| `merge-before` | Change worktree |
| `merge-after` | Base repository after all managed worktree cleanup |
| `drop-before` | Change worktree |
| `drop-after` | Base repository after that repository's worktree removal |

| Variable | Meaning |
| --- | --- |
| `VCM_CHANGE_TAG` | Timestamped Change identity and branch name |
| `VCM_CHANGE_SLUG` | User-supplied Change slug |
| `VCM_SELECTED_REPOSITORIES` | Ordered comma-separated inventory: `root` followed by selected children |
| `VCM_ROOT` | Change root checkout |
| `VCM_ROOT_ORIGIN` | Original root checkout |
| `VCM_REPOSITORY_NAME` | Child name, or `root` for root hooks |
| `VCM_REPOSITORY_ORIGIN` | Original repository checkout |
| `VCM_REPOSITORY_PATH` | Relevant Change checkout |
| `VCM_HOOK_PHASE` | Current lifecycle phase |
| `VCM_HOOK_ID` | Current hook identity |

`vcm fetch` fetches each configured trunk into its cached remote-tracking ref without moving local state. `vcm pull` requires every canonical repository to be clean with its configured trunk checked out, then rebases children in dependency order and the root last. `vcm publish` sends ready Change branches to configured origins without changing canonical trunks; `vcm restore --fetch` may retrieve missing snapshot objects through matching configured origins. Change worktrees never fetch or pull; `vcm refresh [change]` merges advanced canonical local trunks into those worktrees.

Create preflights clean configured-trunk checkouts and records local baselines for every selected base repository, runs root `create-before`, creates the root worktree, then runs each child's `create-before`, creates its worktree, and runs its `create-after` in dependency order. Root `create-after` runs after all selected worktrees exist. Commits produced by `create-before` are included in that repository's creation baseline.

Expansion with `vcm add [change] --only api,web` journals the exact requested additions before resource creation. New children use their normal `create-before` and `create-after` contracts in dependency order. Existing child hooks are not rerun. Root `create-after` runs once per expansion generation after all additions exist, with the expanded `VCM_SELECTED_REPOSITORIES`. Retrying an interrupted expansion preserves completed steps. Restore creates worktrees at snapshot revisions without lifecycle hooks; subsequent operations use the configured policy.

Merge runs every selected repository's `merge-before` hook before changing any trunk, freezes all source and target revisions, prepares squash commits, then updates child trunks in dependency order and the root last. A fresh merge uses one Conventional Commit subject, defaulting to `feat: <manifest slug>` when `--message` is omitted. Every changed repository receives that subject followed by a `Commits:` list of the Git-abbreviated hash (at least seven characters) and subject of each commit unique to its frozen source, in newest-first topological order; commits already reachable from its frozen target are excluded. Commit bodies, authors, and dates are omitted, and unchanged trees create no commit. VCM persists the effective subject when merging starts, so a retry without `--message` reuses it and a different explicit subject is rejected. After successful local integration is checkpointed, ordinary merge removes all managed worktrees, including Git-ignored files and directories they contain, without a VCM force flag or recovery backup. Cleanup still rejects ordinary nonignored untracked files, tracked or staged modifications, ownership or revision drift, conflicts, and foreign nested Git repositories. `merge --keep` defers this cleanup and its hooks until `vcm cleanup`; the persisted retention choice survives retries. Cleanup accepts safely advanced descendant trunks but rejects changed source revisions or rewritten integration targets. VCM then runs child `merge-after` hooks in dependency order and root `merge-after` last. Drop runs root `drop-before` while all selected worktrees exist, then processes children in reverse dependency order by running `drop-before`, removing the owned worktree, and running `drop-after`; it removes the root last and runs root `drop-after`. Ordinary drop rejects ignored content; forced drop backs it up before removal. Hooks for unselected repositories and resources never created are skipped.

Merge supports three independent, invocation-scoped overrides. `--ignore-hook-failures` records a nonzero lifecycle hook command outcome as failed, emits a `failed (ignored by --ignore-hook-failures)` diagnostic, refreshes the recorded source or target revision when necessary, and continues only when the checkout remains clean. This merge-only flag does not control cleanup, create a recovery backup, reset content, or relax safety checks; successful merge cleanup deletes ignored content unconditionally as part of owned worktree removal. `--skip-hooks` accepts the exact comma-separated phases `merge-before` and `merge-after`; each configured bypass is logged and no skipped outcome is stored. `--skip-hook-git-hooks` applies a process-local Git configuration override of `core.hooksPath=/dev/null` inside merge lifecycle hook subprocesses; it does not modify repository configuration. These options compose without implying one another. Dirty post-hook checkouts, ordinary nonignored untracked content, hook output failures, configuration or state failures, ownership or revision drift, foreign nested Git repositories, merge conflicts, and incomplete lifecycle states remain fatal.

Hooks own staging and committing their output. Successful hooks must leave their checkout clean. Failures preserve files for inspection and repair. Hooks must be idempotent: a process interruption can leave external effects whose completion VCM cannot determine. Treat hooks as trusted executable project code.

VCM writes hook start, output, and completion lines to stderr with the prefix `[hook/repository/phase/hook-id @ execution-path]`. Both hook stdout and stderr share one synchronized line stream, so every complete line has exactly one prefix; VCM also terminates and prefixes a final unterminated line before reporting the hook result. Command results remain isolated on stdout, including with `--json`.

The six names above are the complete version `1` phase contract.

## Configuration changes and recovery

State version 4 records selected repository identities, relative paths, URLs, trunks, dependency order, inline hook definitions/order, and applicable runners with execution fingerprints. External script file contents are not fingerprinted. Configuring an additional unselected child does not create drift for existing Changes or add that child automatically.

Inspect reported configuration drift before `vcm recover <change> --adopt-config`; use `--dry-run` first to preview adoption. Adoption records a new baseline without running hooks and is mutually exclusive with `--retry-hook`. Owned repositories cannot be rebound to different names, paths, URLs, or trunks. Dependencies must remain complete and cannot alter an interrupted operation's recorded order. Restore missing selected declarations before recovery.

Pending and failed hook definitions may be explicitly adopted. A running hook requires external-effects acknowledgment through `--retry-hook <key> --acknowledge-effects` before adoption. Completed outcomes record the definition and generation that ran. Changed completed hooks in an unfinished phase can be invalidated with subsequent hooks only before downstream resource effects; otherwise restore their recorded configuration. Completed creation phases remain historical and are not retroactively rerun. See [recovery](recovery.md) for the legacy migration boundary.

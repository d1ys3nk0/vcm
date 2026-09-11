# Vibe Change Manager

VCM manages a Git workspace root and its child repositories as one Change. It bootstraps repositories, synchronizes trunks, creates isolated worktrees, runs project hooks, squash-merges locally, publishes trunks, and records recovery state. Project policy belongs in configured hooks. Git is required; configured hooks additionally require their selected runner.

## Install

```sh
curl -fsSL https://github.com/d1ys3nk0/vcm/releases/latest/download/install.sh | sh
```

Release binaries support macOS and Linux on amd64 and arm64. The installer resolves one release, verifies its archive against that release's SHA-256 checksums, and installs into `/usr/local/bin`. It requests privileges only for the final installation when needed. To select a release or destination, download and inspect the installer, then run:

```sh
VCM_VERSION=v0.0.1 VCM_INSTALL_DIR="$HOME/.local/bin" sh install.sh
```

Add a custom destination to `PATH`. Release archives also include the MIT license; release assets include checksums and build provenance.

## Quick start

Create `vcm.yml` in an existing Git repository with a committed trunk:

```yaml
version: 1
root:
  trunk: main
children:
  - name: api
    path: repos/api
    url: git@github.com:example/api.git
    trunk: main
```

```sh
printf '/repos/api/\n' >> .gitignore
git add vcm.yml .gitignore
git commit -m 'chore: configure VCM workspace'
vcm validate
vcm bootstrap
vcm check
vcm create improve-search
# Or create the root plus an exact subset of children:
vcm create improve-search --only api
vcm list
```

The root and child trunks need configured `origin` remotes. Before creation, publish the initial configuration through your normal Git workflow so synchronization can rebase onto its remote trunk. Ignore each configured child checkout path in the root repository.

Make and commit changes in the returned sibling root and its child worktrees. Use `vcm status` within the Change root. If local trunks advanced, run `vcm refresh` and repeat verification. Then explicitly run `vcm merge` when ready to integrate into local trunks. A fresh merge defaults its Conventional Commit subject to `feat: <manifest slug>`; use `--message '<conventional subject>'` to override it. Merge does not fetch, pull, or push; it removes the managed worktrees after successful local integration and completes post-cleanup finalization from the base repositories. Run `vcm push` from the canonical workspace to publish the resulting trunks. Run `vcm drop` to discard an unneeded Change without integration; inspect `--dry-run` before forced removal.

## Commands

| Command | Purpose |
| --- | --- |
| `validate` | Check configuration and dependency graph |
| `check` | Audit repository cleanliness, local worktrees, and local branches |
| `bootstrap` | Clone missing repositories and validate existing origins |
| `sync [--force]` | Fast-forward child trunks; force resets with recovery backups |
| `push [--dry-run]` | Validate every canonical trunk, then publish children in dependency order and the root last |
| `create <slug> [--only names] [--except names]` | Synchronize origins and create a full or partial Change |
| `list` | List recorded Changes |
| `status [change]` | Inspect lifecycle, hooks, merge, and recovery state |
| `refresh [change]` | Merge advanced canonical local trunks into the Change without fetching |
| `merge [change] [--message SUBJECT] [-f\|--force] [--skip-hooks PHASES] [--skip-git-hooks]` | Gate all repositories, then create one squash commit per changed repository |
| `drop [change] [--force]` | Remove owned Change resources |
| `prune [--dry-run]` | Interactively clean retained checkouts and remove unexpected worktrees and branches |
| `version` | Show version and source commit |

`--workspace PATH` selects a root; otherwise discovery walks upward for `vcm.yml` at a Git root. Commands produce concise human-readable text and tables by default. Every executable command accepts `--json` for a typed machine-readable result; help remains plain text. Mutating commands support `--dry-run`, which executes no hooks or mutations. Flags may appear before or after command arguments. `create --only core,web` selects exactly those children; `create --except devtools` selects every configured child except `devtools`. The root is always selected, the flags are mutually exclusive, and a selected repository whose dependency is not selected is rejected. Excluding every child creates a root-only Change. For `status`, `refresh`, `merge`, and `drop`, Change selection accepts a managed tag or root workspace path. When the argument is omitted, VCM infers the Change from any directory inside its managed worktree; outside a managed Change worktree, the argument is required. Slugs use lowercase kebab-case letters and digits; Change tags add a UTC timestamp and are also branch names.

Merge overrides are invocation-scoped and independent. `-f` or `--force` runs all non-skipped lifecycle hooks but treats a hook command's nonzero exit as a recorded warning only when the hook leaves its checkout clean; later hooks and integration continue. It does not enable forced cleanup or discard content. `--skip-hooks merge-before`, `--skip-hooks merge-after`, or both exact comma-separated phases bypass configured hooks in those phases and log every bypass to stderr without persisting a skipped outcome. Blank, duplicate, whitespace-padded, and unsupported phases are rejected. `--skip-git-hooks` supplies `core.hooksPath=/dev/null` only to Git commands started by merge lifecycle hook subprocesses. Configuration, state, output, checkout cleanliness, ownership, revision, conflict, and incomplete lifecycle failures remain fatal under every override.

`vcm check` allows only each repository's configured local trunk and the tags of its live worktrees recorded as owned and not removed. Remote-tracking branches and VCM recovery refs are outside the audit. Ignored files do not make a retained worktree dirty. Repositories and their findings are reported with the root first, followed by children in `vcm.yml` order; repositories referenced only by stale state follow in name order. Findings are written as a complete human or JSON report followed by a nonzero exit status.

`vcm push` first validates every configured child checkout and the root checkout before publishing any repository. Each checkout must be clean, have no unfinished Git operation, have its configured trunk checked out at the local trunk revision, and use the configured `origin`. VCM fetches each remote trunk and rejects missing, behind, or divergent histories; it never force-pushes or creates a missing remote trunk. After the entire workspace passes preflight, children are pushed in dependency order with explicit trunk refspecs and the root is pushed last. Cross-repository publication is not atomic: a remote race or transport failure can leave earlier repositories published. Fix the failure and rerun `vcm push`; already-current repositories are safe to retry.

Run `vcm prune --dry-run` before pruning. A real prune requires interactive stdin and asks separately before resetting a dirty retained checkout, deleting an unexpected worktree, or deleting an unexpected local branch. Only `y` or `yes` approves an item; declined and failed items do not stop independent actions. Resets use the checkout's current `HEAD`, remove ordinary untracked files, and retain ignored files. Each action phase processes the root first and then children in `vcm.yml` order; resets run before unexpected worktree removals, which run before branch deletions. Prune does not fetch, synchronize, or create recovery backups, and returns nonzero when findings remain.

For example, `vcm list` prints Changes newest first and marks the Change containing the current directory with `@`:

```text
   Change                       State  Repos       Age  Workspace
@  260910120000-improve-search  ready  0/2 merged  2h   /work/product.260910120000-improve-search
```

JSON output is command-specific and does not expose the persisted state. Timestamps use RFC 3339 UTC and Git revisions remain unabbreviated. State version 2 records lifecycle, refresh and merge recovery checkpoints, and the persisted merge subject, keyed by repositories selected for the Change. Version 1 state is read compatibly and upgraded on mutation. Configuration and derived identities are resolved from the current `vcm.yml` and state filename instead of being copied into state. The `vcm.yml` format is also version 1.

| Result | JSON shape |
| --- | --- |
| `validate` | `{valid, workspace}` |
| `check` | `{clean, workspace, repositories:[{name, origin, clean}], issues:[{repository, kind, path?, branch?, detail}]}` |
| `bootstrap`, `sync`, `push` | `{command, complete, workspace}` |
| `create` | `{tag, slug, workspace, state, repositories:[{name, path, base}]}` |
| `list` | `[{tag, slug, workspace, state, created_at, repository_count, merged_count, removed_count}]` |
| `status` | `{tag, slug, workspace, state, created_at, repositories, hooks, backups, recovery_directory, pending_sync}` |
| `merge` | `{tag, state, repositories:[{name, merged, source, target}], backups}` |
| `drop` | `{tag, state, repositories:[{name, removed}], backups}` |
| `prune` | `{complete, workspace, actions:[{repository, action, target, status, detail?}], remaining_issues:[...]}` |
| `version` | `{version, commit}` |
| Lifecycle `--dry-run` | `{command, dry_run, force, skipped_hook_phases, skip_git_hooks, tag?, workspace?, resources}` |
| Error with `--json` | `{error:{message}}` on stderr with a nonzero exit status |

Command results use stdout; progress and hook output use stderr, including with `--json`. Progress identifies the operation, repository, and filesystem location as `[operation/repository @ path] message`. Hook lifecycle and output lines use `[hook/repository/phase/hook-id @ execution-path] message`. Logs are deterministic and omit timestamps, remote URLs, command bodies, and environment variables. Dry runs emit only their stdout operation plans and do not emit simulated progress. See the [configuration and hook reference](docs/configuration.md), [recovery guidance](docs/recovery.md), [contributor guide](CONTRIBUTING.md), and [security policy](SECURITY.md).

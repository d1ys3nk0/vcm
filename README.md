# Vibe Change Manager

VCM manages a Git workspace root and its child repositories as one Change. It bootstraps repositories, synchronizes trunks, creates isolated worktrees, runs project hooks, squash-merges locally, and records recovery state. Project policy belongs in configured hooks. Git is required; configured hooks additionally require their selected runner.

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
vcm create improve-search
# Or create the root plus an exact subset of children:
vcm create improve-search --only api
vcm list
```

The root and child trunks need configured `origin` remotes. Before creation, publish the initial configuration through your normal Git workflow so synchronization can rebase onto its remote trunk. Ignore each configured child checkout path in the root repository.

Make and commit changes in the returned sibling root and its child worktrees. Use `vcm status` within the Change root, then explicitly run `vcm merge` when ready to integrate into local trunks. Merge does not fetch, pull, or push. Run `vcm drop` to discard an unneeded clean, merged Change; inspect `--dry-run` before forced removal.

## Commands

| Command | Purpose |
| --- | --- |
| `validate` | Check configuration and dependency graph |
| `bootstrap` | Clone missing repositories and validate existing origins |
| `sync [--force]` | Fast-forward child trunks; force resets with recovery backups |
| `create <slug> [--only names] [--except names]` | Synchronize origins and create a full or partial Change |
| `list` | List recorded Changes |
| `status [change]` | Inspect lifecycle, hooks, merge, and recovery state |
| `merge [change]` | Run hooks and squash-merge child repositories, then root |
| `drop [change] [--force]` | Remove owned Change resources |
| `version` | Show version and source commit |

`--workspace PATH` selects a root; otherwise discovery walks upward for `vcm.yml` at a Git root. Commands produce concise human-readable text and tables by default. Every executable command accepts `--json` for a typed machine-readable result; help remains plain text. Mutating commands support `--dry-run`, which executes no hooks. Flags may appear before or after command arguments. `create --only core,web` selects exactly those children; `create --except devtools` selects every configured child except `devtools`. The root is always selected, the flags are mutually exclusive, and a selected repository whose dependency is not selected is rejected. Excluding every child creates a root-only Change. Change selection accepts a managed tag or root path. Without a selection, run from the managed Change itself. Slugs use lowercase kebab-case letters and digits; Change tags add a UTC timestamp and are also branch names.

For example, `vcm list` prints Changes newest first and marks the Change containing the current directory with `@`:

```text
   Change                       State  Repos       Age  Workspace
@  260910120000-improve-search  ready  0/2 merged  2h   /work/product.260910120000-improve-search
```

JSON output is command-specific and does not expose the persisted state. Timestamps use RFC 3339 UTC and Git revisions remain unabbreviated. State version 1 records only lifecycle and recovery checkpoints keyed by the repositories selected for the Change. Configuration and derived identities are resolved from the current `vcm.yml` and state filename instead of being copied into state. The `vcm.yml` format is also version 1.

| Result | JSON shape |
| --- | --- |
| `validate` | `{valid, workspace}` |
| `bootstrap`, `sync` | `{command, complete, workspace}` |
| `create` | `{tag, slug, workspace, state, repositories:[{name, path, base}]}` |
| `list` | `[{tag, slug, workspace, state, created_at, repository_count, merged_count, removed_count}]` |
| `status` | `{tag, slug, workspace, state, created_at, repositories, hooks, backups, recovery_directory, pending_sync}` |
| `merge` | `{tag, state, repositories:[{name, merged, source, target}], backups}` |
| `drop` | `{tag, state, repositories:[{name, removed}], backups}` |
| `version` | `{version, commit}` |
| Any `--dry-run` | `{command, dry_run, force, tag?, workspace?, resources}` |
| Error with `--json` | `{error:{message}}` on stderr with a nonzero exit status |

Command results use stdout; progress and hook output use stderr. See the [configuration and hook reference](docs/configuration.md), [recovery guidance](docs/recovery.md), [contributor guide](CONTRIBUTING.md), and [security policy](SECURITY.md).

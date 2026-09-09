# Vibe Change Manager

VCM manages a Git workspace and its downstream repositories as one Change. It bootstraps repositories, synchronizes trunks, creates isolated worktrees, runs project hooks, squash-merges locally, and records recovery state. Project policy belongs in configured hooks. Git and `/bin/sh` must be installed.

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

Create `workspace.yml` in an existing Git repository with a committed trunk:

```yaml
version: 1
trunk: main
repositories:
  - name: api
    path: repos/api
    url: git@github.com:example/api.git
    trunk: main
```

```sh
printf '/repos/api/\n' >> .gitignore
git add workspace.yml .gitignore
git commit -m 'chore: configure VCM workspace'
vcm validate
vcm bootstrap
vcm create improve-search
vcm list
```

The workspace and downstream trunks need configured `origin` remotes. Before creation, publish the initial workspace configuration through your normal Git workflow so synchronization can rebase onto its remote trunk. Ignore each configured downstream checkout path in the workspace repository.

Make and commit changes in the returned sibling workspace and its nested repository worktrees. Use `vcm status` within the Change workspace, then explicitly run `vcm merge` when ready to integrate into local trunks. Merge does not fetch, pull, or push. Run `vcm drop` to discard an unneeded clean, merged Change; inspect `--dry-run` before forced removal.

## Commands

| Command | Purpose |
| --- | --- |
| `validate` | Check configuration and dependency graph |
| `bootstrap` | Clone missing repositories and validate existing origins |
| `sync [--force]` | Fast-forward downstream trunks; force resets with recovery backups |
| `create <slug>` | Synchronize origins and create a complete Change |
| `list` | List recorded Changes |
| `status [change]` | Inspect lifecycle, hooks, merge, and recovery state |
| `merge [change]` | Run hooks and squash-merge downstream repositories, then workspace |
| `drop [change] [--force]` | Remove owned Change resources |
| `version` | Show version and source commit |

`--workspace PATH` selects a workspace; otherwise discovery walks upward for `workspace.yml` at a Git root. `--json` produces machine-readable results. Mutating commands support `--dry-run`, which executes no hooks. Flags may appear before or after command arguments. Change selection accepts a managed tag or workspace path. Without a selection, run from the managed Change itself. Slugs use lowercase kebab-case letters and digits; Change tags add a UTC timestamp and are also branch names.

Command results use stdout; progress and hook output use stderr. See the [configuration and hook reference](docs/configuration.md), [recovery guidance](docs/recovery.md), [contributor guide](CONTRIBUTING.md), and [security policy](SECURITY.md).

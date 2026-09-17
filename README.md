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

## Everyday workflow

VCM treats a named Change as one unit of work across a root repository and selected children. Git remains the source of history; project policy belongs in configured hooks.

```sh
vcm pull
vcm create improve-search --only api,web
vcm status
# Edit and commit in the Change repositories.
vcm refresh
vcm merge --message "feat: improve search"
vcm push
```

Creation uses clean local trunks and does not contact remotes. Run `pull` explicitly when fresh remote work is needed. Merge integrates locally; publication is a separate operation. Creation preflights every selected repository before creating resources, records local baselines, and includes commits produced by `create-before` hooks. Every canonical checkout must remain on its configured trunk.

Enable navigation and completion in Bash or Zsh:

```sh
eval "$(vcm shell init bash)" # use zsh for Zsh
vcm switch improve-search
vcm switch --base
```

Shell integration enters successfully created Changes and returns to the canonical root when merge or drop removes the invoking shell's checkout, even if finalization later fails. `--no-cd` suppresses navigation. Without integration, use `vcm path improve-search` to obtain a path. JSON commands never change the shell directory. Shell initialization prints code for you to evaluate; it does not edit startup files.

## Setup

Run `vcm init` inside an existing canonical Git repository with at least one commit. It creates only `vcm.yml`, using the current branch or an explicit existing `--trunk`. Detached HEAD requires `--trunk`; linked worktrees and existing configuration are rejected.

Configure children and ignore their checkout paths in the root repository:

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

Commit the configuration and ignore entries through your normal Git workflow, configure the root `origin`, then run:

```sh
vcm check --config-only
vcm bootstrap
vcm status
```

Root and child repositories need configured origins. Child dependencies determine creation and integration order. `create --only api,web` selects exactly those children; `--except tools` excludes children. Dependencies must already be included in the selection; the root is always selected. See [configuration and hooks](docs/configuration.md).

## Commands

| Command | Purpose |
| --- | --- |
| `create <name>` | Create from local trunks; supports `--only` or `--except` |
| `switch [change]`, `switch --base` | Enter an existing checkout through shell integration |
| `path [change]`, `path --base` | Print only the absolute checkout path |
| `status [change]` | Inspect the effective base workspace or selected Change |
| `list [--all]` | Show active Changes; include completed records with `--all` |
| `refresh [change]` | Merge local trunks into a Change, children first and root last |
| `merge [change]` | Gate, squash into local trunks, clean up, and finalize |
| `drop [change]` | Remove an unneeded Change; `--force` backs up and discards content |
| `fetch` | Update cached remote trunk refs without moving local branches |
| `pull` | Fetch and rebase clean canonical trunks, children first and root last |
| `push` | Validate all repositories, then publish children and finally root |
| `init [--trunk BRANCH]` | Create minimal root-only configuration |
| `bootstrap` | Clone missing configured children and check existing origins |
| `check [--config-only]` | Audit workspace resources or validate configuration alone |
| `prune` | Interactively clean unexpected resources; preview with `--dry-run` |
| `recover [change]` | Authorize an inspected interrupted hook's retry |
| `shell init bash\|zsh` | Print shell integration and completions |
| `version` | Print build version and source commit |

Use `vcm <command> --help` for the command's own options. Flags can precede or follow arguments; `--` ends flag parsing. Global options are `--workspace PATH`, `--json`, `--color auto|always|never`, and `--help`.

Change selectors accept unique names, exact timestamped tags, and paths. An exact tag wins; ambiguous names list candidates and require a tag. Explicit relative paths resolve from `--workspace` or the current directory. Omitted selectors use that same context, including nested child directories. `status` also works from base; operations that require a Change need a selector there. Historical tags remain usable after removal for inspection and finalization; navigation requires an owned checkout that still exists.

## Inspection and previews

`status` separates working-tree cleanliness, comparison target, ahead/behind counts, and lifecycle progress. Base comparisons use cached `origin/<checked-out-branch>` refs; Change comparisons use configured local trunks. Neither inspection command fetches or runs hooks. Use `--verbose` for paths, checkpoints, completed hooks, unselected repositories, and recovery details. Dirty or divergent state is normal report data; failed inspection returns nonzero with the report. Missing, removed, and deliberately unselected repositories are distinguished.

`list` shows name, repository count, dirty repository count, local base update state, operation, and age. Names expand to tags when ambiguous; `@` marks the effective context. Completed records are labeled merged or discarded. Unknown inspection data is never presented as clean.

Mutating commands support `--dry-run` (navigation is read-only and needs none). Previews show ordered repository effects, hooks, targets, cleanup, backups, and local blockers. They do not fetch, execute hooks, reconcile journals, or write state. Remote freshness, hook results, and future conflicts cannot be proven by a local preview. A known blocker produces a nonzero exit status with the plan.

## Integration and cleanup

Merge defaults its Conventional Commit subject to `feat: <Change name>`; `--message` overrides it before integration starts. Each changed repository gets one squash commit with a list of source commits. The recorded subject is reused on retry. No branch publication happens during merge.

After integration is checkpointed, merge removes owned worktrees including ignored content, without backup. Dirty tracked or nonignored untracked files, ownership/revision drift, foreign nested repositories, and conflicts still stop the affected operation. Drop rejects working or unmerged content unless `--force` is provided; forced drop preserves filesystem and Git recovery backups.

Merge overrides have explicit independent meanings: `--ignore-hook-failures` tolerates command failures only when the lifecycle hook leaves its checkout clean; `--skip-hooks merge-before,merge-after` bypasses selected phases; `--skip-hook-git-hooks` disables Git hooks only within merge lifecycle hook subprocesses. None relax ownership or cleanup safety checks.

Pull preflights all canonical repositories and rejects a rebase that would rewrite an active Change's recorded baseline. Push fetches and validates every repository before publishing any; it rejects behind/divergent histories and never force-pushes. Multi-repository publication is not atomic. Failures report completed, blocked, and pending publication; repair the failure and retry.

`check` reports dirty checkouts, missing or mismatched resources, and unexpected local branches/worktrees. `prune --dry-run` inventories candidates; real pruning asks separately before each reset, worktree removal, and branch deletion. Prune does not create recovery backups. See [recovery](docs/recovery.md) before destructive cleanup.

## Output and migration

Human results use stdout; progress and errors use stderr. `--json` emits command-specific objects without ANSI escapes. Inspection JSON always includes full available details, independent of `--verbose`; unavailable values are omitted with a working-tree state or diagnostic. `list` returns `{changes:[...]}`. `status` returns workspace/context/repositories plus lifecycle details for Changes. Errors have stable `code` and `message`, with repository, Change, operation progress, and next action when known. Exit status is 0 for success and 1 for errors or incomplete audits/plans.

This is a breaking CLI and JSON revision with no compatibility aliases:

| Previous interface | Replacement |
| --- | --- |
| `sync` | `fetch` |
| `tree` | `status` |
| `validate` | `check --config-only` |
| `merge --force` / `merge -f` | `merge --ignore-hook-failures` |
| `merge --skip-git-hooks` | `merge --skip-hook-git-hooks` |
| Implicit remote update during creation | Explicit `pull` before `create` |
| Change tags only / context-only refresh | Names, tags, or paths consistently |
| All recorded Changes in `list` | Active by default; `list --all` for history |

New manifests use state version 3; configuration stays version 1. Completed creation records from versions 1/2 remain readable and upgrade on authorized mutation. Finish or discard unfinished legacy creations with the previous binary before mutation with this version. Legacy synchronization journals are preserved and block mutation; they are never silently reconciled or deleted. See [state recovery](docs/recovery.md).

## Development

Run `task verify` for formatting, static analysis, race tests, installer tests, and the pinned vulnerability scanner. Build with `task build`. See [CONTRIBUTING.md](CONTRIBUTING.md).

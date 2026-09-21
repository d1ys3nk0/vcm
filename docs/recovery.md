# Recovery

VCM persists intent before each resource mutation. Repair the reported condition and rerun the same command with the exact Workspace ID or workspace path; completed checkpoints are not repeated.

## Harness adoption

For a detached external root, VCM completes all non-mutating validation and child collision checks, persists the Workspace ID, Workspace Name, origin revision, and branch custody, and only then creates the root branch. A stopped process can therefore resume without inventing a second identity or leaving an untracked branch.

Repeated harness startup and resume events are safe: the adapter ignores the canonical checkout, no-ops for a ready managed path, and resumes an interrupted `creating` record for that exact root path.

If automatic detached-root naming collides, VCM does not append a suffix. Choose a stable name explicitly:

```sh
vcm create <name> --existing-root <cwd>
```

If an external root disappears before release, restore the registered linked worktree at its recorded path and retry. VCM never treats premature disappearance as successful cleanup. After merge, drop, or cleanup marks the root released, harness deletion is valid.

## Hook interruption

A failed hook preserves its status and error. A hook recorded as running may have produced external effects; inspect those effects before authorizing retry with:

```sh
vcm recover <workspace-id> --retry-hook <repository/phase/id> --acknowledge-effects
```

Configuration adoption and hook retry are separate operations. Use `--dry-run` where supported.

## Merge and cleanup

Merge freezes source and target revisions before changing trunks. It integrates children in dependency order and the root last, then releases worktrees. Retry preserves the recorded merge subject and does not duplicate completed integration.

`merge --keep` records retention before integration. Run `cleanup <workspace-id>` later; cleanup verifies source revisions and that integration commits remain reachable before removing VCM-owned resources. External root directories and externally owned branches are never removed.

`drop --force` is destructive to workspace content but creates filesystem and Git recovery evidence first. Inspect the dry run and affected resources before confirming.

## Legacy boundary

State version 5 is a hard cut. Active version 4 workspaces, interrupted legacy creation, and legacy synchronization journals must be completed or abandoned with the previous VCM binary. Version 5 permits exact read-only inspection of completed version 4 history by legacy tag or path; it never mutates that history.

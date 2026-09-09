# Recovery

Start with `vcm status <change>`. Keep the original repository set and Change paths intact while repairing an interrupted operation. Lifecycle manifests under the workspace Git common directory record configuration, ownership, bases, hooks, merge targets, and cleanup progress. These local files are operational state, not project artifacts; do not execute them or edit them to bypass ownership checks.

VCM serializes mutations with an operating-system workspace lock. If another invocation is active, let it finish. The lock is released when its process exits; the lock file may remain and should not be deleted to bypass an active lock.

For a failed hook, inspect stderr and the relevant Change checkout. Repair and commit intended output, ensure the checkout is clean, and retry the same command. An interrupted hook remains recorded as `running`: after inspecting its effects and confirming an idempotent retry is safe, change only that hook outcome's `status` to `failed` in the recorded manifest, preserving file permissions, then retry. Do not change ownership or revision fields. VCM does not promise exactly-once external effects.

For an interrupted merge, inspect both the Change source and origin trunk. Completed downstream merges remain in place if a later hook or workspace merge fails. Retry the same Change after repairing the reported problem; do not reset completed targets or advance source branches behind the manager's recorded checkpoints. Unexpected source or target revisions require explicit investigation.

Ordinary synchronization rejects local divergence. `sync --force` and `drop --force` can discard working content; inspect their dry-run output and the affected resources first. Recovery backups preserve Git history and discarded filesystem content before destructive work. Keep these backups until the result is verified. A backup is recovery evidence, not authorization to overwrite unrelated work.

Pending synchronization journals are stored as `vcm/*.sync` under the workspace Git common directory. After an interrupted nontrivial rebase, the current revision can differ from both the recorded starting revision and remote target even if the rebase completed. VCM rejects this ambiguous outcome rather than accepting it automatically. Inspect the journal and Git history, preserve any completed rebased history with a recovery branch or tag, then explicitly repair the checkout to the reported recorded starting revision or target before retrying. Resolve unfinished Git operations and preserve working content before any reset.

Ordinary drop also rejects ignored filesystem content. Forced drop preserves discarded content in recovery backups, but still rejects foreign nested Git repositories; relocate those repositories explicitly before retrying.

VCM only removes resources it recorded as owned. If paths, branches, or worktrees were replaced manually, restore the expected ownership or resolve the discrepancy explicitly. Never delete an unfamiliar path merely because its name resembles a Change.

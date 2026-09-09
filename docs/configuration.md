# Configuration and hooks

`workspace.yml` uses configuration version `1`. The machine-readable reference is [workspace.schema.json](../schema/workspace.schema.json). Unknown fields are errors. `repositories` must be an explicit list; use `[]` for a workspace without downstream repositories. The CLI additionally validates filesystem path safety, Git branch names, and the dependency graph.

| Field | Meaning |
| --- | --- |
| `version` | Required integer `1` |
| `trunk` | Workspace integration branch |
| `hooks` | Optional workspace lifecycle hooks |
| `repositories` | Ordered downstream repository declarations |
| `repositories[].name` | Unique repository identity |
| `repositories[].path` | Explicit path relative to the workspace root |
| `repositories[].url` | Expected Git origin URL |
| `repositories[].trunk` | Downstream integration branch |
| `repositories[].depends_on` | Optional list of repository names |
| `repositories[].hooks` | Optional downstream lifecycle hooks |

Paths must remain inside the workspace and must not overlap or escape through symlinks. Repository identities must be unique; dependencies must exist and form an acyclic graph. Git branch names must be valid. Dependencies precede dependents during bootstrap, creation, and merging; declaration order resolves ties. Cleanup reverses that order. Every Change includes every configured repository.

```yaml
version: 1
trunk: main
hooks:
  pre-merge:
    - id: verify
      command: ./scripts/verify-change.sh
  post-merge:
    - id: archive
      command: ./scripts/archive-change.sh
repositories:
  - name: api
    path: repos/api
    url: git@github.com:example/api.git
    trunk: main
    hooks:
      create:
        - id: prepare
          command: ./scripts/prepare-change.sh
  - name: web
    path: repos/web
    url: git@github.com:example/web.git
    trunk: main
    depends_on: [api]
```

Hook entries contain a stable `id` and shell `command`. Workspace phases are `create`, `pre-merge`, `post-merge`, and `drop`. Downstream phases are `create`, `merge`, and `drop`. Entries execute in declaration order with `/bin/sh -eu` in the relevant Change checkout. Commands can use these environment variables:

| Variable | Meaning |
| --- | --- |
| `VCM_CHANGE_TAG` | Timestamped Change identity and branch name |
| `VCM_CHANGE_SLUG` | User-supplied Change slug |
| `VCM_WORKSPACE` | Change workspace root |
| `VCM_WORKSPACE_ORIGIN` | Original workspace root |
| `VCM_REPOSITORY_NAME` | Repository name, or `workspace` for root hooks |
| `VCM_REPOSITORY_ORIGIN` | Original repository checkout |
| `VCM_REPOSITORY_PATH` | Relevant Change checkout |

A downstream create hook runs immediately after that worktree exists. The workspace create hook runs after the complete workspace exists. Merge runs workspace pre-merge, downstream merge hooks and squash-merges in dependency order, then workspace post-merge and the final workspace squash-merge. Workspace drop hooks run while the complete workspace still exists; downstream drop hooks and cleanup follow in reverse dependency order.

Hooks own staging and committing their output. Successful hooks must leave their checkout clean. Failures preserve files for inspection and repair. Hooks must be idempotent: a process interruption can leave external effects whose completion VCM cannot determine. Treat hooks as trusted executable project code.

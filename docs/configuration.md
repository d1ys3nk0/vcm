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
    pre-merge:
      - id: verify
        shell: |
          ./scripts/verify-change.sh
    post-merge:
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
      create:
        - id: prepare
          shell: ./scripts/prepare-change.sh
  - name: web
    path: repos/web
    url: git@github.com:example/web.git
    trunk: main
    depends_on: [api]
```

Each hook has a stable `id` and exactly one nonblank `shell` or `python` body. Root phases are `create`, `pre-merge`, `post-merge`, and `drop`. Child phases are `create`, `merge`, and `drop`. Entries execute in declaration order in the relevant Change checkout. Shell bodies run as `<runners.shell> -eu -o pipefail -c <body>`; Python bodies run as `<runners.python> -c <body>`.

| Variable | Meaning |
| --- | --- |
| `VCM_CHANGE_TAG` | Timestamped Change identity and branch name |
| `VCM_CHANGE_SLUG` | User-supplied Change slug |
| `VCM_ROOT` | Change root checkout |
| `VCM_ROOT_ORIGIN` | Original root checkout |
| `VCM_REPOSITORY_NAME` | Child name, or `root` for root hooks |
| `VCM_REPOSITORY_ORIGIN` | Original repository checkout |
| `VCM_REPOSITORY_PATH` | Relevant Change checkout |
| `VCM_HOOK_PHASE` | Current lifecycle phase |
| `VCM_HOOK_ID` | Current hook identity |

A selected child create hook runs immediately after that worktree exists. The root create hook runs after all selected worktrees exist. Merge runs root pre-merge hooks, selected child merge hooks and squash-merges in dependency order, then root post-merge hooks and the final root squash-merge. Root drop hooks run while all selected resources still exist; selected child drop hooks and cleanup follow in reverse dependency order.

Hooks own staging and committing their output. Successful hooks must leave their checkout clean. Failures preserve files for inspection and repair. Hooks must be idempotent: a process interruption can leave external effects whose completion VCM cannot determine. Treat hooks as trusted executable project code.

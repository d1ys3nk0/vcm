# Contributing

Use Go 1.27.1, Git, and a POSIX shell. Version managers such as `mise` can install the Go version. Install `pre-commit` for the local lint wrapper.

```sh
go mod download
make build
make lint
make test
make vuln
make release VERSION=v0.0.0
```

Release cross-builds require a missing `dist` directory to avoid mixing versions. Move or remove a previous build deliberately before rerunning. `bin/` and `dist/` are ignored.

Keep configuration validation and operation planning separate from Git, hooks, and persistence. Add behavioral regression tests with temporary local repositories and remotes. Exercise interrupted operations and preserved user resources. Do not add tests that freeze configurable values or private implementation details. Tests must not start dependency services or use real product checkouts.

CI runs formatting checks, `go vet`, race-enabled tests, installer behavioral tests, vulnerability scanning, and four release cross-builds directly on Linux and macOS. Local lint hooks live in `.pre-commit-config.yaml`; `make lint` applies manual fixers before checks.

Use focused Conventional Commits. Publishing is a separate operator action: only explicit `vX.Y.Z` tags whose commits belong to `main` can release. The release workflow repeats validation, builds archives without write permissions, then publishes checksums, the installer, and provenance in a separate publishing job. Actions are pinned to immutable revisions.

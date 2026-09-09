PRE_COMMIT ?= pre-commit

.PHONY: build test lint lint-fix lint-chk vuln release
build:
	go build -o bin/vcm ./cmd/vcm
test:
	go test -race ./...
	sh scripts/test-installer.sh
lint: lint-fix lint-chk
lint-chk:
	$(PRE_COMMIT) run --hook-stage pre-commit --all-files
lint-fix:
	$(PRE_COMMIT) run --hook-stage manual --all-files >/dev/null || true
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
release:
	sh scripts/build-release.sh "$(VERSION)"

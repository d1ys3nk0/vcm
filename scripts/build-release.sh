#!/bin/sh
set -eu
version=${1:?usage: build-release.sh vX.Y.Z [commit]}
commit=${2:-$(git rev-parse HEAD)}
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || exit 1
if [ -e dist ]; then
    printf 'dist already exists; move or remove it before building release artifacts\n' >&2
    exit 1
fi
mkdir dist
for os in darwin linux; do
    for arch in amd64 arm64; do
        output="dist/vcm_${version#v}_${os}_${arch}"
        mkdir -p "$output"
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version -X main.commit=$commit" -o "$output/vcm" ./cmd/vcm
        cp LICENSE "$output/LICENSE"
        COPYFILE_DISABLE=1 tar -czf "$output.tar.gz" -C "$output" vcm LICENSE
        rm -rf "$output"
    done
done
cp install.sh dist/install.sh
(cd dist && if command -v sha256sum >/dev/null; then sha256sum ./*.tar.gz install.sh; else shasum -a 256 ./*.tar.gz install.sh; fi) | sed 's|  ./|  |' > dist/SHA256SUMS

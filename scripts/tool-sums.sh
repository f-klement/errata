#!/bin/sh
# tool-sums.sh: rewrite the Makefile's *_SHA256_* lines for the pinned versions.
#
# Run it after changing a *_VERSION line, which is what a Renovate merge
# request for a tool does. It reads each project's own published checksum file
# at that version, so the hashes come from the release, not from a download
# this machine happened to receive.
set -eu
MK="$(cd "$(dirname "$0")/.." && pwd)/Makefile"
ver() { sed -n "s/^$1_VERSION *:= *v\{0,1\}\([^ ]*\)$/\1/p" "$MK"; }
sum() { awk -v f="$2" '$2 == f { print $1 }' "$1"; }
set_sum() {
    [ -n "$2" ] || { echo "no checksum for $1 in the published file" >&2; exit 1; }
    sed -i "s/^\($1 *:= *\)[0-9a-f]*$/\1$2/" "$MK"
}
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

v=$(ver GOLANGCI_LINT)
curl -sSfL -o "$T/c" "https://github.com/golangci/golangci-lint/releases/download/v$v/golangci-lint-$v-checksums.txt"
set_sum GOLANGCI_LINT_SHA256_linux_amd64 "$(sum "$T/c" "golangci-lint-$v-linux-amd64.tar.gz")"
set_sum GOLANGCI_LINT_SHA256_linux_arm64 "$(sum "$T/c" "golangci-lint-$v-linux-arm64.tar.gz")"

v=$(ver GREMLINS)
curl -sSfL -o "$T/c" "https://github.com/go-gremlins/gremlins/releases/download/v$v/checksums.txt"
set_sum GREMLINS_SHA256_linux_amd64 "$(sum "$T/c" "gremlins_${v}_linux_amd64.tar.gz")"
set_sum GREMLINS_SHA256_linux_arm64 "$(sum "$T/c" "gremlins_${v}_linux_arm64.tar.gz")"

v=$(ver GORELEASER)
curl -sSfL -o "$T/c" "https://github.com/goreleaser/goreleaser/releases/download/v$v/checksums.txt"
set_sum GORELEASER_SHA256_linux_amd64 "$(sum "$T/c" goreleaser_Linux_x86_64.tar.gz)"
set_sum GORELEASER_SHA256_linux_arm64 "$(sum "$T/c" goreleaser_Linux_arm64.tar.gz)"

v=$(ver SYFT)
curl -sSfL -o "$T/c" "https://github.com/anchore/syft/releases/download/v$v/syft_${v}_checksums.txt"
set_sum SYFT_SHA256_linux_amd64 "$(sum "$T/c" "syft_${v}_linux_amd64.tar.gz")"
set_sum SYFT_SHA256_linux_arm64 "$(sum "$T/c" "syft_${v}_linux_arm64.tar.gz")"

v=$(ver KO)
curl -sSfL -o "$T/c" "https://github.com/ko-build/ko/releases/download/v$v/checksums.txt"
set_sum KO_SHA256_linux_amd64 "$(sum "$T/c" "ko_${v}_Linux_x86_64.tar.gz")"
set_sum KO_SHA256_linux_arm64 "$(sum "$T/c" "ko_${v}_Linux_arm64.tar.gz")"

echo "checksums match the published files for the pinned versions"

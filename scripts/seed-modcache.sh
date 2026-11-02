#!/bin/sh
# seed-modcache.sh: fill the in-tree module cache from another, for `make ci-local`.
#
# Usage: scripts/seed-modcache.sh <from GOMODCACHE> <to GOMODCACHE>, from the module root.
#
# `make ci-local` keeps the Go caches inside the project, as CI's .go job does, so through the
# runner it would download every module again although the node's /cache holds them. This fills
# <to> with go itself, from <from>'s downloads, <from>/cache/download, first on GOPROXY as a file
# proxy and the proxy go would use otherwise after it: `go list -m all` takes the go.mod of every
# module in the graph, and `go mod download` the zip of each module go.sum holds a zip hash for.
# That is what the gate reads and nothing else <from> holds; `go mod download all` would also
# take zips the build never opens and write their hashes into go.sum, which the gate's
# go mod tidy -diff then refuses. A module <from> lacks comes from the network as on a cold start. go hashes
# each zip and go.mod it takes from the file proxy against go.sum, as it does a download from the
# network, so a zip changed in <from> stops the seed with go's checksum error.
#
# The build and lint caches are not seeded, since Go keys a package's build by its directory
# and the in-tree module cache moves every dependency (ci/README.md, The gate in CI). A <from>
# without downloads leaves <to> to start cold.
set -eu

if [ "$#" -ne 2 ]; then
    echo "seed-modcache: REFUSED: usage: scripts/seed-modcache.sh <from GOMODCACHE> <to GOMODCACHE>" >&2
    exit 2
fi
src=$1/cache/download
if [ ! -d "$src" ]; then
    echo "seed-modcache: $src does not exist; $2 starts cold"
    exit 0
fi
GO=${GO:-go}
start=$(date +%s)
proxy="file://$src,$($GO env GOPROXY)"
# go list and go mod download may add hashes to go.sum for what they load, so the tree's go.sum
# is put back on every exit, a failed or interrupted go call's included: the gate's
# go mod tidy -diff reads it, and run by hand the tree is the developer's.
sum=$(mktemp)
cp go.sum "$sum"
trap 'cmp -s "$sum" go.sum || cp "$sum" go.sum; rm -f "$sum"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
GOMODCACHE=$2 GOFLAGS=-modcacherw GOPROXY=$proxy $GO list -m all >/dev/null
# shellcheck disable=SC2046
GOMODCACHE=$2 GOFLAGS=-modcacherw GOPROXY=$proxy \
    $GO mod download $(awk '$2 !~ /\/go\.mod$/ { print $1 "@" $2 }' "$sum" | sort -u)
echo "seed-modcache: $2 holds the modules go.sum names, $(du -sm "$2/cache/download" | cut -f1) MiB of downloads, from $src in $(($(date +%s) - start)) s"

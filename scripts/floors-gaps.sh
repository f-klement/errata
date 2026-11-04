#!/bin/sh
# floors-gaps.sh: what scripts/mutation-floors.txt and the tree disagree on.
#
# Usage: scripts/floors-gaps.sh [<table>]    the table defaults to scripts/mutation-floors.txt
#
# Prints one line for each package mutation judges that has no row in the table, each row for a
# package that is not there, and each package with two rows, and exits 1 when it prints any; 0
# when there is none. A package mutation judges is one `go list` finds under internal/ that
# .gremlins.yaml does not exclude whole, as scripts/mutation.sh judges it. It needs only go, not
# gremlins, so `make precheck` runs it: a new package without its row went RED in a gate on
# 2026-11-03, 16 minutes in, where precheck could have refused it in seconds. ci/tests/mutation.sh
# holds it to the real tree and to a table with each gap planted. GO names the go binary, as in
# the Makefile.
set -eu

R=$(cd "$(dirname "$0")/.." && pwd)
table=${1:-$R/scripts/mutation-floors.txt}
GO=${GO:-go}
[ -r "$table" ] || { echo "floors-gaps: REFUSED: $table cannot be read" >&2; exit 2; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

module=$(cd "$R" && "$GO" list -m)
(cd "$R" && "$GO" list ./internal/...) | sed "s|^$module/||" | while IFS= read -r p; do
    d=$p
    while :; do
        grep -qxF "    - ^$d/" "$R/.gremlins.yaml" && break
        case "$d" in */*) d=${d%/*} ;; *) echo "$p"; break ;; esac
    done
done | LC_ALL=C sort -u >"$tmp/pkgs"
[ -s "$tmp/pkgs" ] || { echo "floors-gaps: REFUSED: go list found no package under internal/ to judge" >&2; exit 2; }

sed -e 's/#.*//' "$table" | awk 'NF { print $1 }' | LC_ALL=C sort >"$tmp/rows"
{
    LC_ALL=C sort -u "$tmp/rows" | LC_ALL=C comm -13 - "$tmp/pkgs" | sed 's/^/no floors row for /'
    LC_ALL=C sort -u "$tmp/rows" | LC_ALL=C comm -23 - "$tmp/pkgs" | sed 's/^/a floors row for no package: /'
    uniq -d "$tmp/rows" | sed 's/^/two floors rows for /'
} >"$tmp/gaps"
cat "$tmp/gaps"
[ ! -s "$tmp/gaps" ]

#!/bin/sh
# fuzz-input.sh: keep the input of a fuzz failure from a tree that is thrown away.
#
#   scripts/fuzz-input.sh run <package> <target> <command...>
#   scripts/fuzz-input.sh recover [<log>]
#
# When a fuzz target fails, go test writes the failing input to
# <package>/testdata/fuzz/<target>/<name>. In a runner's pod or a CI job that tree is gone once
# the run ends, and with it the one input that shows the failure: four fuzz finds of the v0.4.0
# wave were lost that way.
#
# run: runs the command, which `make fuzz` gives as the go test -fuzz of <target> in <package>
# (./internal/x, as go test takes it). When it fails, prints each file the run added under the
# target's corpus directory, base64 encoded between two marker lines that name its path from
# the repository's root:
#
#   ==> fuzz input begin internal/x/testdata/fuzz/FuzzX/0123456789abcdef
#   <base64>
#   ==> fuzz input end internal/x/testdata/fuzz/FuzzX/0123456789abcdef
#
# Then it exits with the command's status. A run that passes prints nothing more.
#
# recover: reads a log, or standard input, and writes each file between markers at its path
# under the current directory, so `go test -run=<target>/<name> ./<package>` runs it again. It
# reads a runner's log, a gate's fuzz.log or a CI job's log, with carriage returns and colour
# codes in it. It refuses, exit 2, a path that is not a corpus file of a package under
# internal/, and a file whose content does not hash to its name, which a log cut short would
# give: go names each corpus file after the first 16 hex digits of its content's SHA-256. It
# exits 1 when the log holds no marked input.
set -eu

usage () {
    echo "usage: scripts/fuzz-input.sh run <package> <target> <command...>" >&2
    echo "       scripts/fuzz-input.sh recover [<log>]" >&2
    exit 2
}
refuse () {
    echo "fuzz-input: REFUSED: $1" >&2
    exit 2
}

# The SHA-256 of standard input, by sha256sum, or by shasum -a 256 where it is missing, as on
# macOS without coreutils.
sha256 () {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -d' ' -f1
}

# corpus_files <dir>: the files under <dir>, one per line, sorted; nothing when it is missing.
corpus_files () {
    [ ! -d "$1" ] || find "$1" -type f | LC_ALL=C sort
}

# run: the before and after listings are written to a temporary directory, since a shell
# variable would drop a trailing newline and comm needs files.
run () {
    [ $# -ge 3 ] || usage
    pkg=${1#./}
    pkg=${pkg%/}
    target=$2
    shift 2
    dir="$pkg/testdata/fuzz/$target"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    corpus_files "$dir" >"$tmp/before"
    rc=0
    "$@" || rc=$?
    [ "$rc" != 0 ] || return 0
    corpus_files "$dir" >"$tmp/after"
    LC_ALL=C comm -13 "$tmp/before" "$tmp/after" >"$tmp/new"
    if [ ! -s "$tmp/new" ]; then
        echo "fuzz-input: $target failed and wrote no new file under $dir"
        exit "$rc"
    fi
    while IFS= read -r f; do
        echo "==> fuzz input begin $f"
        base64 <"$f"
        echo "==> fuzz input end $f"
    done <"$tmp/new"
    echo "fuzz-input: recover with scripts/fuzz-input.sh recover <this log>, from the repository's root"
    exit "$rc"
}

# A corpus file of a package under internal/: letters, digits, _ and - in each directory, so
# no .. and no absolute path; a Fuzz target's directory; a name of 16 hex digits.
corpus_path () {
    printf '%s\n' "$1" | grep -qE '^internal(/[A-Za-z0-9_-]+)+/testdata/fuzz/Fuzz[A-Za-z0-9_]*/[0-9a-f]{16}$'
}

# recover: the log's lines with colour codes and carriage returns removed go to a file, and each
# begin marker's lines up to its end marker are decoded from it.
recover () {
    [ $# -le 1 ] || usage
    if [ $# = 1 ]; then
        [ -r "$1" ] || refuse "cannot read $1."
        src=$1
    else
        src=-
    fi
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    esc=$(printf '\033')
    cr=$(printf '\r')
    cat -- "$src" | tr -d '\000' | sed -e "s/${esc}\[[0-9;]*[A-Za-z]//g" -e "s/${cr}\$//" >"$tmp/log"
    sed -n 's/^==> fuzz input begin //p' "$tmp/log" | awk '!seen[$0]++' >"$tmp/paths"
    [ -s "$tmp/paths" ] || { echo "fuzz-input: no fuzz input between markers in ${1:-standard input}" >&2; exit 1; }
    while IFS= read -r p; do
        corpus_path "$p" || refuse "'$p' is not a corpus file of a package under internal/, so nothing was written for it."
        awk -v b="==> fuzz input begin $p" -v e="==> fuzz input end $p" '
            $0 == b { on = 1; got = 1; next }
            $0 == e { if (on) done = 1; on = 0; exit }
            on { print }
            END { exit !(got && done) }' "$tmp/log" >"$tmp/b64" \
            || refuse "the input $p has no end marker, so the log was cut short."
        base64 -d <"$tmp/b64" >"$tmp/data" 2>/dev/null || refuse "the input $p is not base64."
        sum=$(sha256 <"$tmp/data" | cut -c1-16)
        [ "$sum" = "${p##*/}" ] || refuse "the input $p hashes to $sum, not to its name, so the log holds a different or partial file."
        mkdir -p "$(dirname "$p")"
        cp "$tmp/data" "$p"
        target=$(basename "$(dirname "$p")")
        pkg=${p%%/testdata/fuzz/*}
        echo "fuzz-input: wrote $p; run it with go test -run='^$target\$/^${p##*/}\$' ./$pkg"
    done <"$tmp/paths"
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case "$cmd" in
    run) run "$@" ;;
    recover) recover "$@" ;;
    *) usage ;;
esac

#!/bin/sh
# Claim: the Contents list of each Markdown file named lists every heading of
# level 2 and 3 that follows it, in order, each by its text and its anchor,
# indented two spaces for level 3, and nothing else.
# Usage: sh checks/contents.sh <file.md>...
# Exit 0 if true, 1 if false (check protocol, see internal/protocol/exec.go).
# A file named that is not there, or has no "## Contents" heading, has no list
# to test, which is unknown (exit 2). checks/markdown.awk reads fences,
# headings and anchors, as the link check reads them, so a heading inside a
# fenced example is none. awk failing, as on a missing checks/markdown.awk, is
# unknown (exit 2), never a verdict. ci/tests/contents.sh holds the cases.
here=$(dirname "$0")
[ "$#" -gt 0 ] || { echo "usage: contents.sh <file.md>..."; exit 2; }
for f in "$@"; do
    [ -f "$f" ] || { echo "$f: no such file"; exit 2; }
done
out=$(printf '%s\n' "$@" | LC_ALL=C awk -v q="'" -f "$here/markdown.awk" -f "$here/contents.awk") ||
    { echo "the contents check did not run: awk exited $?"; exit 2; }
verdict=$(printf '%s\n' "$out" | sed -n '$s/^verdict \([012]\)$/\1/p')
[ -n "$verdict" ] || { echo "the contents check did not run: awk printed no verdict"; exit 2; }
printf '%s\n' "$out" | sed '$d'
exit "$verdict"

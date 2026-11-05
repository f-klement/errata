#!/bin/sh
# Claim: every relative Markdown link points at a file that exists, and every
# anchor in one, after a #, names a heading of the Markdown file it points at.
# So does every pointer such as docs/reference.md#comment that a Go file
# writes, in a comment or in a message errata prints, read from the root.
# Exit 0 if true, 1 if false (check protocol, see internal/protocol/exec.go).
# The repository's own files: tracked files in a git checkout, every file
# elsewhere (the exported public tree has no .git). Never dependency sources
# such as a module cache inside the project directory, nor a witness fixture
# under .errata, where a broken link may be the planted break. No file at all
# means the check found nothing to test, which is unknown (exit 2), not a pass.
# A link is an inline link, [text](target), or a reference definition,
# [label]: target. Inline code is not a link: `[value 1](1)` shows the syntax
# and links nothing, so each code span is removed before the links are read. A
# fenced code block holds neither links nor headings, so it is passed over
# whole. checks/markdown.awk reads fences, headings and anchors, and says what
# it cannot write as GitHub does: an anchor of a heading outside ASCII or with
# emphasis is refused, with the reason, so that a link the mirror cannot
# follow never passes. A link to https://github.com/f-klement/errata/blob/main/
# and a path, as DOCKERHUB.md links the mirror, is checked as that path. A
# path that climbs above the root is refused, since no forge serves it. awk
# failing, as on a missing checks/markdown.awk, is unknown (exit 2), never a
# pass. ci/tests/relative-links.sh holds the cases.
here=$(dirname "$0")
tracked() {
    if [ -e .git ]; then
        git ls-files --cached --others --exclude-standard -- "$1" ':!:*/testdata/*' ':!:.errata/*'
    else
        find . -name '.?*' -prune -o -name testdata -prune -o -name "$1" -print
    fi | sort
}
files=$(tracked '*.md')
[ -n "$files" ] || { echo "no Markdown files to check"; exit 2; }
bad=$( { printf '%s\n' "$files" | sed 's/^/md /'; tracked '*.go' | sed 's/^/go /'; } |
  LC_ALL=C awk -v q="'" -f "$here/markdown.awk" -f "$here/relative-links.awk") ||
  { echo "the link check did not run: awk exited $?"; exit 2; }
[ -z "$bad" ] && exit 0
printf '%s\n' "$bad"
exit 1

#!/bin/sh
# Claim: every relative Markdown link points at a file that exists.
# Exit 0 if true, 1 if false (check protocol, see internal/protocol/exec.go).
# The repository's own Markdown: tracked files in a git checkout, every file
# elsewhere (the exported public tree has no .git). Never dependency sources
# such as a module cache inside the project directory. No file at all means
# the check found nothing to test, which is unknown (exit 2), not a pass.
markdown_files() {
    if [ -e .git ]; then
        git ls-files --cached --others --exclude-standard -- '*.md' ':!:*/testdata/*'
    else
        find . -name '.?*' -prune -o -name '*.md' -print
    fi | sort
}
[ -n "$(markdown_files)" ] || { echo "no Markdown files to check"; exit 2; }
bad=$(markdown_files | while read -r f; do
  dir=$(dirname "$f")
  grep -o '\]([^)#]*' "$f" | sed 's/^](//' | grep -v -e '^[a-z][a-z]*:' -e '^$' |
  while read -r target; do
    [ -e "$dir/$target" ] || echo "$f: $target"
  done
done)
[ -z "$bad" ] && exit 0
printf '%s\n' "$bad"
exit 1

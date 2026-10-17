#!/bin/sh
# Claim: no Markdown prose uses a dash as punctuation.
# Fenced code blocks are exempt, since YAML list items start with "- ".
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
hits=$(markdown_files | while read -r f; do
  awk -v f="$f" '
    /^[[:space:]]*```/ { code = !code; next }
    code { next }
    { line = $0; sub(/^[[:space:]]*[-*] /, "", line) }
    line ~ /—|–| - / { printf "%s:%d: %s\n", f, NR, $0 }
  ' "$f"
done)
[ -z "$hits" ] && exit 0
printf '%s\n' "$hits"
exit 1

#!/bin/sh
# Claim: every TODO in the API's code names the issue that tracks it, as TODO(#12).
# Exit 0 if true, 1 if false, 2 if there is no code to check (the check protocol).
[ -d "$ERRATA_ROOT/src" ] || { echo "no src directory"; exit 2; }
hits=$(grep -rn 'TODO' "$ERRATA_ROOT/src" | grep -v 'TODO(#[0-9][0-9]*)')
[ -z "$hits" ] && exit 0
printf '%s\n' "$hits"
exit 1

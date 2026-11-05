#!/bin/sh
# Claim: no migration drops a table, so a release can be rolled back without a restore.
# Exit 0 if true, 1 if false, 2 if there is no migration to check (the check protocol).
set -- "$ERRATA_ROOT"/migrations/*.sql
[ -e "$1" ] || { echo "no migration"; exit 2; }
hits=$(grep -l 'DROP TABLE' "$@")
[ -z "$hits" ] && exit 0
printf 'drops a table: %s\n' "$hits"
exit 1

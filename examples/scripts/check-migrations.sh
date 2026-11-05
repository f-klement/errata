#!/bin/sh
# Claim: the migrations are numbered 001, 002, 003 and so on, with no gap and no number twice.
# Exit 0 if true, 1 if false, 2 if there is nothing to check (the check protocol).
cd "$ERRATA_ROOT/migrations" 2>/dev/null || { echo "no migrations directory"; exit 2; }
want=1
for f in [0-9][0-9][0-9]_*.sql; do
  [ -e "$f" ] || { echo "no migration"; exit 2; }
  n=$(printf '%s' "$f" | cut -c1-3 | sed 's/^0*//')
  if [ "$n" -ne "$want" ]; then
    echo "$f follows migration $((want - 1)); want $(printf '%03d' "$want")"
    exit 1
  fi
  want=$((want + 1))
done

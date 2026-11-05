#!/bin/sh
# Prints the team, sorted, as AUTHORS lists it.
LC_ALL=C sort "$ERRATA_ROOT/team/members.txt"

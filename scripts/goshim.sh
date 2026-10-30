#!/bin/sh
# goshim.sh: the go that scripts/mutation.sh puts first on gremlins' PATH.
#
# Usage: goshim.sh <go> <casecover> <module root> <go arguments...>
#
# A `go test` with -coverprofile is gremlins' coverage pass. The shim runs it with the real go,
# then scripts/casecover on the profile, which adds a covered block for the expressions of each
# case clause and the statement of each select arm whose body ran (scripts/mutation.sh says
# why). Every other call goes to the real
# go unchanged, a mutant's go test among them.
#
# Both steps fail the pass. When the tests fail, the shim exits with go's status and casecover
# does not run, so gremlins reports that it could not gather coverage instead of mutating on
# the profile the failed tests wrote. When casecover fails, the shim exits with its status, so
# gremlins never reads a profile half rewritten. ci/tests/mutation.sh tests both.
real_go=$1 casecover=$2 root=$3
shift 3
if [ "${1:-}" = test ]; then
    prof="" prev=""
    for a do
        case "$a" in -coverprofile=*) prof=${a#-coverprofile=} ;; esac
        [ "$prev" != -coverprofile ] || prof=$a
        prev=$a
    done
    if [ -n "$prof" ]; then
        "$real_go" "$@" || exit
        exec "$casecover" "$root" "$prof"
    fi
fi
exec "$real_go" "$@"

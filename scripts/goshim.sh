#!/bin/sh
# goshim.sh: the go that scripts/mutation.sh puts first on gremlins' PATH.
#
# Usage: goshim.sh <go> <casecover> <testreach> <module root> <go arguments...>
#
# A `go test` with -coverprofile is gremlins' coverage pass. The shim runs it with the real go,
# then scripts/casecover on the profile, which adds a covered block for the expressions of each
# case clause and the statement of each select arm whose body ran (scripts/mutation.sh says
# why).
#
# Any other `go test`, while ERRATA_MUTATION_REACH names a package's index of the tests that
# reach each line, goes to scripts/testreach, which runs a mutant's go test narrowed to the tests
# that reach its line and the whole package's when those pass, and hands any other command to go
# unchanged. scripts/mutation.sh sets the variable on gremlins' call alone, and a test that runs
# go with the environment cleared reaches go directly. Every other call goes to the real go
# unchanged.
#
# Both steps of the coverage pass fail it. When the tests fail, the shim exits with go's status
# and casecover does not run, so gremlins reports that it could not gather coverage instead of
# mutating on the profile the failed tests wrote. When casecover fails, the shim exits with its
# status, so gremlins never reads a profile half rewritten. ci/tests/mutation.sh tests both.
real_go=$1 casecover=$2 testreach=$3 root=$4
shift 4
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
    if [ -n "${ERRATA_MUTATION_REACH:-}" ]; then
        exec "$testreach" run "$real_go" "$root" "$ERRATA_MUTATION_REACH" "$@"
    fi
fi
exec "$real_go" "$@"

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
#
# A mutant's go test names its package as gremlins v0.6.0 finds it: up from the file, the first
# directory whose path ends in the package clause's name, else the module root
# (internal/engine/engine.go, pkgName). For a package whose name is not its directory's, package
# main above all (scripts/crap), that is an ancestor of its import path, which has no Go file, so
# every mutant's build failed and gremlins counted the failure as a kill. scripts/mutation.sh
# names the package a pass mutates in ERRATA_MUTATION_PKG, and a go test in the form gremlins
# gives a mutant, `test [-tags <tags>] -timeout <limit> -failfast <package>`, whose package is an
# ancestor of that one, gets that package instead, before testreach or go sees it. Another
# package, or any other form (a test's own go test), passes unchanged.
#
# A mutant that does not compile is not viable, not killed. Gremlins reads exit 1 as killed and
# exit 2 as not viable (internal/engine/executor.go, getTestFailedStatus), and go test exits 1 for
# a build that fails as for a test that fails. So a mutant's go test that goes to go runs with its
# output kept, and when go exits 1 and printed `FAIL <package> [build failed]` or `[setup
# failed]` for the package, the shim exits 2. A test that fails keeps go's 1. testreach exits 2
# for a failed build of its own. Gremlins discards the output and, at its limit, kills the shell;
# the go test left running then ends at its own -timeout, as a mutant's test binary already did.
# A shim that cannot keep the output exits 3, which gremlins reads as lived, so the fault shows
# as a survivor and never as a kill.
real_go=$1 casecover=$2 testreach=$3 root=$4
shift 4
mutant=""
if [ "${1:-}" = test ]; then
    case "$#:$*" in
        "5:test -timeout $3 -failfast $5")
            mutant=yes
            case "${ERRATA_MUTATION_PKG:-}" in "$5"/*) set -- "$1" "$2" "$3" "$4" "$ERRATA_MUTATION_PKG" ;; esac ;;
        "7:test -tags $3 -timeout $5 -failfast $7")
            mutant=yes
            case "${ERRATA_MUTATION_PKG:-}" in "$7"/*) set -- "$1" "$2" "$3" "$4" "$5" "$6" "$ERRATA_MUTATION_PKG" ;; esac ;;
    esac
fi
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
if [ -n "$mutant" ]; then
    for pkg do :; done
    out=$(mktemp) || exit 3
    rc=0
    "$real_go" "$@" >"$out" 2>&1 || rc=$?
    cat "$out"
    if [ "$rc" -eq 1 ] && awk -v p="$pkg" '$1 == "FAIL" && $2 == p && ($3 " " $4 == "[build failed]" || $3 " " $4 == "[setup failed]") { found = 1 } END { exit !found }' "$out"; then
        rc=2
    fi
    rm -f "$out"
    exit "$rc"
fi
exec "$real_go" "$@"

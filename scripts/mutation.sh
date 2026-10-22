#!/bin/sh
# mutation.sh: run gremlins over each package of a scope, each held to its own floors.
#
# Usage: scripts/mutation.sh all | none | <package directory>...
#   make mutation                                   every package, as locally and nightly
#   make mutation MUTATION_SCOPE="internal/config"  the packages listed
#
# Each package is one pass, `gremlins unleash internal/<pkg>`, judged alone against its floors
# below. The path scopes gremlins' coverage pass, its mutants and so its timeout per mutant to
# that package. `all` is every package `go list` finds under internal/, so the nightly pass
# judges each package exactly as a push that touches it does. `none` mutates nothing and
# passes. CI's test job passes the scope ci/mutation-scope.sh computes; that script says which
# packages a pipeline puts in it. A package .gremlins.yaml excludes whole, such as
# internal/heapcap, is listed and skipped.
#
# Gremlins runs in a copy of the tracked tree under TMPDIR, not in the project directory. It
# copies the module root once per worker, and in CI that root holds Go's module and build
# caches, 1 to 1.5 GB, so a run wrote 2 to 3 GB of copies (measured 2026-10-18). The copy has
# what git tracks plus untracked files it does not ignore, so work not yet committed is
# mutated too. Outside a git checkout there is nothing to list, and gremlins runs in place.
#
# Two go test flags reach gremlins' runs through GOFLAGS, since gremlins passes no flags of
# its own choosing. -vet=off: lint already runs govet, and vet was about 5% of a mutant's
# cost. -count=1: the timeout per mutant is a multiple of the coverage pass's duration, and a
# coverage pass answered from Go's test cache takes a fraction of a second, which would turn
# every slow mutant into a timeout.
set -eu
GREMLINS=${GREMLINS:-gremlins}

refuse() {
    echo "mutation: REFUSED: $1" >&2
    exit 2
}

[ "$#" -gt 0 ] || refuse "no scope given. Usage: scripts/mutation.sh all | none | <package directory>..."
cd "$(dirname "$0")/.."
case "$*" in
    all|none) ;;
    *)
        for p in "$@"; do
            case "$p" in
                all|none) refuse "'$p' stands alone; the scope was: $*" ;;
                internal/*/*|internal/) refuse "'$p' is not a package directory such as internal/config" ;;
                internal/*) [ -d "$p" ] || refuse "$p is not a package directory in this tree" ;;
                *) refuse "'$p' is not a package directory such as internal/config" ;;
            esac
        done ;;
esac

scope=$*
if [ "$1" = none ]; then
    echo "==> mutation scope: none, so nothing to mutate; passed"
    exit 0
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/errata-mutation.XXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    mkdir "$work/tree"
    # A tracked file deleted from the working tree is still listed; tar would stop on it.
    git ls-files -z --cached --others --exclude-standard \
        | xargs -0 sh -c 'for f; do if [ -e "$f" ]; then printf "%s\0" "$f"; fi; done' _ \
        | tar --null -T - -cf - | tar -xf - -C "$work/tree"
    [ -f "$work/tree/go.mod" ] || refuse "the copy of the tracked tree has no go.mod"
    cd "$work/tree"
    echo "==> mutation: running from a copy of the tracked tree, $(du -sk . | cut -f1) KiB"
else
    echo "==> mutation: not a git checkout, so gremlins runs in the project directory"
fi
# Gremlins' own working copies go under the same directory, and leave with it.
TMPDIR=$work
export TMPDIR
GOFLAGS="${GOFLAGS:+$GOFLAGS }-vet=off -count=1"
export GOFLAGS

# Floors per package, judged here from the counts gremlins prints, not by gremlins. Each pass
# reads a copy of .gremlins.yaml with both thresholds at 0 and the package's timeout
# coefficient written in, given as --config, so .gremlins.yaml's own thresholds apply only to
# gremlins run by hand. Not --threshold-efficacy and --threshold-mcover: gremlins v0.6.0
# defines them as float flags, viper hands a float flag back as a string, and gremlins then
# reads the threshold as 0, which turns it off (measured 2026-10-18). Gremlins' own figures
# count a timed out mutant in neither, so one false timeout took check from 72.73% to 71.88%,
# under its floor; here a timed out mutant counts as covered and killed:
#   mutant coverage  (killed + lived + timed out) / (those + not covered)
#   efficacy         (killed + timed out) / (killed + lived + timed out)
# The timeout cap below keeps a limit that is too short from passing on timeouts. A figure at
# or below its floor fails. A floor may only rise: ci/test-ci.sh refuses one lowered.
#
# Efficacy is 85 for every package.
EFFICACY_FLOOR=85
# Mutant coverage, mutants some test reaches, is 85 but where Go's coverage profile cannot
# see a test reach them. It leaves case expressions, package-level declarations and code
# after a statement's first func literal outside every block, so their mutants always read
# as not covered, however the tests run them. Counted at e43c2ef:
#   check     72  9 of its 33 mutants, case expressions in Validate and LeftOutCount
#   protocol  78  3 of its 14 mutants
#   extract   83  520 of its 2671 at 715dc01 (436 case expressions, 77 package level, 7
#                 after a func literal), a ceiling of 83.74%; it read 83.49% once every
#                 reachable INI mutant was covered
mcover_floor() {
    case "$1" in
        internal/check)    echo 72 ;;
        internal/protocol) echo 78 ;;
        internal/extract)  echo 83 ;;
        *)                 echo 85 ;;
    esac
}
# A package fails when more than this percent of its mutants that ran timed out. A timed
# out mutant counts as killed, so without a cap a run whose limit is too short would pass on
# mutants no test failed.
TIMEOUT_CAP=5

# coefficient_for <package>: a timeout coefficient above .gremlins.yaml's, for a package whose
# tests wait on a fixed time that its coverage pass does not show. The timeout is the
# coefficient times that pass, so a scoped run of a fast package gets a short one.
#
# internal/protocol: TestRunTimeout runs `sleep 5` under a 50 ms check timeout. The mutants
# that break the timeout (exec.go:38, proc_unix.go:16) are killed only when the sleep ends,
# 5 to 6 s after they start, against a coverage pass of 0.7 s. Measured on the build node
# on 2026-10-18, pinned to two cores: all three timed out at coefficients 2, 3 and 5, and
# were killed at 10, with 1.5 s to spare. 20 gives about 14 s. It costs nothing, because a
# killed mutant ends when its test fails, not at the timeout.
#
# internal/extract: its coverage pass takes 2.4 s, but a mutant that lives reruns all its
# tests, 3.2 to 4.9 s alone on two cores, and several running side by side take longer
# still. At 5, its 12 s limit read mutants as timed out that live: properties.go:375 and
# ini_ansible_values.go:142 pass every test in 4.6 and 4.4 s by hand (2026-10-18, the build
# node at a load of 10), so a timeout there hid two survivors. At 10, about 24 s, the two
# files' 581 mutants that ran gave both as lived, and 6 timeouts against 33 at 5.
coefficient_for() {
    case "$1" in
        internal/protocol) echo 20 ;;
        internal/extract)  echo 10 ;;
    esac
}

# count <label>: the number gremlins printed after "<label>: ", or 0.
count() {
    n=$(sed -n "s/.*$1: \([0-9]*\).*/\1/p" "$work/out" | head -n 1)
    echo "${n:-0}"
}

# config_for <package>: .gremlins.yaml with both thresholds at 0 and that package's
# coefficient, as a file.
config_for() {
    coef=$(coefficient_for "$1")
    sed -e "s/^    efficacy: [0-9]*/    efficacy: 0/" \
        -e "s/^    mutant-coverage: [0-9]*/    mutant-coverage: 0/" \
        ${coef:+-e "s/^  timeout-coefficient: [0-9]*/  timeout-coefficient: $coef/"} \
        .gremlins.yaml >"$work/gremlins.yaml"
    for key in "    efficacy: 0" "    mutant-coverage: 0" ${coef:+"  timeout-coefficient: $coef"}; do
        grep -q "^$key\( \|\$\)" "$work/gremlins.yaml" || refuse "could not write '$key' into a copy of .gremlins.yaml"
    done
}

# percent <part> <whole>: part over whole as a percentage with two decimals.
percent() {
    awk -v p="$1" -v w="$2" 'BEGIN { printf "%.2f%%", 100 * p / w }'
}

# unleash <package>: one gremlins pass, its output shown and kept for the summary.
unleash() {
    p=$1
    mcover=$(mcover_floor "$p")
    coef=$(coefficient_for "$p")
    name="$p (floors: efficacy $EFFICACY_FLOOR, mutant coverage $mcover${coef:+; timeout coefficient $coef})"
    echo "==> mutation: $name"
    config_for "$p"
    rm -f "$work/rc"
    {
        rc=0
        "$GREMLINS" unleash --config "$work/gremlins.yaml" -S lt "$p" || rc=$?
        echo "$rc" >"$work/rc"
    } 2>&1 | tee "$work/out"
    rc=$(cat "$work/rc")
    killed=$(count Killed) lived=$(count Lived) timedout=$(count "Timed out") uncovered=$(count "Not covered")
    ran=$((killed + lived + timedout))
    all=$((ran + uncovered))
    if [ "$all" -gt 0 ]; then
        figures="Mutant coverage: $(percent "$ran" "$all")."
    else
        figures="No mutants."
    fi
    if [ "$ran" -gt 0 ]; then
        figures="$figures Efficacy: $(percent $((killed + timedout)) "$ran")."
    fi
    verdict="passed"
    if [ "$rc" -ne 0 ]; then
        verdict="FAILED, gremlins exit $rc"
    elif ! grep -q 'Killed: [0-9]' "$work/out" && grep -q '^No results to report\.$' "$work/out"; then
        # What gremlins prints, and all it prints, for a package with no mutants.
        figures="No mutants."
    elif ! grep -q 'Killed: [0-9]' "$work/out"; then
        verdict="FAILED, gremlins printed no counts"
        rc=1
    elif [ "$all" -gt 0 ] && [ $((ran * 100)) -le $((all * mcover)) ]; then
        verdict="FAILED, mutant coverage at or below $mcover"
        rc=1
    elif [ "$ran" -gt 0 ] && [ $(((killed + timedout) * 100)) -le $((ran * EFFICACY_FLOOR)) ]; then
        verdict="FAILED, efficacy at or below $EFFICACY_FLOOR"
        rc=1
    elif [ $((timedout * 100)) -gt $((ran * TIMEOUT_CAP)) ]; then
        verdict="FAILED, $timedout of $ran mutants timed out, over $TIMEOUT_CAP%"
        rc=1
    fi
    echo "$p: $verdict. $figures Timed out: $timedout of $ran." >>"$work/summary"
    [ "$rc" -eq 0 ]
}

if [ "$1" = all ]; then
    mod=$(go list -m)
    go list ./internal/... >"$work/packages"
    set -- $(sed "s|^$mod/||" "$work/packages")
    [ "$#" -gt 0 ] || refuse "go list found no package under internal/"
    echo "==> mutation: every package: $*"
fi

failed=0
for p in "$@"; do
    if grep -qxF "    - ^$p/" .gremlins.yaml; then
        echo "$p: skipped, .gremlins.yaml excludes it whole." >>"$work/summary"
        continue
    fi
    unleash "$p" || failed=1
done

echo "==> mutation scope: $scope"
sed 's/^/==>   /' "$work/summary"
if [ "$failed" -ne 0 ]; then
    echo "==> mutation: FAILED"
    exit 1
fi
echo "==> mutation: passed"

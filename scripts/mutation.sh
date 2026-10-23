#!/bin/sh
# mutation.sh: run gremlins over each package of a scope, each held to its own floors.
#
# Usage: scripts/mutation.sh all | none | <package directory>...
#   make mutation                                   every package, as locally and nightly
#   make mutation MUTATION_SCOPE="internal/config"  the packages listed
#
# Each package is one pass, `gremlins unleash <package directory>`, judged alone against its
# floors below. A package directory is one under internal/ that holds Go files, nested or not:
# internal/config, internal/extract, internal/extract/ini. The path scopes gremlins' coverage
# pass, its mutants and so its timeout per mutant to that package. `all` is every package
# `go list` finds under internal/, so the nightly pass judges each package exactly as a push
# that touches it does. `none` mutates nothing and passes. CI's test job passes the scope
# ci/mutation-scope.sh computes; that script says which packages a pipeline puts in it. A
# package .gremlins.yaml excludes whole, or one under a directory it excludes whole, such as
# internal/heapcap, is listed and skipped.
#
# Gremlins mutates every Go file under the path it is given, and matches exclude-files against
# each file's path relative to that path (gremlins v0.6.0, internal/engine/engine.go walks
# os.DirFS of the path). So a pass over internal/extract would also mutate internal/extract/ini,
# and a pattern such as ^internal/extract/coretest/ matches nothing in it. Each pass's config
# therefore excludes every file below the package's own directory, and repeats each pattern of
# .gremlins.yaml anchored under the package with the package's path taken off (config_for).
# Gremlins' coverage pass still runs the tests under the whole path, ./<path>/..., so a package
# with packages below it has a longer pass, and so a longer timeout per mutant.
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

# has_go <directory>: it holds a Go file.
has_go() {
    for f in "$1"/*.go; do
        [ -e "$f" ] && return 0
    done
    return 1
}

[ "$#" -gt 0 ] || refuse "no scope given. Usage: scripts/mutation.sh all | none | <package directory>..."
cd "$(dirname "$0")/.."
case "$*" in
    all|none) ;;
    *)
        for p in "$@"; do
            case "$p" in
                all|none) refuse "'$p' stands alone; the scope was: $*" ;;
                */|*//*|./*|*/./*|*/.|../*|*/../*|*/..) refuse "'$p' is not a package directory such as internal/config" ;;
                internal/*)
                    [ -d "$p" ] || refuse "$p is not a package directory in this tree"
                    has_go "$p" || refuse "$p holds no Go file, so it is not a package directory" ;;
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
# So does a build cache of its own. Each mutant is a build no later run repeats, and Go keeps
# an entry 5 days after its last use, so in the caller's cache a full pass left gigabytes
# behind: on 2026-10-22 the build node's suite cache held 123.6 GB written since 2026-10-18,
# and CI's restored cache held 196,255 files, against 1,375 for a warm build and test of
# toml. A toml pass grew a cache by 1 MiB a mutant.
GOCACHE=$work/go-build
export GOCACHE
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
# The timeout cap below keeps a limit that is too short from passing on timeouts, judged on a
# second pass when the first is over it (unleash). A figure at
# or below its floor fails. A floor may only rise: ci/test-ci.sh refuses one lowered.
#
# Efficacy is 85 for every package.
EFFICACY_FLOOR=85
# Mutant coverage, mutants some test reaches, is 85 but where Go's coverage profile cannot
# see a test reach them. It leaves case expressions, package-level declarations and code
# after a statement's first func literal outside every block, so their mutants always read
# as not covered, however the tests run them. Such a package's floor is its measured figure
# rounded down, and one under it when the figure is whole, since a figure at its floor
# fails. check and protocol were counted at e43c2ef, the extract packages at the split into
# one package per reader (#105), on 2026-10-22, each scoped alone:
#   check               72  9 of its 33 mutants, case expressions in Validate and LeftOutCount
#   protocol            78  3 of its 14 mutants
#   extract             64  7 of its 20, after the first func literal of kinds' return
#                           statement: 65.00%
#   extract/core        79  116 of its 571 (115 case expressions, 1 package level): 79.68%
#   extract/ini         82  253 of its 1500 (178 case expressions, 75 package level), and 7
#                           in dead code (php.go 163 twice, 374, 548, 552, 554, 628): 82.67%
#   extract/properties  79  34 of its 167, case expressions: 79.64%
#   extract/xml         82  94 of its 527, case expressions: 82.16%
# text (5 such mutants of 238, 97.90%, after goldmark v2) and toml (10 of 78, 87.18%) clear
# 85, and json, regex and yaml have none. Before the split, extract as one package had 83 (520
# of its 2671).
mcover_floor() {
    case "$1" in
        internal/check)              echo 72 ;;
        internal/protocol)           echo 78 ;;
        internal/extract)            echo 64 ;;
        internal/extract/core)       echo 79 ;;
        internal/extract/ini)        echo 82 ;;
        internal/extract/properties) echo 79 ;;
        internal/extract/xml)        echo 82 ;;
        *)                           echo 85 ;;
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
# internal/extract/ini: its coverage pass takes 1.0 to 1.4 s on the build node, and its tests
# with one change rebuilt 0.9 to 1.0 s alone, but two mutants running side by side on a node
# under other load take several times that. At 5, a limit of 6.7 s, 34 of its 1240 mutants
# timed out on 2026-10-22, the build node running CI's whole module pass beside it. Applied by
# hand one at a time on the fallback node, 14 ran past 60 s, 12 lived in 1.7 to 2.7 s
# (ansible_values.go:812, :1026, :1028, :1039, git.go:40, ini.go:317, notliteral.go:78, :138,
# :249, :268, php.go:399, :400) and 6 were killed, two of them only after 11 s
# (ansible_values.go:1046); two could not be applied by hand. So a timeout at 5 hid 12
# survivors. At 10, about 14 s, the next run's 15 timeouts were the 14 that ran past 60 s and
# the other mutant of one of them (ini.go:306), and those 12 read as lived.
#
# internal/extract/core and internal/extract/xml: on a quiet build node at 5, a mutant that
# lives by hand in 1.4 s (core path.go:640) and one that lives in 0.8 s (xml xpath.go:231)
# each timed out, against limits of 8.9 s and 4.7 s; the same xml mutant read as lived in
# another run. Their property tests draw random inputs, so a mutant's run time varies from
# run to run. Each package's other timeouts at 5 ran past 60 s by hand or were killed.
#
# Before the split into one package per reader, internal/extract had 10: its pass took 2.4 s
# and a mutant that lived 3.2 to 4.9 s. The facade's pass now runs every reader's tests, 3 to
# 5 s against 0.3 s for its own, so 5 is ample there.
#
# Every other reader under internal/extract has 10 as well. Their passes are short, so 5 gives
# a limit of a second or two, and a mutant a test kills can run past it on a node under CI's
# load. A small package cannot absorb that under TIMEOUT_CAP: json had 2 of its 13 mutants
# time out in main's pipeline on 2026-10-22 (json.go:38 and :57), 15%, where both earlier
# full runs that day timed out none. 10 costs a killed mutant nothing.
coefficient_for() {
    case "$1" in
        internal/protocol) echo 20 ;;
        internal/extract/*) echo 10 ;;
    esac
}

# count <label>: the number gremlins printed after "<label>: ", or 0.
count() {
    n=$(sed -n "s/.*$1: \([0-9]*\).*/\1/p" "$work/out" | head -n 1)
    echo "${n:-0}"
}

# config_for <package>: .gremlins.yaml with both thresholds at 0 and that package's
# coefficient, as a file. Its exclude-files gain, first, the pattern `/`: gremlins matches a
# path relative to the package, so a path with a slash is a file below the package's own
# directory, in a package nested under it or in testdata. Then each pattern .gremlins.yaml
# anchors under the package, ^<package>/<rest>, as ^<rest>, so it excludes in this pass what
# it excludes in a run from the module root.
config_for() {
    coef=$(coefficient_for "$1")
    sed -e "s/^    efficacy: [0-9]*/    efficacy: 0/" \
        -e "s/^    mutant-coverage: [0-9]*/    mutant-coverage: 0/" \
        ${coef:+-e "s/^  timeout-coefficient: [0-9]*/  timeout-coefficient: $coef/"} \
        .gremlins.yaml >"$work/gremlins.base"
    {
        echo "    - /"
        awk -v pre="    - ^$1/" 'index($0, pre) == 1 && length($0) > length(pre) { print "    - ^" substr($0, length(pre) + 1) }' .gremlins.yaml
    } >"$work/excludes"
    awk -v extra="$work/excludes" '
        { print }
        /^  exclude-files:$/ { while ((getline line < extra) > 0) print line; n++ }
        END { if (n != 1) exit 1 }
    ' "$work/gremlins.base" >"$work/gremlins.yaml" \
        || refuse "a copy of .gremlins.yaml has no single '  exclude-files:' line to add the package's exclusions to"
    for key in "    efficacy: 0" "    mutant-coverage: 0" ${coef:+"  timeout-coefficient: $coef"}; do
        grep -q "^$key\( \|\$\)" "$work/gremlins.yaml" || refuse "could not write '$key' into a copy of .gremlins.yaml"
    done
}

# excluded_whole <package>: .gremlins.yaml excludes the package's directory, or one above
# it, whole.
excluded_whole() {
    d=$1
    while :; do
        grep -qxF "    - ^$d/" .gremlins.yaml && return 0
        case "$d" in
            */*) d=${d%/*} ;;
            *) return 1 ;;
        esac
    done
}

# percent <part> <whole>: part over whole as a percentage with two decimals.
percent() {
    awk -v p="$1" -v w="$2" 'BEGIN { printf "%.2f%%", 100 * p / w }'
}

# pass_once <package>: one gremlins pass, its output shown and kept, and its counts.
pass_once() {
    rm -f "$work/rc"
    {
        rc=0
        "$GREMLINS" unleash --config "$work/gremlins.yaml" -S lt "$1" || rc=$?
        echo "$rc" >"$work/rc"
    } 2>&1 | tee "$work/out"
    rc=$(cat "$work/rc")
    killed=$(count Killed) lived=$(count Lived) timedout=$(count "Timed out") uncovered=$(count "Not covered")
    ran=$((killed + lived + timedout))
}

# unleash <package>: a gremlins pass judged against the package's floors. A package over the
# timeout cap runs once more, and the second pass is judged: a mutant times out when the node
# is busy as well as when it hangs, and one that hangs times out on both passes. On the build
# node, CI's test jobs and image builds running side by side timed out 2 of json's 13 mutants
# on 2026-10-22 and 14 and 20 of config's 110 on 2026-10-23; each package had none on a rerun.
unleash() {
    p=$1
    mcover=$(mcover_floor "$p")
    coef=$(coefficient_for "$p")
    name="$p (floors: efficacy $EFFICACY_FLOOR, mutant coverage $mcover${coef:+; timeout coefficient $coef})"
    echo "==> mutation: $name"
    config_for "$p"
    pass_once "$p"
    again=""
    if [ "$rc" -eq 0 ] && grep -q 'Killed: [0-9]' "$work/out" && [ $((timedout * 100)) -gt $((ran * TIMEOUT_CAP)) ]; then
        echo "==> mutation: $p: $timedout of $ran mutants timed out, over $TIMEOUT_CAP%; running the package once more"
        pass_once "$p"
        again=", on its second pass too"
    fi
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
        verdict="FAILED, $timedout of $ran mutants timed out, over $TIMEOUT_CAP%$again"
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
    if excluded_whole "$p"; then
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

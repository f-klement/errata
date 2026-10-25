#!/bin/sh
# mutation.sh: run gremlins over each package of a scope, each held to its own floors.
#
# Usage: scripts/mutation.sh [--diff <base>] all | none | <package directory>...
#   make mutation                                   every package, as locally and weekly
#   make mutation MUTATION_SCOPE="internal/config"  the packages listed
#   make mutation MUTATION_SCOPE="internal/config" MUTATION_DIFF=<commit>
#                                                   the lines those packages changed since it
#
# Each package is one pass, `gremlins unleash <package directory>`, judged alone against its
# floors below. A package directory is one under internal/ that holds Go files, nested or not:
# internal/config, internal/extract, internal/extract/ini. The path scopes gremlins' coverage
# pass, its mutants and so its timeout per mutant to that package. `all` is every package
# `go list` finds under internal/, so the weekly pass judges each package exactly as a run by
# hand does. `none` mutates nothing and passes. CI's mutation job passes the scope
# ci/mutation-scope.sh computes, with --diff (ci/push-mutation.sh); that script says which
# packages a pipeline puts in it. A
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
#
# --diff <base> mutates only the lines a package changed since <base>: gremlins' own --diff, which
# marks every mutant outside the changed lines Skipped. As gremlins v0.6.0 wires it, it skips
# every mutant here. It runs `git diff --merge-base <base>` in the directory it runs in
# (internal/diff/parse.go), and the copy under TMPDIR has no .git; and it looks a mutant's file up
# in that diff by its path relative to the package (internal/engine/engine.go walks os.DirFS of
# the package, and diff.go keys on the diff's new name), while git names files from the
# repository root. So this script makes each package's diff itself, in the checkout, with
# --relative, and a `git` on PATH for the gremlins call prints it (the shim). A package whose
# own Go code did not change, in scope through its tests, its testdata or a module bump, is
# skipped and left to the weekly pass: an empty diff would make gremlins mutate all of it
# (diff.go: an empty Diff counts every position as changed), and a whole pass can take most of
# an hour (extract/ini, 1500 mutants), past a push's 25 minutes (owner decision 2026-10-24).
# ci/test-ci.sh runs the real gremlins on a module of its own to show that a diff run mutates
# the changed lines and skips the rest.
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

diff_base=""
if [ "${1:-}" = --diff ]; then
    [ "$#" -ge 2 ] || refuse "--diff takes a base commit"
    diff_base=$2
    shift 2
    case "$diff_base" in
        ''|-*) refuse "--diff takes a base commit, not '$diff_base'" ;;
    esac
fi
[ "$#" -gt 0 ] || refuse "no scope given. Usage: scripts/mutation.sh [--diff <base>] all | none | <package directory>..."
cd "$(dirname "$0")/.."
case "$*" in
    all) [ -z "$diff_base" ] || refuse "--diff takes the packages that changed, not all, which the weekly pass mutates" ;;
    none) ;;
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

# diff_file <package>: where that package's diff goes.
diff_file() {
    echo "$work/diff/$(echo "$1" | tr / _).diff"
}
# diff_for <package>: when the package's own Go code changed since the base, write its diff, with
# paths relative to the package, to diff_file; nothing otherwise. Run in the checkout, before the
# copy, which has no .git. The diff compares the base with the files as the copy below will hold them: a scratch index
# (GIT_INDEX_FILE) gets every tracked file and every untracked file git does not ignore, so new
# work not yet committed is a change too, and the checkout's own index is left alone.
# --no-renames: a file moved into the package is new code there, so all of it is mutated.
diff_for() {
    : >"$work/diff.names"
    GIT_INDEX_FILE=$work/diff.index git diff --cached --merge-base --no-renames --name-only "$diff_base" -- "$1" >"$work/diff.names"
    if awk -v pre="$1/" 'index($0, pre) == 1 { f = substr($0, length(pre) + 1); if (f !~ /\// && f ~ /\.go$/ && f !~ /_test\.go$/) found = 1 } END { exit !found }' "$work/diff.names"; then
        GIT_INDEX_FILE=$work/diff.index git diff --cached --merge-base --no-renames --relative="$1/" "$diff_base" -- "$1" >"$(diff_file "$1")"
    fi
}
if [ -n "$diff_base" ]; then
    git rev-parse --is-inside-work-tree >/dev/null 2>&1 || refuse "--diff needs a git checkout to compare with $diff_base"
    git rev-parse -q --verify "$diff_base^{commit}" >/dev/null || refuse "--diff $diff_base names no commit in this checkout"
    real_git=$(command -v git)
    mkdir "$work/diff"
    GIT_INDEX_FILE=$work/diff.index git read-tree HEAD
    GIT_INDEX_FILE=$work/diff.index git add -A
    for p in "$@"; do
        diff_for "$p"
    done
    # The `git` gremlins finds first on PATH. It answers the one call gremlins makes, `git diff
    # --merge-base <base>`, with the package's diff from diff_for, and leaves a mark that it did,
    # so a gremlins that stopped asking cannot pass as a diff run; it hands anything else to git.
    mkdir "$work/shim"
    cat >"$work/shim/git" <<'SHIM'
#!/bin/sh
if [ "$#" -eq 3 ] && [ "$1" = diff ] && [ "$2" = --merge-base ]; then
    if [ "$3" != "$ERRATA_MUTATION_BASE" ]; then
        echo "mutation: git shim: asked for a diff from $3, but the base is $ERRATA_MUTATION_BASE" >&2
        exit 1
    fi
    : >"$ERRATA_MUTATION_ASKED"
    exec cat "$ERRATA_MUTATION_DIFF"
fi
exec "$ERRATA_REAL_GIT" "$@"
SHIM
    chmod +x "$work/shim/git"
fi
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
# On a diff run (--diff) the same floors judge only the mutants on the changed lines, so the
# figures are those of the new code alone, over a denominator that may be a handful: one lived
# mutant of six is 83.33% efficacy, under 85, and fails the run until a test kills it. That is
# the intent: new code meets the bar the package meets. The other side of a small denominator:
# a mutant in a case expression always reads as not covered (below), so a change made only of
# such lines can fall under its package's mutant coverage floor with every arm tested. So a diff
# run judges mutant coverage only from MCOVER_MIN_DIFF mutants on its changed lines, and
# efficacy always (owner decision 2026-10-24); a whole run judges both at any count. Efficacy
# needs a mutant some test reaches, so a diff run whose changed lines hold mutants and no test
# reaches any of them, new code with no test, fails, naming those lines: gremlins prints each
# mutant not covered on a diff run (-S ltc).
MCOVER_MIN_DIFF=10
#
# Efficacy is 85 for every package.
EFFICACY_FLOOR=85
# Mutant coverage, mutants some test reaches, is 85 but where Go's coverage profile cannot
# see a test reach them. It leaves case expressions, package-level declarations and code
# after a statement's first func literal outside every block, so their mutants always read
# as not covered, however the tests run them. Such a package's floor is its measured figure
# rounded down, and one under it when the figure is whole, since a figure at its floor
# fails. check and protocol were counted at e43c2ef, the other extract packages at the split
# into one package per reader (#105), on 2026-10-22, and ini and jenkins again on 2026-10-25,
# with env's reader in (#38, #107), each scoped alone:
#   check               72  9 of its 33 mutants, case expressions in Validate and LeftOutCount
#   protocol            78  3 of its 14 mutants
#   extract/ini         82  253 of its 1500 (178 case expressions, 75 package level), and 4
#                           in dead code (php.go 544, 548, 550, 624): 82.87%
#   extract/jenkins     79  86 of its 416 (84 case expressions, 2 package level): 79.33%
#   extract/properties  79  34 of its 167, case expressions: 79.64%
#   extract/xml         82  94 of its 527, case expressions: 82.16%
# core had 82 (111 of 637, 82.57%) until env's reader added code its tests reach: 111 of 744,
# 110 case expressions and 1 package level, 85.08% on 2026-10-25, so it has 85, one mutant
# over. githubactions (52 case expressions of 408, 87.25%) and gitlabci (38 of 380, 90.00%)
# clear 85 on 2026-10-25, and env, every one of its 46 mutants covered, has none;
# text (5 such mutants of 238, 97.90%, after goldmark v2), toml (10 of 78, 87.18%) and
# toolversion (9 of 117, 92.31%, on 2026-10-23) clear 85, and json, regex and yaml have none.
# The facade had 64, 7 of its 20 after the first func literal of kinds' return statement
# (65.00%), until tool_version (#46) worked out which kinds are set before that statement: 22
# of 22 since, so it has 85 too. Before the split, extract as one package had 83 (520
# of its 2671).
mcover_floor() {
    case "$1" in
        internal/check)              echo 72 ;;
        internal/protocol)           echo 78 ;;
        internal/extract/ini)        echo 82 ;;
        internal/extract/jenkins)    echo 79 ;;
        internal/extract/properties) echo 79 ;;
        internal/extract/xml)        echo 82 ;;
        *)                           echo 85 ;;
    esac
}
# A package fails when more than TIMEOUT_CAP percent of its mutants that ran timed out, and
# more than TIMEOUT_MIN of them. A timed out mutant counts as killed, so without a cap a run
# whose limit is too short would pass on mutants no test failed. The minimum count is for small
# runs, a diff run above all: 1 timeout of 12 is 8%, and a mutant that hangs (a loop condition
# turned so it never ends) times out at any limit, so the percentage alone would fail a run on
# one or two genuine hangs. Three timeouts in any run is over the cap when they are over 5%.
TIMEOUT_CAP=5
TIMEOUT_MIN=2

# over_cap: the pass's timeouts are over both TIMEOUT_CAP and TIMEOUT_MIN.
over_cap() {
    [ "$timedout" -gt "$TIMEOUT_MIN" ] && [ $((timedout * 100)) -gt $((ran * TIMEOUT_CAP)) ]
}

# The limit per mutant. Gremlins gives each mutant its timeout coefficient times the duration
# of its coverage pass, the first thing it runs, so a short pass gives a short limit: on
# 2026-10-24 a warm build cache made internal/semver's pass 0.24 s, coefficient 5 gave 1.2 s,
# and 297 of its 314 mutants timed out, though they take 1.2 to 2.7 s. So every package gets at
# least LIMIT_FLOOR seconds a mutant. Gremlins takes no limit in seconds, only a coefficient, so
# the script measures the pass first, the same `go test -tags <tags> -cover -coverprofile
# ./<package>/...` with the same GOFLAGS and GOCACHE, and gives gremlins the coefficient that
# makes LIMIT_AIM seconds of it (measure_pass, coefficient_for). The measured run comes after
# a run with the same flags that runs no test (-run '^$'), so it and gremlins' own pass after
# it both find every test binary built. The flags must match: -cover builds other binaries,
# and a warm-up without it left the measured run compiling them, twice gremlins' pass
# (internal/rule 1.64 s against 0.86 s, semver 0.54 against 0.23, on 2026-10-24), so the
# limit came out at 8.6 s. LIMIT_AIM is half again the floor because
# gremlins' pass is a second measurement of the same thing and comes out shorter as often as
# longer; on top of that, a pass that ends with a timeout under LIMIT_FLOOR, from gremlins'
# own figure, runs once more with the coefficient taken from that figure (unleash).
LIMIT_FLOOR=10
LIMIT_AIM=15

# base_coefficient_for <package>: the least coefficient for a package whose tests wait on more
# than the floor gives, measured; .gremlins.yaml's 5 otherwise. The floor made the others
# redundant on 2026-10-24: the extract readers' 10 (mutants killed in a second or two, under
# limits of 1 to 9 s on a busy node) and semver's 20 (mutants of 1.2 to 2.7 s against 1.2 s).
#
# internal/protocol: TestRunTimeout runs `sleep 5` under a 50 ms check timeout. The mutants
# that break the timeout (exec.go:38, proc_unix.go:16) are killed only when the sleep ends,
# 5 to 6 s after they start, against a coverage pass of 0.7 s. Measured on the build node
# on 2026-10-18, pinned to two cores: all three timed out at coefficients 2, 3 and 5, and
# were killed at 10, with 1.5 s to spare. 20 gives about 14 s, room the floor's 10 s does not
# leave on a node under load. It costs nothing, because a killed mutant ends when its test
# fails, not at the timeout.
#
# internal/extract/ini: applied by hand one at a time on the fallback node on 2026-10-22, two
# mutants were killed only after 11 s (ansible_values.go:1046), past the floor. Its pass takes
# 1.0 to 1.4 s, so 10 gives 10 to 14 s.
base_coefficient_for() {
    case "$1" in
        internal/protocol) echo 20 ;;
        internal/extract/ini) echo 10 ;;
        *) sed -n 's/^  timeout-coefficient: \([0-9]*\).*/\1/p' .gremlins.yaml ;;
    esac
}

# measure_pass <package>: pass_s, the seconds of the package's coverage pass as gremlins runs
# it, the shorter of two runs after one that builds it; empty when the tests fail, which
# gremlins then reports itself. The shorter, because gremlins' pass came out shorter than one
# measured run: internal/semver's 0.23 s against 0.88 s on 2026-10-24, which put its limit at
# 4.2 s and cost a second pass of 3.3 minutes.
measure_pass() {
    tags=$(sed -n 's/^  tags: *\([^ #]*\).*/\1/p' .gremlins.yaml)
    go test ${tags:+-tags "$tags"} -cover -coverprofile "$work/measure.cov" -run '^$' "./$1/..." >/dev/null 2>&1 || true
    pass_s=""
    for _ in 1 2; do
        start=$(date +%s%N)
        go test ${tags:+-tags "$tags"} -cover -coverprofile "$work/measure.cov" "./$1/..." >/dev/null 2>&1 || { pass_s=""; return 0; }
        pass_s=$(awk -v a="$start" -v b="$(date +%s%N)" -v p="$pass_s" 'BEGIN { s = (b - a) / 1e9; if (p != "" && p + 0 < s) s = p; printf "%.3f", s }')
    done
}

# coefficient_for <package> <pass seconds>: the larger of the package's base coefficient and
# the one that makes LIMIT_AIM seconds of a pass that long. A pass under 10 ms counts as 10 ms.
coefficient_for() {
    base=$(base_coefficient_for "$1")
    if [ -z "$2" ]; then
        echo "$base"
        return
    fi
    awk -v b="$base" -v aim="$LIMIT_AIM" -v s="$2" 'BEGIN {
        if (s < 0.01) s = 0.01
        c = int(aim / s); if (c < aim / s) c++
        print (c > b ? c : b)
    }'
}

# gremlins_pass_s: the seconds of the coverage pass gremlins timed, from its "done in" line, a
# Go duration such as 812.3ms, 4.3s or 1m2.5s; empty when it printed none.
gremlins_pass_s() {
    sed -n 's/.*Gathering coverage\.\.\. *done in \([0-9.hmsµun]*\).*/\1/p; s/^done in \([0-9.hmsµun]*\)$/\1/p' "$work/out" | head -n 1 | awk '
        NF {
            d = $0; t = 0
            while (match(d, /^[0-9.]+(h|ms|µs|us|ns|m|s)/)) {
                part = substr(d, 1, RLENGTH); d = substr(d, RLENGTH + 1)
                match(part, /[0-9.]+/); v = substr(part, 1, RLENGTH) + 0; u = substr(part, RLENGTH + 1)
                if (u == "h") t += v * 3600; else if (u == "m") t += v * 60; else if (u == "s") t += v
                else if (u == "ms") t += v / 1000; else t += v / 1000000
            }
            printf "%.3f", t
        }'
}

# seconds <coefficient> <pass seconds>: the limit they give, with one decimal.
seconds() {
    awk -v c="$1" -v s="$2" 'BEGIN { printf "%.1f", c * s }'
}

# count <label>: the number gremlins printed after "<label>: ", or 0.
count() {
    n=$(sed -n "s/.*$1: \([0-9]*\).*/\1/p" "$work/out" | head -n 1)
    echo "${n:-0}"
}

# config_for <package> <coefficient>: .gremlins.yaml with both thresholds at 0 and that
# coefficient, as a file. Its exclude-files gain, first, the pattern `/`: gremlins matches a
# path relative to the package, so a path with a slash is a file below the package's own
# directory, in a package nested under it or in testdata. Then each pattern .gremlins.yaml
# anchors under the package, ^<package>/<rest>, as ^<rest>, so it excludes in this pass what
# it excludes in a run from the module root.
config_for() {
    coef=$2
    sed -e "s/^    efficacy: [0-9]*/    efficacy: 0/" \
        -e "s/^    mutant-coverage: [0-9]*/    mutant-coverage: 0/" \
        -e "s/^  timeout-coefficient: [0-9]*/  timeout-coefficient: $coef/" \
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
    for key in "    efficacy: 0" "    mutant-coverage: 0" "  timeout-coefficient: $coef"; do
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

# pass_once <package> [<diff file>]: one gremlins pass, its output shown and kept, and its
# counts. With a diff file, gremlins runs with --diff and the shim first on PATH.
pass_once() {
    rm -f "$work/rc" "$work/asked"
    {
        rc=0
        if [ -n "${2:-}" ]; then
            PATH="$work/shim:$PATH" ERRATA_MUTATION_BASE=$diff_base ERRATA_MUTATION_DIFF=$2 \
                ERRATA_MUTATION_ASKED="$work/asked" ERRATA_REAL_GIT=$real_git \
                "$GREMLINS" unleash --config "$work/gremlins.yaml" --diff "$diff_base" -S ltc "$1" || rc=$?
        else
            "$GREMLINS" unleash --config "$work/gremlins.yaml" -S lt "$1" || rc=$?
        fi
        echo "$rc" >"$work/rc"
    } 2>&1 | tee "$work/out"
    rc=$(cat "$work/rc")
    killed=$(count Killed) lived=$(count Lived) timedout=$(count "Timed out") uncovered=$(count "Not covered")
    skipped=$(count Skipped)
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
    measure_pass "$p"
    coef=$(coefficient_for "$p" "$pass_s")
    if [ -n "$pass_s" ]; then
        limit="limit per mutant $(seconds "$coef" "$pass_s") s: coefficient $coef times a pass of $pass_s s"
    else
        limit="limit per mutant unknown: the tests failed when measured; coefficient $coef"
    fi
    name="$p (floors: efficacy $EFFICACY_FLOOR, mutant coverage $mcover; $limit)"
    diff="" how=""
    if [ -n "$diff_base" ]; then
        diff=$(diff_file "$p")
        name="$name, the lines changed since $diff_base"
    fi
    echo "==> mutation: $name"
    config_for "$p" "$coef"
    pass_once "$p" "$diff"
    again=""
    # The limit gremlins gave, from its own pass. Under the floor with a mutant timed out, the
    # timeout may be the short limit's: the package runs once more, its coefficient taken from
    # gremlins' figure, and the second pass is judged.
    short=""
    gpass=$(gremlins_pass_s)
    if [ -n "$gpass" ] && [ "$timedout" -gt 0 ] \
        && awk -v l="$(seconds "$coef" "$gpass")" -v f="$LIMIT_FLOOR" 'BEGIN { exit !(l < f) }'; then
        short=yes
    fi
    if [ "$rc" -eq 0 ] && grep -q 'Killed: [0-9]' "$work/out" && { over_cap || [ -n "$short" ]; }; then
        if [ -n "$short" ]; then
            was=$(seconds "$coef" "$gpass")
            coef=$(coefficient_for "$p" "$gpass")
            config_for "$p" "$coef"
            echo "==> mutation: $p: $timedout of $ran mutants timed out under a limit of $was s, below the floor of $LIMIT_FLOOR s; running the package once more at coefficient $coef"
        else
            echo "==> mutation: $p: $timedout of $ran mutants timed out, over $TIMEOUT_CAP% and over $TIMEOUT_MIN; running the package once more"
        fi
        pass_once "$p" "$diff"
        gpass=$(gremlins_pass_s)
        again=", on its second pass too"
    fi
    if [ -n "$gpass" ]; then
        echo "==> mutation: $p: limit per mutant $(seconds "$coef" "$gpass") s: coefficient $coef times gremlins' pass of $gpass s"
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
    elif [ -n "$diff" ] && [ "$ran" -eq 0 ] && [ "$uncovered" -gt 0 ]; then
        lines=$(sed -n 's/.*NOT COVERED .* at \([^ ]*\):[0-9]*$/\1/p' "$work/out" | sort -u | head -n 10 | tr '\n' ' ' | sed 's/ $//; s/ /, /g')
        verdict="FAILED, no test reaches any of its $uncovered mutants on the changed lines (${lines:-lines not printed})"
        rc=1
    elif [ "$all" -gt 0 ] && { [ -z "$diff" ] || [ "$all" -ge "$MCOVER_MIN_DIFF" ]; } && [ $((ran * 100)) -le $((all * mcover)) ]; then
        verdict="FAILED, mutant coverage at or below $mcover"
        rc=1
    elif [ "$ran" -gt 0 ] && [ $(((killed + timedout) * 100)) -le $((ran * EFFICACY_FLOOR)) ]; then
        verdict="FAILED, efficacy at or below $EFFICACY_FLOOR"
        rc=1
    elif over_cap; then
        verdict="FAILED, $timedout of $ran mutants timed out, over $TIMEOUT_CAP% and over $TIMEOUT_MIN$again"
        rc=1
    elif [ -n "$diff" ] && [ ! -e "$work/asked" ]; then
        # The shim was not asked for the diff, so gremlins judged every mutant, not the changed
        # lines: it no longer calls git as the shim expects.
        verdict="FAILED, gremlins never asked the git shim for the diff"
        rc=1
    fi
    if [ -n "$diff" ]; then
        how=" Outside the changed lines: $skipped skipped."
        if [ "$all" -gt 0 ] && [ "$all" -lt "$MCOVER_MIN_DIFF" ]; then
            how="$how Mutant coverage not judged: the changed lines hold $all of the $MCOVER_MIN_DIFF mutants it needs."
        fi
    fi
    echo "$p: $verdict. $figures Timed out: $timedout of $ran.$how" >>"$work/summary"
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
    if [ -n "$diff_base" ] && [ ! -e "$(diff_file "$p")" ]; then
        echo "==> mutation: $p: its Go code is as at $diff_base, so there are no changed lines to mutate; the weekly pass judges it whole"
        echo "$p: skipped, its Go code is as at $diff_base; left to the weekly pass." >>"$work/summary"
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

#!/bin/sh
# mutation.sh: run gremlins over each package of a scope, each held to its own floors.
#
# Usage: scripts/mutation.sh [--diff <base>] [--deadline <epoch seconds>] [--jobs <n>] [--most <n>]
#                            all | none | <package directory>...
#   make mutation                                   every package, as locally and weekly
#   make mutation MUTATION_SCOPE="internal/config"  the packages listed
#   make mutation MUTATION_SCOPE="internal/config" MUTATION_DIFF=<commit>
#                                                   the lines those packages changed since it
#   make mutation ... MUTATION_DEADLINE=<epoch seconds>
#                                                   ending by then, leaving out and naming a
#                                                   package that would not (left_out below)
#   make mutation ... MUTATION_JOBS=<n>             n packages at once (run_slot below); CI's
#                                                   mutation job sets it from its pod (#151)
#   make mutation ... MUTATION_DIFF=<commit> MUTATION_MOST=<n>
#                                                   at most n packages, those with the most
#                                                   changed lines; the rest are left out (#151)
#
# Each package is one pass, `gremlins unleash <package directory>`, judged alone against its
# floors (scripts/mutation-floors.txt, read below). A package directory is one under internal/
# that holds Go files, nested or not: internal/config, internal/extract, internal/extract/ini.
# The path scopes gremlins' coverage pass, its mutants and so its timeout per mutant to that
# package. `all` is every package
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
#
# --jobs <n> runs up to n packages at once (#151). A push that lands several tickets scopes a
# dozen packages, and each pays for its build, its timing and gremlins' coverage pass even when
# its changed lines hold a handful of mutants: pipeline 2986574549 ran 13 packages one after
# the other in 1417 s, over the 25 minutes. ci/push-mutation.sh picks n from the pod's CPUs
# and memory. The mutants that run at once stay at .gremlins.yaml's workers, shared out
# between the packages, so the timing each package measures holds as it does today. Each
# package's verdict, the LEFT OUT for time line and the exit status are the same either way,
# and its log is printed whole, in the order of the scope, once every package has ended. The
# weekly pass runs one package at a time, so each package's memory line is its own.
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

diff_base="" deadline="" jobs=1 most=""
while :; do
    case "${1:-}" in
        --most)
            [ "$#" -ge 2 ] || refuse "--most takes the number of packages a diff run mutates at most"
            most=$2
            shift 2
            case "$most" in
                ''|*[!0-9]*|0*) refuse "--most takes the number of packages a diff run mutates at most, 1 or more, not '$most'" ;;
            esac ;;
        --jobs)
            [ "$#" -ge 2 ] || refuse "--jobs takes a number of packages to run at once"
            jobs=$2
            shift 2
            case "$jobs" in
                ''|*[!0-9]*|0*) refuse "--jobs takes a number of packages to run at once, 1 or more, not '$jobs'" ;;
            esac ;;
        --diff)
            [ "$#" -ge 2 ] || refuse "--diff takes a base commit"
            diff_base=$2
            shift 2
            case "$diff_base" in
                ''|-*) refuse "--diff takes a base commit, not '$diff_base'" ;;
            esac ;;
        --deadline)
            [ "$#" -ge 2 ] || refuse "--deadline takes a time in seconds since the epoch"
            deadline=$2
            shift 2
            case "$deadline" in
                ''|*[!0-9]*) refuse "--deadline takes a time in seconds since the epoch, not '$deadline'" ;;
            esac ;;
        *) break ;;
    esac
done
[ "$#" -gt 0 ] || refuse "no scope given. Usage: scripts/mutation.sh [--diff <base>] [--deadline <epoch seconds>] [--jobs <n>] [--most <n>] all | none | <package directory>..."
# The weekly pass judges every package whole, so only a diff run, a push's, has a cap.
[ -z "$most" ] || [ -n "$diff_base" ] || refuse "--most caps a diff run's packages; without --diff every package in scope is mutated"
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
trap '[ -z "${sampler:-}" ] || kill "$sampler" 2>/dev/null; rm -rf "$work"' EXIT
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
    # The path to git is written in, not passed in the environment: gremlins runs the tests, and
    # a test runner such as testscript starts its commands with PATH and little else, so a test
    # that runs git reaches this shim without ERRATA_REAL_GIT. The ERRATA_MUTATION_ variables
    # are read only on gremlins' own call, which has the whole environment.
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
SHIM
    printf 'exec %s "$@"\n' "'$(printf '%s' "$real_git" | sed "s/'/'\\\\''/g")'" >>"$work/shim/git"
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
    in_copy=yes
    echo "==> mutation: running from a copy of the tracked tree, $(du -sk . | cut -f1) KiB"
else
    echo "==> mutation: not a git checkout, so gremlins runs in the project directory"
fi

# Case expressions. Go's coverage profile has a block for a case clause's body and none for its
# expressions, so gremlins read every mutant in a case expression as not covered and never ran
# it, however the tests ran the arm; seven packages held a lowered mutant coverage floor for it,
# and code chose if over switch to be measured (#209). So the `go` gremlins finds first on PATH
# is a shim, scripts/goshim.sh: on a `go test` with -coverprofile, gremlins' coverage pass, it
# runs go and then scripts/casecover on the profile, which adds a covered block for the
# expressions of each case clause, and the statement of each select arm, whose body the tests
# ran. Gremlins then runs those mutants and
# judges them killed or lived like any other; a clause whose body no test ran stays not covered.
# Every other call goes to go unchanged, a mutant's go test among them. The helper is in
# scripts/, not ci/, because the public tree has no ci/ and make mutation runs there too. The
# script's own go test runs (measure_pass, measure_mutant) call go itself, not the shim. The
# paths the shim takes are written into the `go` that calls it, not passed in the environment,
# for the reason the git shim gives below.
[ -f scripts/casecover/main.go ] || refuse "scripts/casecover is missing; it covers case expressions for gremlins"
[ -f scripts/goshim.sh ] || refuse "scripts/goshim.sh is missing; it runs scripts/casecover for gremlins"
mkdir "$work/bin"
go build -o "$work/bin/casecover" ./scripts/casecover || refuse "could not build scripts/casecover"
# quote <text>: the text as one single quoted word for sh.
quote() {
    printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}
# write_goshim <directory>: the `go` gremlins finds first, in that directory, with the tree it
# runs in, the working directory, as casecover's module root. A package run at once beside
# others runs in a copy of the tree of its own (run_slot) and writes one of its own, so
# casecover reads the files gremlins tests. goshim_dir names the one run_gremlins puts on PATH.
write_goshim() {
    mkdir -p "$1"
    {
        echo '#!/bin/sh'
        echo "exec /bin/sh $(quote "$(pwd)/scripts/goshim.sh") $(quote "$(command -v go)") $(quote "$work/bin/casecover") $(quote "$(pwd)") \"\$@\""
    } >"$1/go"
    chmod +x "$1/go"
    goshim_dir=$1
}
write_goshim "$work/goshim"
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
# second pass when the first is over it (unleash). A figure at or below its floor fails. A floor
# may only rise: ci/test-ci.sh refuses one lower than at the release tag before HEAD, from the
# first release tag that holds the table, unless the line above the row records the owner's
# approval. Before that tag the comparison is skipped; in CI a clone with no release tag fails
# it, and outside CI it is skipped. scripts/mutation-floors.txt's header gives the line's form.
#
# On a diff run (--diff) the same floors judge only the mutants on the changed lines, so the
# figures are those of the new code alone, over a denominator that may be a handful: one lived
# mutant of six is 83.33% efficacy, under 85, and fails the run until a test kills it. That is
# the intent: new code meets the bar the package meets. The other side of a small denominator:
# a mutant in a package level declaration, or after a statement's first func literal, always
# reads as not covered, so a change made only of such lines can fall under its package's mutant
# coverage floor with every line tested. So a diff
# run judges mutant coverage only from MCOVER_MIN_DIFF mutants on its changed lines, and
# efficacy always (owner decision 2026-10-24); a whole run judges both at any count. Efficacy
# needs a mutant some test reaches, so a diff run whose changed lines hold mutants and no test
# reaches any of them, new code with no test, fails, naming those lines: gremlins prints each
# mutant not covered on a diff run (-S ltc).
MCOVER_MIN_DIFF=10

# The floors themselves sit in one table, scripts/mutation-floors.txt: one row a package, its
# efficacy floor, its mutant coverage floor and its least timeout coefficient, each with its
# reason. floors_for reads a package's row; a package with no row is refused before any pass
# runs.
FLOORS_FILE=scripts/mutation-floors.txt
[ -f "$FLOORS_FILE" ] || refuse "$FLOORS_FILE is missing; it holds every package's floors"
floors_table=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$FLOORS_FILE")

# floors_for <package>: efficacy, mcover and base_coef from the package's row; false when the
# table has no row for it, or more than one, or a row whose figures are not whole numbers.
floors_for() {
    row=$(printf '%s\n' "$floors_table" | awk -v p="$1" '$1 == p { n++; r = $2 " " $3 " " $4; ok = (NF == 4 && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ && $4 ~ /^[0-9]+$/) } END { if (n != 1 || !ok) exit 1; print r }') || return 1
    set -- $row
    efficacy=$1 mcover=$2 base_coef=$3
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
#
# The coverage pass times the tests alone, on a warm build. A mutant also rebuilds its package,
# links the test binary, and builds beside the other workers; and when the mutation changes the
# package's export data (an inlinable function), or on a worker's first mutant, it rebuilds
# every package of the test binary that imports it too. For internal/extract/core that is 19
# more packages, every reader and the facade, since core's tests read files through
# extract.Build. On a CI node with an i7-6500U,
# the job held to 2 CPUs, on 2026-10-28, core's pass measured 3.156 s, so the limit was 15.8 s,
# and 103 of its 123 mutants timed out at it, the job at its CPU limit and throttled in 80 to
# 100% of its periods; on the build node the same mutants took 4.3 s each and all but one were
# killed (#193). So the script also times a mutant (measure_mutant), and the limit is at least
# MUTANT_FLOOR times that, aiming at half again as much, as LIMIT_AIM is to LIMIT_FLOOR.
MUTANT_FLOOR=2
# With a deadline, a package starts only when MEASURE_LEAST seconds are left: timing its pass
# and a mutant took 17 s for internal/extract/core on the build node held to one CPU, and the
# measuring stops at the deadline in any case (bounded).
# MUTATION_MEASURE_LEAST sets it otherwise; ci/test-ci.sh lowers it for its deadline tests.
MEASURE_LEAST=${MUTATION_MEASURE_LEAST:-60}
case "$MEASURE_LEAST" in ''|*[!0-9]*) refuse "MUTATION_MEASURE_LEAST is '$MEASURE_LEAST', not a number of seconds" ;; esac

# measure_pass <package>: pass_s, the seconds of the package's coverage pass as gremlins runs
# it, the shorter of two runs after one that builds it; empty when the tests fail, which
# gremlins then reports itself. The shorter, because gremlins' pass came out shorter than one
# measured run: internal/semver's 0.23 s against 0.88 s on 2026-10-24, which put its limit at
# 4.2 s and cost a second pass of 3.3 minutes.
measure_pass() {
    tags=$(sed -n 's/^  tags: *\([^ #]*\).*/\1/p' .gremlins.yaml)
    bounded go test ${tags:+-tags "$tags"} -cover -coverprofile "$pw/measure.cov" -run '^$' "./$1/..." >/dev/null 2>&1 || true
    pass_s=""
    for _ in 1 2; do
        start=$(date +%s%N)
        bounded go test ${tags:+-tags "$tags"} -cover -coverprofile "$pw/measure.cov" "./$1/..." >/dev/null 2>&1 || { pass_s=""; return 0; }
        pass_s=$(awk -v a="$start" -v b="$(date +%s%N)" -v p="$pass_s" 'BEGIN { s = (b - a) / 1e9; if (p != "" && p + 0 < s) s = p; printf "%.3f", s }')
    done
}

# workers: how many mutants a package's gremlins runs at once: pkg_workers, set below from
# .gremlins.yaml and the packages that run at once.
workers() {
    echo "$pkg_workers"
}

# measure_mutant <package>: mutant_s, the seconds a mutant takes as gremlins runs one, its
# build and its tests, with as many at once as gremlins has workers; empty when it cannot be
# measured. Gremlins runs a mutant as `go test -tags <tags> -timeout <t> -failfast <package>`
# with GOFLAGS as here. The stand-in for a mutant is an exported function appended to one of
# the package's Go files: it changes the package's export data, so every package that imports
# it rebuilds, the most a mutant costs to build (a comment would rebuild the package alone). The
# tests then all run, as for a mutant that lives. A run with the same flags that runs no test
# comes first, so the timed runs find the packages that did not change built, as a worker
# does after its first mutant. Only in the copy of the tree: it edits the file, then puts it
# back.
measure_mutant() {
    mutant_s=""
    [ -n "${in_copy:-}" ] || return 0
    tags=$(sed -n 's/^  tags: *\([^ #]*\).*/\1/p' .gremlins.yaml)
    # The first Go file the build takes, as go list names it with gremlins' tags: a file a build
    # constraint leaves out, or one whose name starts with _ or ., would change nothing that is
    # built, and the probe would time the tests alone.
    src=$(go list ${tags:+-tags "$tags"} -f '{{if .GoFiles}}{{index .GoFiles 0}}{{end}}' "./$1" 2>/dev/null) || return 0
    [ -n "$src" ] || return 0
    src=$1/$src
    bounded go test ${tags:+-tags "$tags"} -run '^$' "./$1" >/dev/null 2>&1 || return 0
    cp "$src" "$pw/probe.keep"
    printf '\nfunc ErrataMutationProbe() {}\n' >>"$src"
    w=$(workers)
    start=$(date +%s%N)
    i=0 probes=""
    while [ "$i" -lt "$w" ]; do
        { bounded go test ${tags:+-tags "$tags"} -failfast "./$1" >/dev/null 2>&1 || : >"$pw/probe.failed"; } &
        probes="$probes $!"
        i=$((i + 1))
    done
    # Each probe by its own id: a bare wait would also wait for the memory sampler, which
    # never ends.
    for id in $probes; do wait "$id" || true; done
    end=$(date +%s%N)
    cp "$pw/probe.keep" "$src"
    if [ -e "$pw/probe.failed" ]; then
        rm -f "$pw/probe.failed"
        echo "==> mutation: $1: could not time a mutant: its tests failed with a function added to $src"
        return 0
    fi
    mutant_s=$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.3f", (b - a) / 1e9 }')
}

# limit_floor: the least limit per mutant, in seconds: LIMIT_FLOOR, or MUTANT_FLOOR times the
# measured mutant when that is more.
limit_floor() {
    awk -v f="$LIMIT_FLOOR" -v k="$MUTANT_FLOOR" -v m="${mutant_s:-0}" 'BEGIN { l = k * m; if (l < f) l = f; s = sprintf("%.1f", l); sub(/\.0$/, "", s); print s }'
}

# coefficient_for <package> <pass seconds>: the larger of the least coefficient in the package's
# floors row (base_coef, from floors_for) and the one that makes a pass that long half again the
# least limit (LIMIT_AIM, unless the measured mutant asks for more). A pass under 10 ms counts
# as 10 ms.
coefficient_for() {
    base=$base_coef
    if [ -z "$2" ]; then
        echo "$base"
        return
    fi
    awk -v b="$base" -v aim="$(awk -v l="$(limit_floor)" -v a="$LIMIT_AIM" -v f="$LIMIT_FLOOR" 'BEGIN { print l * a / f }')" -v s="$2" 'BEGIN {
        if (s < 0.01) s = 0.01
        c = int(aim / s); if (c < aim / s) c++
        print (c > b ? c : b)
    }'
}

# gremlins_pass_s: the seconds of the coverage pass gremlins timed, from its "done in" line, a
# Go duration such as 812.3ms, 4.3s or 1m2.5s; empty when it printed none.
gremlins_pass_s() {
    sed -n 's/.*Gathering coverage\.\.\. *done in \([0-9.hmsµun]*\).*/\1/p; s/^done in \([0-9.hmsµun]*\)$/\1/p' "$pw/out" | head -n 1 | awk '
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
    n=$(sed -n "s/.*$1: \([0-9]*\).*/\1/p" "$pw/out" | head -n 1)
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
        -e "s/^  workers: [0-9]*/  workers: $pkg_workers/" \
        .gremlins.yaml >"$pw/gremlins.base"
    {
        echo "    - /"
        awk -v pre="    - ^$1/" 'index($0, pre) == 1 && length($0) > length(pre) { print "    - ^" substr($0, length(pre) + 1) }' .gremlins.yaml
    } >"$pw/excludes"
    awk -v extra="$pw/excludes" '
        { print }
        /^  exclude-files:$/ { while ((getline line < extra) > 0) print line; n++ }
        END { if (n != 1) exit 1 }
    ' "$pw/gremlins.base" >"$pw/gremlins.yaml" \
        || refuse "a copy of .gremlins.yaml has no single '  exclude-files:' line to add the package's exclusions to"
    for key in "    efficacy: 0" "    mutant-coverage: 0" "  timeout-coefficient: $coef" "  workers: $pkg_workers"; do
        grep -q "^$key\( \|\$\)" "$pw/gremlins.yaml" || refuse "could not write '$key' into a copy of .gremlins.yaml"
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

# Memory (#152). The pod's cgroup, MUTATION_CGROUP (default /sys/fs/cgroup, the job's own
# under cgroup v2), is read each second while a package runs: memory.current, which counts the
# page cache, and anon from memory.stat, the working set that reclaim cannot give back. Each
# package's line gives the highest of each. anon is the package's own, since the processes of
# the package before it have exited. memory.current is the pod's: the kernel keeps the page
# cache earlier packages read until it reclaims it, so after a few packages it reads near the
# limit whatever the package needs, and the line says so. memory.peak is the cgroup's highest
# since the pod started, so the line quotes it as the pod's so far. A whole pass of core
# or ini reached memory.peak at the 4 GiB limit on 2026-10-25 and passed: the page cache may
# make up much of that, and the anon beside it says how much. When packages run at once the
# cgroup holds them all, and the one line covers them together.
CGROUP=${MUTATION_CGROUP:-/sys/fs/cgroup}
sampler=""

# mib <bytes>: in MiB, whole.
mib() {
    awk -v b="$1" 'BEGIN { printf "%d", b / 1048576 }'
}

# memory_readable: the cgroup has the two files the samples read.
memory_readable() {
    [ -r "$CGROUP/memory.current" ] && [ -r "$CGROUP/memory.stat" ]
}

# memory_note: once, before the first package, the line that says memory is not measured.
memory_note() {
    memory_readable || echo "==> mutation: memory not measured: $CGROUP has no readable memory.current and memory.stat"
}

# memory_sample <file>: the file holds the highest memory.current and anon seen, in bytes.
memory_sample() {
    memory_readable || return 0
    c=$(cat "$CGROUP/memory.current" 2>/dev/null) || return 0
    a=$(sed -n 's/^anon //p' "$CGROUP/memory.stat" 2>/dev/null) || return 0
    old="0 0"
    [ ! -s "$1" ] || old=$(cat "$1")
    echo "$old ${c:-0} ${a:-0}" | awk '{ print ($3 > $1 ? $3 : $1), ($4 > $2 ? $4 : $2) }' >"$1.new" && mv "$1.new" "$1"
}

# memory_start <file>: sample now and then each second, in the background, into the file.
memory_start() {
    rm -f "$1"
    memory_readable || return 0
    memory_sample "$1"
    (while :; do sleep 1; memory_sample "$1"; done) &
    sampler=$!
}

# memory_stop <file>: stop the samples, and take a last one.
memory_stop() {
    [ -n "$sampler" ] || return 0
    kill "$sampler" 2>/dev/null || true
    wait "$sampler" 2>/dev/null || true
    sampler=""
    memory_sample "$1"
}

# memory_line <what> <file>: the line for one package, or for the packages that ran at once.
memory_line() {
    [ -s "$2" ] || return 0
    set -- "$1" $(cat "$2")
    limit=$(cat "$CGROUP/memory.max" 2>/dev/null || true)
    case "$limit" in
        ''|*[!0-9]*) limit="no limit" ;;
        *) limit="a $(mib "$limit") MiB limit" ;;
    esac
    peak=$(cat "$CGROUP/memory.peak" 2>/dev/null || true)
    case "$peak" in
        ''|*[!0-9]*) peak="" ;;
        *) peak="; the pod's memory.peak so far $(mib "$peak") MiB" ;;
    esac
    echo "==> memory $1: anon at most $(mib "$3") MiB; the pod's memory.current at most $(mib "$2") MiB while it ran, the page cache earlier packages left included; sampled each second, of $limit$peak"
}

# memory_summary: the memory lines again, after the verdicts, one a package in the order of
# the scope, so the weekly pass's figures sit together at the end of its log.
memory_summary() {
    memory_readable || return 0
    echo "==> mutation: memory of each pass (anon, the working set, is the package's own; memory.current is the pod's and counts the page cache earlier packages left):"
    if [ -e "$work/memory.line" ]; then
        cat "$work/memory.line"
    else
        for i in $run; do
            [ ! -e "$work/p/$i/memory.line" ] || cat "$work/p/$i/memory.line"
        done
    fi
}

# left_s: the seconds left before the deadline, 0 when it has passed.
left_s() {
    l=$((deadline - $(date +%s)))
    [ "$l" -gt 0 ] || l=0
    echo "$l"
}

# fits <seconds>: that long, from now, ends before the deadline; always, with no deadline.
fits() {
    [ -z "$deadline" ] || awk -v s="$1" -v l="$(left_s)" 'BEGIN { exit !(s <= l) }'
}

# bounded <command...>: the command, stopped at the deadline when there is one (exit 124, as
# `timeout` gives, or 137 when it had to kill). timeout signals its whole process group, so the
# go test runs under gremlins stop too. Every command that runs tests, the measuring included,
# runs this way, so no package runs past the deadline.
bounded() {
    if [ -n "$deadline" ]; then
        # timeout takes 0 as no limit at all.
        timeout -k 10 "$(left_s | sed 's/^0$/1/')" "$@"
    else
        "$@"
    fi
}

# run_gremlins <diff file> <arguments...>: gremlins with those arguments, bounded, with the go
# shim first on PATH, and with --diff and the git shim when there is a diff file.
run_gremlins() {
    d=$1
    shift
    if [ -n "$d" ]; then
        PATH="$goshim_dir:$work/shim:$PATH" ERRATA_MUTATION_BASE=$diff_base ERRATA_MUTATION_DIFF=$d \
            ERRATA_MUTATION_ASKED="$pw/asked" \
            bounded "$GREMLINS" unleash --config "$pw/gremlins.yaml" --diff "$diff_base" "$@"
    else
        PATH="$goshim_dir:$PATH" bounded "$GREMLINS" unleash --config "$pw/gremlins.yaml" "$@"
    fi
}

# pass_once <package> [<diff file>]: one gremlins pass, its output shown and kept, and its
# counts. With a diff file, gremlins runs with --diff and the shim first on PATH. ran_out is
# set when the deadline stopped it.
pass_once() {
    rm -f "$pw/rc" "$pw/asked"
    {
        rc=0
        if [ -n "${2:-}" ]; then
            run_gremlins "$2" -S ltc "$1" || rc=$?
        else
            run_gremlins "" -S lt "$1" || rc=$?
        fi
        echo "$rc" >"$pw/rc"
    } 2>&1 | tee "$pw/out"
    rc=$(cat "$pw/rc")
    ran_out=""
    if [ -n "$deadline" ] && { [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; } && [ "$(left_s)" -eq 0 ]; then
        ran_out=yes
    fi
    killed=$(count Killed) lived=$(count Lived) timedout=$(count "Timed out") uncovered=$(count "Not covered")
    skipped=$(count Skipped)
    ran=$((killed + lived + timedout))
}

# dry_once <package> [<diff file>]: gremlins' dry run, which runs its coverage pass and counts
# the mutants without running them: runnable, the mutants a pass would run, and dry_pass,
# gremlins' pass in seconds. runnable is empty when the dry run printed no count.
dry_once() {
    rc=0
    run_gremlins "${2:-}" --dry-run "$1" >"$pw/out" 2>&1 || rc=$?
    runnable=$(sed -n 's/.*Runnable: \([0-9]*\).*/\1/p' "$pw/out" | head -n 1)
    dry_pass=$(gremlins_pass_s)
    [ "$rc" -eq 0 ] || runnable=""
}

# per_mutant: the seconds a mutant takes, as many at once as gremlins has workers: the measured
# mutant, or the whole limit when there is none.
per_mutant() {
    echo "${mutant_s:-$(seconds "$coef" "${pass_s:-$LIMIT_AIM}")}"
}

# estimate <mutants> <pass seconds>: the seconds a pass of that many mutants takes, gremlins'
# coverage pass and then the mutants, per_mutant for each round of as many as it has workers.
estimate() {
    awk -v n="$1" -v w="$(workers)" -v per="$(per_mutant)" -v g="${2:-0}" 'BEGIN { r = int(n / w); if (r * w < n) r++; printf "%d", g + r * per + 1 }'
}

# left_out <package> <why>: the package is not judged, for want of time, and the summary
# says so and why; the weekly pass judges it whole.
left_out() {
    echo "==> mutation: $1: LEFT OUT, $2; the weekly pass judges it"
    echo "$1: LEFT OUT for time, $2; left to the weekly pass." >>"$pw/summary"
    echo "$1" >"$pw/left"
}

# scope_note: what a summary line adds for a run over the changed lines, after judge_pass: the
# mutants outside them, and mutant coverage left unjudged when they hold too few. It is empty
# for a run over whole packages.
scope_note() {
    [ -n "$diff" ] || return 0
    printf ' Outside the changed lines: %s skipped.' "$skipped"
    if [ "$all" -gt 0 ] && [ "$all" -lt "$MCOVER_MIN_DIFF" ]; then
        printf ' Mutant coverage not judged: the changed lines hold %s of the %s mutants it needs.' "$all" "$MCOVER_MIN_DIFF"
    fi
}

# judge_pass <package> [no-cap]: the verdict on the pass's counts, against the package's floors:
# figures, verdict and rc (0 when it passed). no-cap leaves out the timeout cap, the one check a
# busy node can fail on its own; every other check fails on what the tests did, whatever the
# timeouts, since a timed out mutant counts as covered and killed.
judge_pass() {
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
    elif ! grep -q 'Killed: [0-9]' "$pw/out" && grep -q '^No results to report\.$' "$pw/out"; then
        # What gremlins prints, and all it prints, for a package with no mutants.
        figures="No mutants."
    elif ! grep -q 'Killed: [0-9]' "$pw/out"; then
        verdict="FAILED, gremlins printed no counts"
        rc=1
    elif [ -n "$diff" ] && [ "$ran" -eq 0 ] && [ "$uncovered" -gt 0 ]; then
        lines=$(sed -n 's/.*NOT COVERED .* at \([^ ]*\):[0-9]*$/\1/p' "$pw/out" | sort -u | head -n 10 | tr '\n' ' ' | sed 's/ $//; s/ /, /g')
        verdict="FAILED, no test reaches any of its $uncovered mutants on the changed lines (${lines:-lines not printed})"
        rc=1
    elif [ "$all" -gt 0 ] && { [ -z "$diff" ] || [ "$all" -ge "$MCOVER_MIN_DIFF" ]; } && [ $((ran * 100)) -le $((all * mcover)) ]; then
        verdict="FAILED, mutant coverage at or below $mcover"
        rc=1
    elif [ "$ran" -gt 0 ] && [ $(((killed + timedout) * 100)) -le $((ran * efficacy)) ]; then
        verdict="FAILED, efficacy at or below $efficacy"
        rc=1
    elif [ "${2:-}" != no-cap ] && over_cap; then
        verdict="FAILED, $timedout of $ran mutants timed out, over $TIMEOUT_CAP% and over $TIMEOUT_MIN$again"
        rc=1
    elif [ -n "$diff" ] && [ ! -e "$pw/asked" ]; then
        # The shim was not asked for the diff, so gremlins judged every mutant, not the changed
        # lines: it no longer calls git as the shim expects.
        verdict="FAILED, gremlins never asked the git shim for the diff"
        rc=1
    fi
}

# unleash <package>: a gremlins pass judged against the package's floors. A package over the
# timeout cap runs once more, and the second pass is judged: a mutant times out when the node
# is busy as well as when it hangs, and one that hangs times out on both passes. On the build
# node, CI's test jobs and image builds running side by side timed out 2 of json's 13 mutants
# on 2026-10-22 and 14 and 20 of config's 110 on 2026-10-23; each package had none on a rerun.
unleash() {
    p=$1
    if ! fits "$MEASURE_LEAST"; then
        left_out "$p" "$(left_s) s were left before the deadline, under the $MEASURE_LEAST s its pass and a mutant take to time"
        return 0
    fi
    floors_for "$p"
    measure_pass "$p"
    measure_mutant "$p"
    if ! fits 1; then
        left_out "$p" "the deadline came while its pass and a mutant were timed"
        return 0
    fi
    coef=$(coefficient_for "$p" "$pass_s")
    if [ -n "$pass_s" ]; then
        limit="limit per mutant $(seconds "$coef" "$pass_s") s: coefficient $coef times a pass of $pass_s s"
    else
        limit="limit per mutant unknown: the tests failed when measured; coefficient $coef"
    fi
    name="$p (floors: efficacy $efficacy, mutant coverage $mcover; $limit)"
    diff=""
    if [ -n "$diff_base" ]; then
        diff=$(diff_file "$p")
        name="$name, the lines changed since $diff_base"
    fi
    echo "==> mutation: $name"
    if [ -n "$mutant_s" ]; then
        echo "==> mutation: $p: a mutant took $mutant_s s to build and test, $(workers) at once, so each gets at least $(limit_floor) s"
    fi
    config_for "$p" "$coef"
    # With a deadline, a dry run counts the mutants first, and a package whose pass would not
    # end before the deadline is left out whole rather than stopped halfway.
    if [ -n "$deadline" ]; then
        dry_once "$p" "$diff"
        if [ -n "$runnable" ]; then
            need=$(estimate "$runnable" "$dry_pass")
            if ! fits "$need"; then
                left_out "$p" "its $runnable mutants need about $need s, $(workers) at once at $(per_mutant) s each, and $(left_s) s are left before the deadline"
                return 0
            fi
            echo "==> mutation: $p: $runnable mutants, about $need s of the $(left_s) s left before the deadline"
        fi
    fi
    pass_once "$p" "$diff"
    if [ -n "$ran_out" ]; then
        left_out "$p" "the deadline came during its pass"
        return 0
    fi
    again=""
    # The limit gremlins gave, from its own pass. Under the floor with a mutant timed out, the
    # timeout may be the short limit's: the package runs once more, its coefficient taken from
    # gremlins' figure, and the second pass is judged.
    short=""
    gpass=$(gremlins_pass_s)
    if [ -n "$gpass" ] && [ "$timedout" -gt 0 ] \
        && awk -v l="$(seconds "$coef" "$gpass")" -v f="$(limit_floor)" 'BEGIN { exit !(l < f) }'; then
        short=yes
    fi
    if [ "$rc" -eq 0 ] && grep -q 'Killed: [0-9]' "$pw/out" && { over_cap || [ -n "$short" ]; }; then
        need=$(estimate "$ran" "$gpass")
        if ! fits "$need"; then
            # A timeout may be the node's and not the mutant's, which is why a second pass
            # judges; with no time for it the package is left out, as when the deadline stops
            # that pass, and never failed on the first pass's timeouts. A check the timeouts
            # cannot move still fails it: mutant coverage, efficacy (a failure there is
            # mutants that lived, which no timeout caused, since a timeout counts as killed),
            # a changed line no test reaches, the shim never asked.
            echo "==> mutation: $p: limit per mutant $(seconds "$coef" "$gpass") s: coefficient $coef times gremlins' pass of $gpass s"
            judge_pass "$p" no-cap
            if [ "$rc" -ne 0 ]; then
                echo "$p: $verdict, judged on its first pass, with no time for the second. $figures Timed out: $timedout of $ran.$(scope_note)" >>"$pw/summary"
                return 1
            fi
            if [ -n "$short" ] && ! over_cap; then
                why="its limit of $(seconds "$coef" "$gpass") s came out under the floor of $(limit_floor) s with $timedout mutants timed out"
            else
                why="$timedout of its $ran mutants timed out"
            fi
            left_out "$p" "$why, and the second pass that judges them needs about $need s, more than the $(left_s) s left before the deadline"
            return 0
        else
            if [ -n "$short" ]; then
                was=$(seconds "$coef" "$gpass")
                coef=$(coefficient_for "$p" "$gpass")
                config_for "$p" "$coef"
                echo "==> mutation: $p: $timedout of $ran mutants timed out under a limit of $was s, below the floor of $(limit_floor) s; running the package once more at coefficient $coef"
            else
                echo "==> mutation: $p: $timedout of $ran mutants timed out, over $TIMEOUT_CAP% and over $TIMEOUT_MIN; running the package once more"
            fi
            pass_once "$p" "$diff"
            if [ -n "$ran_out" ]; then
                left_out "$p" "the deadline came during its second pass"
                return 0
            fi
            gpass=$(gremlins_pass_s)
            again=", on its second pass too"
        fi
    fi
    if [ -n "$gpass" ]; then
        echo "==> mutation: $p: limit per mutant $(seconds "$coef" "$gpass") s: coefficient $coef times gremlins' pass of $gpass s"
    fi
    judge_pass "$p"
    echo "$p: $verdict. $figures Timed out: $timedout of $ran.$(scope_note)" >>"$pw/summary"
    [ "$rc" -eq 0 ]
}

if [ "$1" = all ]; then
    mod=$(go list -m)
    go list ./internal/... >"$work/packages"
    set -- $(sed "s|^$mod/||" "$work/packages")
    [ "$#" -gt 0 ] || refuse "go list found no package under internal/"
    echo "==> mutation: every package: $*"
fi

# A package with no row is refused before the first pass, not an hour into a run.
for p in "$@"; do
    excluded_whole "$p" || floors_for "$p" \
        || refuse "$p has no row in $FLOORS_FILE, or more than one, or one that is not four fields of a package and three whole numbers: add one, with its efficacy floor, mutant coverage floor and least coefficient"
done

# Each package in scope gets a directory of its own, $work/p/<its place in the scope>: its name,
# its scratch files (pw), its summary line, and a file `left` when it was left out for time.
# The summary is put together from them in the order of the scope, however the packages ran.
mkdir "$work/p"
i=0 run=""
for p in "$@"; do
    i=$((i + 1))
    mkdir "$work/p/$i"
    echo "$p" >"$work/p/$i/name"
    if excluded_whole "$p"; then
        echo "$p: skipped, .gremlins.yaml excludes it whole." >"$work/p/$i/summary"
        continue
    fi
    if [ -n "$diff_base" ] && [ ! -e "$(diff_file "$p")" ]; then
        echo "==> mutation: $p: its Go code is as at $diff_base, so there are no changed lines to mutate; the weekly pass judges it whole"
        echo "$p: skipped, its Go code is as at $diff_base; left to the weekly pass." >"$work/p/$i/summary"
        continue
    fi
    run="$run $i"
done
count=$(echo $run | wc -w)

# The cap (--most, #151). A push mutates at most that many packages, so its job ends inside its
# 25 minutes however many tickets it lands; ci/push-mutation.sh says how the number was picked.
# The packages with the most changed lines run, the lines a diff adds or changes in the
# package's own Go code: those hold most of the mutants a push brings, and a package with a few
# changed lines leaves the least unjudged until the weekly pass. Counting mutants would take a
# gremlins dry run, a coverage pass, for every package, the cost the cap is there to save. A tie
# goes to the package first in the scope. The rest are left out as for time, named, and the run
# exits 3. The packages that run start with the most changed lines, so the longest start first
# when they run at once.
# changed_lines <diff file>: the lines it adds to the package's own Go code, not its tests. The
# diff also holds the files of packages nested below, which this package's pass excludes.
changed_lines() {
    awk '/^\+\+\+ / { f = substr($0, 7); own = (f !~ /\// && f ~ /\.go$/ && f !~ /_test\.go$/); next }
        own && /^\+/ { n++ }
        END { print n + 0 }' "$1"
}
if [ -n "$most" ] && [ "$count" -gt "$most" ]; then
    ranked=$(for i in $run; do
        printf '%s %s\n' "$(changed_lines "$(diff_file "$(cat "$work/p/$i/name")")")" "$i"
    done | sort -k1,1nr -k2,2n -s)
    run=$(printf '%s\n' "$ranked" | head -n "$most" | awk '{ printf " %s", $2 }')
    printf '%s\n' "$ranked" | tail -n +"$((most + 1))" | while read -r lines i; do
        pw=$work/p/$i
        left_out "$(cat "$pw/name")" "over the push's cap of $most packages, which go to those with the most changed lines; it has $lines"
    done
    count=$most
fi

# Packages at once: --jobs, and no more than there are packages to run. The mutants that run
# at once stay at .gremlins.yaml's workers, which were sized for the CI job pod's two cores, so
# each package's gremlins gets an equal share of them, and at least one.
yaml_workers=$(sed -n 's/^  workers: *\([0-9]*\).*/\1/p' .gremlins.yaml)
yaml_workers=${yaml_workers:-1}
at_once=$jobs
[ "$at_once" -le "$count" ] || at_once=$count
# No more packages at once than there are workers, so no more mutants run at once than one
# package at a time runs: a pod with more CPUs than that would otherwise give each package one
# worker and run more mutants at once than the limit per mutant was ever measured at (#193).
if [ "$at_once" -gt "$yaml_workers" ]; then
    echo "==> mutation: --jobs $jobs, but at most $yaml_workers packages run at once, the gremlins workers .gremlins.yaml sets, so no more mutants run at once than one package at a time runs"
    at_once=$yaml_workers
fi
[ "$at_once" -ge 1 ] || at_once=1
pkg_workers=$((yaml_workers / at_once))
[ "$pkg_workers" -ge 1 ] || pkg_workers=1

memory_note
failed=0
if [ "$at_once" -le 1 ]; then
    for i in $run; do
        pw=$work/p/$i
        p=$(cat "$pw/name")
        memory_start "$pw/memory"
        unleash "$p" || failed=1
        memory_stop "$pw/memory"
        memory_line "$p" "$pw/memory" | tee "$pw/memory.line"
    done
else
    # run_slot: run the packages no other slot has claimed, one after the other. A claim is a
    # directory, which mkdir makes for one caller only. Each package runs in a copy of the tree
    # of its own, since timing a mutant edits a file of the package (measure_mutant), and its
    # output goes to its log, shown whole once every package has ended. The build cache is
    # shared: go keeps it safe for builds running at once, and one package then reuses what
    # another built.
    run_slot() {
        for i in $run; do
            mkdir "$work/claim/$i" 2>/dev/null || continue
            pw=$work/p/$i
            p=$(cat "$pw/name")
            echo "==> mutation: $p: started"
            status=0
            if [ -n "${in_copy:-}" ]; then
                cp -R "$work/tree" "$pw/tree"
                (cd "$pw/tree" && write_goshim "$pw/goshim" && unleash "$p") >"$pw/log" 2>&1 || status=$?
            else
                (unleash "$p") >"$pw/log" 2>&1 || status=$?
            fi
            echo "$status" >"$pw/status"
            echo "==> mutation: $p: ended"
        done
    }
    echo "==> mutation: $at_once of the $count packages run at once, each with $pkg_workers of the $yaml_workers gremlins workers .gremlins.yaml sets; each package's log follows once all have ended"
    mkdir "$work/claim"
    memory_start "$work/memory"
    k=0 slots=""
    while [ "$k" -lt "$at_once" ]; do
        run_slot &
        slots="$slots $!"
        k=$((k + 1))
    done
    # Each slot by its own id, not a bare wait, which would wait for the memory sampler too.
    for id in $slots; do wait "$id" || true; done
    memory_stop "$work/memory"
    # A package whose run stopped before its verdict, a refusal (exit 2) above all, wrote no
    # summary line: it gets one here, and a refusal makes the run exit 2, as it does when
    # packages run one at a time.
    names="" refused=""
    for i in $run; do
        pw=$work/p/$i
        p=$(cat "$pw/name")
        names="$names $p"
        echo "==> mutation: $p: its log"
        cat "$pw/log" 2>/dev/null || echo "==> mutation: $p: no log, since its slot ended before it ran the package"
        status=$(cat "$pw/status" 2>/dev/null || echo none)
        [ "$status" = 0 ] || failed=1
        [ "$status" != 2 ] || refused=yes
        if [ "$status" != 0 ] && [ ! -e "$pw/summary" ]; then
            if [ "$status" = none ]; then
                echo "$p: FAILED, its slot ended before it ran the package." >"$pw/summary"
            else
                echo "$p: FAILED, its run stopped with exit $status before a verdict; its log above says why." >"$pw/summary"
            fi
        fi
    done
    memory_line "$at_once at once:$names" "$work/memory" | tee "$work/memory.line"
fi

: >"$work/summary"
left=""
i=0
for p in "$@"; do
    i=$((i + 1))
    [ ! -e "$work/p/$i/summary" ] || cat "$work/p/$i/summary" >>"$work/summary"
    [ ! -e "$work/p/$i/left" ] || left="$left $p"
done

echo "==> mutation scope: $scope"
sed 's/^/==>   /' "$work/summary"
memory_summary
if [ "$failed" -ne 0 ]; then
    [ -z "$left" ] || echo "==> mutation: left out for time, and judged only by the weekly pass:$left"
    if [ -n "${refused:-}" ]; then
        echo "==> mutation: REFUSED in a package run at once, its log above says why; FAILED"
        exit 2
    fi
    echo "==> mutation: FAILED"
    exit 1
fi
# Packages left out for time and none failed: exit 3, which CI's mutation job allows as a
# warning (.gitlab-ci.yml), naming them; MUTATION_LEFT_OUT, a file, gets the list, so that
# ci/push-mutation.sh can tell this 3 from a failure once make has turned both into 2.
if [ -n "$left" ]; then
    [ -z "${MUTATION_LEFT_OUT:-}" ] || echo "${left# }" >"$MUTATION_LEFT_OUT"
    echo "==> mutation: LEFT OUT for time, and judged only by the weekly pass:$left"
    exit 3
fi
# Every package skipped, none judged and none left out: no changed line is Go code gremlins
# mutates (tests only, or code .gremlins.yaml excludes). ci/gate.sh reads this line as GREEN,
# apart from a run that judged nothing for lack of time (owner decision 2026-10-29).
if ! grep -q '^[^ ]*: \(passed\|FAILED\)' "$work/summary"; then
    echo "==> mutation: nothing in scope: no changed line is Go code that mutation tests, every package above was skipped"
fi
echo "==> mutation: passed"

# One entry point for a developer and for CI. `make check` is the gate.
# Tools install into ./bin at pinned versions and never enter errata's
# dependency graph. On Linux they are the projects' released binaries, each
# checked against its release's checksums file and, where the release signs,
# its signature (scripts/fetch-tool.sh); elsewhere they are built with go
# install. CI sets BIN to the CI image's tool directory, where they are already there.

GO        ?= go
BIN       ?= $(CURDIR)/bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X github.com/f-klement/errata/internal/cli.Version=$(VERSION)

GOLANGCI_LINT_VERSION := v2.15.0
GREMLINS_VERSION      := v0.6.0
GOVULNCHECK_VERSION   := v1.9.0
GORELEASER_VERSION    := v2.20.0
SYFT_VERSION          := v1.56.0
KO_VERSION            := v0.19.1
JQ_VERSION            := 1.8.2
COSIGN_VERSION        := v3.1.4

# The identity each signature must carry, from the publisher's own release workflow or install
# docs. syft (release.yaml, run from main) and goreleaser (release.yml, run on the tag) sign
# their checksums file. jq's ci.yml, run on the tag, signs SLSA provenance naming each binary.
# ko's provenance is signed by the SLSA generator's reusable workflow at a release tag, so its
# identity is a pattern, and the run must be ko-build/ko's at the version's tag, as ko's
# install docs check with slsa-verifier. A bump edits none of these: the only version they
# name comes from the version line. golangci-lint's release.yml, run on the tag, attests build
# provenance for each archive in GitHub's attestation API rather than in the release (owner
# decision 2026-11-02). gremlins signs nothing, and govulncheck publishes no binaries and is
# built.
SYFT_SIGNER          := https://github.com/anchore/syft/.github/workflows/release.yaml@refs/heads/main
GORELEASER_SIGNER    := https://github.com/goreleaser/goreleaser/.github/workflows/release.yml@refs/tags/$(GORELEASER_VERSION)
JQ_SIGNER            := https://github.com/jqlang/jq/.github/workflows/ci.yml@refs/tags/jq-$(JQ_VERSION)
GOLANGCI_LINT_SIGNER := https://github.com/golangci/golangci-lint/.github/workflows/release.yml@refs/tags/$(GOLANGCI_LINT_VERSION)
KO_SIGNER            := ^https://github\.com/slsa-framework/slsa-github-generator/\.github/workflows/generator_generic_slsa3\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$$

# cosign checks every signature above, so it is the one tool pinned by hand: the SHA-256 of
# each binary, from cosign's own checksums file at that version. After a bump, `make
# tool-sums` rewrites them and checks that file's signature with the cosign already in BIN
# (ci/README.md, Tool archives and their signatures). Until then the download fails its check.
COSIGN_SHA256_linux_amd64  := 424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa
COSIGN_SHA256_linux_arm64  := ade83b7aac62e131cacfe96d958948b2150cc9efec46a570a2649b8e27dedad2
COSIGN_SHA256_darwin_amd64 := 9d1e4abe2b6be279ed0d2ff706fde1ccd738747c834e3e6d44039c238867334f
COSIGN_SHA256_darwin_arm64 := 9f9b8cd557cf2e5b3df3373a00baa005a86b02391e3e06dd0ce37b3a1533d3a4

COVERAGE_MIN := 90
FUZZTIME     ?= 15s
# How long Go minimizes each input that adds coverage, as a time or a count. A worker that
# minimizes does not fuzz, and the coordinator counts its execs only when it returns, so a
# pass reads 0/sec meanwhile. Go's default of 60 s let one minimization take most of a 10 s
# pass at one worker (measured 2026-10-22: 3 to 34 thousand execs, against 42 to 49 thousand
# at 100x). The nightly campaigns pass 60s, where the time for a smaller corpus is there.
FUZZMINIMIZETIME ?= 100x
# Each fuzz target as package:Name, found in the tree: every func whose name starts with Fuzz and
# that takes a *testing.F, at the start of a line of a _test.go file under internal/, outside
# testdata, which go test does not build. A new target needs no edit here. `go test -fuzz` takes
# one target per run. The patterns hold no lone parenthesis, which would end make's $(shell).
FUZZ_TARGETS = $(shell find internal -name testdata -prune -o -name '*_test.go' -type f -print \
	| xargs grep -HE '^func Fuzz[A-Za-z0-9_]*[(][A-Za-z_][A-Za-z0-9_]* \*testing\.F[)]' \
	| sed 's|^\(.*\)/[^/]*_test\.go:func \(Fuzz[A-Za-z0-9_]*\).*|./\1:\2|' | LC_ALL=C sort -u)

.PHONY: check gate precheck seat-check build tools lint fmt test cover slow fuzz fuzz-first fuzz-rest mutation vuln dogfood public-dry release-dry image-dry ci-local ci-test tool-sums hooks cli-doc cli-doc-check loc clean

# ci/ is stripped from the public mirror, so a public clone runs the gate
# without the publish and autopatch targets. CI's test job passes CI_TEST=apart
# and leaves ci-test out, since the ci-test job runs it beside test (owner
# decision 2026-11-04); a gate anywhere else runs it. Only a command-line value
# counts, as for MUTATION_SCOPE below: a shell that exported CI_TEST=apart
# would make a seat's gate skip the shell suite and pass.
ifeq ($(origin CI_TEST),environment)
override CI_TEST :=
endif
# Exactly apart: "apart x" runs the suite.
ifeq ($(CI_TEST),apart)
GATE_SUITE :=
else
GATE_SUITE := ci-test
endif
INTERNAL := $(if $(wildcard ci/public-tree.sh),public-dry $(GATE_SUITE))

# `gate` is everything but mutation testing, for a quick run by hand: Gremlins
# over the whole module takes hours. CI's test job runs `gate` with fuzzing
# scoped to the packages a push changed (ci/mutation-scope.sh) and without
# ci-test, which its ci-test job runs. Its mutation job mutates the lines those
# packages changed (ci/push-mutation.sh), and the weekly pass runs every
# package whole.
#
# The checks that fail most come first (#279). In the v0.4.0 wave 3, three gates went red in
# fuzz after 22 to 73 minutes, on a target of the package the seat had changed. So fuzz-first
# fuzzes FUZZ_FIRST's packages right after lint, and fuzz-rest, at the end, fuzzes the rest of
# MUTATION_SCOPE: each target still runs once, for FUZZTIME. cover holds CRAP and needs the
# profile test writes, so it follows test straight away; test runs scripts/slowtests's own test.
gate: lint fuzz-first test cover slow fuzz-rest vuln dogfood image-dry $(INTERNAL)

check: gate mutation

# The checks that sank whole gates for want of a minute's look, for a seat to run on its final
# commit before handing it back. In the two waves up to 2026-10-25 five gates of 12 to 15 minutes
# went red on one of them: three on lint or on a node name in a public file (public-dry), and two
# on a new package without its `//go:build mutation` mutation_test.go, which only
# TestEveryTestedPackageLinksTheWatchdog in make test catches. A sixth, on 2026-11-01, went red
# on TestReadersImportNoOtherReader, after a package was split out below core. The TestArch tests
# fail on a package, an import or a kind that docs/architecture.md does not draw, which most new
# packages are. scripts/floors-gaps.sh refuses a package with no row in
# scripts/mutation-floors.txt, on which a gate went red 16 minutes in on 2026-11-03. Those run
# first, since they take seconds where lint takes minutes. The public mirror has no ci/, so there
# it is the tests, the floors and lint.
precheck:
	$(GO) test -count=1 -run '^TestEveryTestedPackageLinksTheWatchdog$$' ./internal/heapcap
	$(GO) test -count=1 -run '^(TestReadersImportNoOtherReader|TestReaderImportsRefuses)$$' ./internal/extract
	$(GO) test -count=1 -run '^TestArch' ./internal/cli
	GO=$(GO) sh scripts/floors-gaps.sh
	$(MAKE) --no-print-directory lint $(if $(wildcard ci/public-tree.sh),public-dry)

# What a seat runs on its final commit before handing it back (docs/agents/waves.md, item 7), in
# one call: ci/run-suite.sh make seat-check PKGS="internal/a internal/b", the package directories
# it touched. In the v0.4.0 wave 3, 7 of 30 gates went red on a check this runs in minutes: a fuzz
# find in the seat's own package (3), a CRAP score of 10 or more (2), and a slow test that
# scripts/slowtests did not list (2). The fast checks run first: the race tests of PKGS,
# scripts/slowtests's own test, precheck, PKGS's fuzz targets for 60 s each, then cover, which
# runs every test. A PKGS that is empty once canon_dirs has spelled it (./ and / are), holds
# anything but a plain directory from the root, or names a directory without Go files is refused, since fuzz would
# match none of it and pass. Globbing is off while it is checked, so a glob is checked as written.
# Last, ci/seat-review.sh prints the review checks report.txt holds (weakened, atbase, shuffle)
# against SEAT_BASE, by default the merge base with main. Their findings go to the reviewer and
# never fail seat-check. A runner's pod has no main, so through ci/run-suite.sh pass SEAT_BASE
# and ERRATA_CI_HISTORY=1 (docs/agents/waves.md, item 7). The public mirror has no ci/, so there
# seat-check ends with cover.
# canon_dirs: each directory with a leading ./, a trailing / or /. and a doubled / taken off, so
# it is spelled as the fuzz recipe and FUZZ_TARGETS spell a package. What is left is checked
# where it is used.
canon_dirs = $(patsubst %/,%,$(patsubst %/.,%,$(patsubst ./%,%,$(subst //,/,$(subst //,/,$(1))))))
SEAT_PKGS = $(call canon_dirs,$(PKGS))
SEAT_BASE ?= $(shell git merge-base origin/main HEAD 2>/dev/null || git merge-base main HEAD 2>/dev/null)
seat-check:
	@[ -n "$(strip $(SEAT_PKGS))" ] || { echo "make seat-check: REFUSED: PKGS is empty; name the package directories the change touched, such as PKGS=\"internal/config internal/cli\"" >&2; exit 2; }
	@set -f; for p in $(SEAT_PKGS); do \
		printf '%s\n' "$$p" | grep -qE '^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$$' || { echo "make seat-check: REFUSED: '$$p' in PKGS is not a package directory from the repository's root, such as internal/config: letters, digits, _, - and /, with no glob, . or .." >&2; exit 2; }; \
		find "$$p" -maxdepth 1 -name '*.go' -type f 2>/dev/null | grep -q . || { echo "make seat-check: REFUSED: '$$p' in PKGS holds no Go file" >&2; exit 2; }; \
	done
	$(GO) test -race -count=1 $(addprefix ./,$(SEAT_PKGS))
	$(GO) test -count=1 ./scripts/slowtests/
	$(MAKE) --no-print-directory precheck
	$(MAKE) --no-print-directory fuzz FUZZTIME=60s MUTATION_SCOPE="$(SEAT_PKGS)"
	$(MAKE) --no-print-directory cover
	$(if $(wildcard ci/seat-review.sh),GO=$(GO) sh ci/seat-review.sh "$(SEAT_BASE)")

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o errata ./cmd/errata

# ==> tool recipes begin. ci/image-tag.sh hashes every line from here to the end marker, with
# the version, pin and signer lines above, into the CI image's tag and the nodes' /cache/tools:
# a flag or URL here that binds a check, such as ko's --repository, is a new image when it
# changes. Keep every line of the tools' recipes inside.
PLATFORM := $(shell uname -s | tr A-Z a-z)_$(shell uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
# The platforms whose released binaries are installed. Every tool releases for both; elsewhere
# the Go tools are built with go install, and jq has a case of its own.
RELEASED := $(filter linux_amd64 linux_arm64,$(PLATFORM))
# GoReleaser and ko name their archives Linux_x86_64 and Linux_arm64.
GORELEASER_ASSET := $(subst linux_amd64,Linux_x86_64,$(subst linux_arm64,Linux_arm64,$(PLATFORM)))
GH    := https://github.com
FETCH := COSIGN=$(BIN)/cosign JQ=$(BIN)/jq scripts/fetch-tool.sh

# The checker of cosign's SHA-256: sha256sum, or on a system without it, such as macOS
# without coreutils, shasum -a 256, which reads the same lines with -c.
SHA256SUM := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo shasum -a 256)

tools: $(BIN)/golangci-lint $(BIN)/gremlins $(BIN)/govulncheck $(BIN)/goreleaser $(BIN)/syft $(BIN)/ko $(BIN)/jq

# cosign, against its hand pin. It is fetched where a tool's release is, since only those are
# verified with it. curl tries again on a 408, 429, 500, 502, 503, 504, 522 or 524, as
# scripts/fetch-tool.sh does: the download
# answered 504 to four ci-image:build jobs on 2026-11-08 and 2026-11-09.
$(BIN)/cosign:
ifneq ($(COSIGN_SHA256_$(PLATFORM)),)
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	curl -sSfL --retry 5 --retry-max-time 120 -o "$$tmp/b" "$(GH)/sigstore/cosign/releases/download/$(COSIGN_VERSION)/cosign-$(subst _,-,$(PLATFORM))" && \
	{ echo "$(COSIGN_SHA256_$(PLATFORM))  $$tmp/b" | $(SHA256SUM) -c --quiet - || \
	  { echo "checksum mismatch for cosign $(COSIGN_VERSION). After a version bump, run make tool-sums." >&2; exit 1; }; } && \
	mkdir -p "$(BIN)" && install -m 0755 "$$tmp/b" "$@" && \
	echo "installed $@, checked against its pin"
else
	GOBIN=$(BIN) $(GO) install github.com/sigstore/cosign/v3/cmd/cosign@$(COSIGN_VERSION)
endif

# golangci-lint: its provenance is in GitHub's attestation API, read with jq. Installs reach
# api.github.com, which answers 60 calls an hour without a token, and refuse on anything but 200.
$(BIN)/golangci-lint: | $(if $(RELEASED),$(BIN)/cosign $(BIN)/jq)
ifneq ($(RELEASED),)
	@$(FETCH) --sums $(GH)/golangci/golangci-lint/releases/download/$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION:v%=%)-checksums.txt \
	  --attestations golangci/golangci-lint --type https://slsa.dev/provenance/v1 --identity '$(GOLANGCI_LINT_SIGNER)' \
	  --member golangci-lint-$(GOLANGCI_LINT_VERSION:v%=%)-$(subst _,-,$(PLATFORM))/golangci-lint \
	  $(GH)/golangci/golangci-lint/releases/download/$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION:v%=%)-$(subst _,-,$(PLATFORM)).tar.gz $@
else
	GOBIN=$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
endif
# gremlins: its release signs nothing, so the checksums file is the check.
$(BIN)/gremlins:
ifneq ($(RELEASED),)
	@$(FETCH) --unsigned --sums $(GH)/go-gremlins/gremlins/releases/download/$(GREMLINS_VERSION)/checksums.txt \
	  --member gremlins $(GH)/go-gremlins/gremlins/releases/download/$(GREMLINS_VERSION)/gremlins_$(GREMLINS_VERSION:v%=%)_$(PLATFORM).tar.gz $@
else
	GOBIN=$(BIN) $(GO) install github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)
endif
$(BIN)/govulncheck:
	GOBIN=$(BIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
$(BIN)/goreleaser: | $(if $(RELEASED),$(BIN)/cosign)
ifneq ($(RELEASED),)
	@$(FETCH) --sums $(GH)/goreleaser/goreleaser/releases/download/$(GORELEASER_VERSION)/checksums.txt \
	  --signed-sums $(GH)/goreleaser/goreleaser/releases/download/$(GORELEASER_VERSION)/checksums.txt.sigstore.json \
	  --identity '$(GORELEASER_SIGNER)' --member goreleaser \
	  $(GH)/goreleaser/goreleaser/releases/download/$(GORELEASER_VERSION)/goreleaser_$(GORELEASER_ASSET).tar.gz $@
else
	GOBIN=$(BIN) $(GO) install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
endif
# syft writes the SPDX SBOM GoReleaser attaches to each release archive.
$(BIN)/syft: | $(if $(RELEASED),$(BIN)/cosign)
ifneq ($(RELEASED),)
	@$(FETCH) --sums $(GH)/anchore/syft/releases/download/$(SYFT_VERSION)/syft_$(SYFT_VERSION:v%=%)_checksums.txt \
	  --signed-sums $(GH)/anchore/syft/releases/download/$(SYFT_VERSION)/syft_$(SYFT_VERSION:v%=%)_checksums.txt.sigstore.json \
	  --identity '$(SYFT_SIGNER)' --member syft \
	  $(GH)/anchore/syft/releases/download/$(SYFT_VERSION)/syft_$(SYFT_VERSION:v%=%)_$(PLATFORM).tar.gz $@
else
	GOBIN=$(BIN) $(GO) install github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)
endif
# ko builds the release image; make image-dry runs it the way GoReleaser does.
$(BIN)/ko: | $(if $(RELEASED),$(BIN)/cosign)
ifneq ($(RELEASED),)
	@$(FETCH) --sums $(GH)/ko-build/ko/releases/download/$(KO_VERSION)/checksums.txt \
	  --provenance $(GH)/ko-build/ko/releases/download/$(KO_VERSION)/multiple.intoto.jsonl --type slsaprovenance02 \
	  --identity-regexp '$(KO_SIGNER)' --repository ko-build/ko --ref refs/tags/$(KO_VERSION) --member ko \
	  $(GH)/ko-build/ko/releases/download/$(KO_VERSION)/ko_$(KO_VERSION:v%=%)_$(GORELEASER_ASSET).tar.gz $@
else
	GOBIN=$(BIN) $(GO) install github.com/google/ko@$(KO_VERSION)
endif
# jq reads renovate.json in ci/tests/tool-sums.sh as Renovate parses it, JSON escapes included.
# jq publishes binaries for Linux and macOS (named jq-macos-*), each checked against its
# release's sha256sum.txt and its signed provenance. jq is not written in Go, so elsewhere it
# is the system's own, and only at the pinned version.
JQ_ASSET := $(filter jq-linux-amd64 jq-linux-arm64 jq-macos-amd64 jq-macos-arm64,jq-$(subst darwin,macos,$(subst _,-,$(PLATFORM))))
$(BIN)/jq: | $(if $(JQ_ASSET),$(BIN)/cosign)
ifneq ($(JQ_ASSET),)
	@$(FETCH) --sums $(GH)/jqlang/jq/releases/download/jq-$(JQ_VERSION)/sha256sum.txt \
	  --provenance $(GH)/jqlang/jq/releases/download/jq-$(JQ_VERSION)/jq-attestation.json --type https://slsa.dev/provenance/v1 \
	  --identity '$(JQ_SIGNER)' $(GH)/jqlang/jq/releases/download/jq-$(JQ_VERSION)/$(JQ_ASSET) $@
else
	@j=$$(command -v jq) || { echo "no jq release for $(PLATFORM): install jq $(JQ_VERSION)" >&2; exit 1; }; \
	[ "$$("$$j" --version)" = "jq-$(JQ_VERSION)" ] || { echo "$$j is $$("$$j" --version), not jq-$(JQ_VERSION): install jq $(JQ_VERSION)" >&2; exit 1; }; \
	mkdir -p "$(BIN)" && ln -sf "$$j" "$@"
endif
# <== tool recipes end.

# Lint runs once for each GOOS the release builds. A file named _windows.go
# or _darwin.go is outside a linux build, so linux alone would not see what
# it imports, and depguard's allow list for internal/rule would not hold.
# The same holds for the mutation build tag: internal/heapcap/mutation.go and
# every mutation_test.go compile only under it, so linux runs once more with it.
lint: $(BIN)/golangci-lint
	$(GO) mod tidy -diff
	$(BIN)/golangci-lint run ./...
	$(BIN)/golangci-lint run --build-tags mutation ./...
	GOOS=windows $(BIN)/golangci-lint run ./...
	GOOS=darwin $(BIN)/golangci-lint run ./...

fmt: $(BIN)/golangci-lint
	$(BIN)/golangci-lint fmt ./...

# A gate's scope (#284). ci/gate.sh runs make gate GATE_BASE=<commit>, and ci/mutation-scope.sh
# --gate then reads the change since that commit and decides which packages test and slow run,
# which packages' functions cover scores, and how long each fuzz target runs. Its section "The
# gate's scope" says how, and why the tests it leaves out cannot reach the change. Without
# GATE_BASE every package runs, as in the push pipeline, the weekly pass and a run by hand. Each
# step prints a line that starts "==> reach", which report.txt quotes. Only a command-line value
# counts, as for MUTATION_SCOPE below, and so for FUZZTIME_NEAR and FUZZTIME_FAR; the SCOPE_
# values are overrides, so that nothing but the scope script narrows them. The pod needs the
# history for the diff (ERRATA_CI_HISTORY=1).
ifeq ($(origin GATE_BASE),environment)
override GATE_BASE :=
endif
ifeq ($(origin FUZZTIME_NEAR),environment)
override FUZZTIME_NEAR := 60s
endif
ifeq ($(origin FUZZTIME_FAR),environment)
override FUZZTIME_FAR := 5s
endif
FUZZTIME_NEAR ?= 60s
FUZZTIME_FAR  ?= 5s
override SCOPE_WHY          := every package, since no GATE_BASE was given
override SCOPE_TEST         := all
override SCOPE_COVER        := all
override SCOPE_FUZZ_NEAR    :=
override SCOPE_FUZZ_FAR     := all
override SCOPE_FUZZTIME_FAR := $(FUZZTIME)
ifneq ($(GATE_BASE),)
GATE_SCOPE    := .cache/gate-scope.mk
GATE_SCOPE_RC := $(shell mkdir -p .cache && MUTATION_SCOPE_BASE='$(GATE_BASE)' sh ci/mutation-scope.sh --gate >$(GATE_SCOPE) 2>.cache/gate-scope.log; echo $$?)
ifneq ($(GATE_SCOPE_RC),0)
$(error the gate's scope from GATE_BASE=$(GATE_BASE) failed, exit $(GATE_SCOPE_RC): $(shell cat .cache/gate-scope.log))
endif
include $(GATE_SCOPE)
endif
# A landing gate scores cover over every package (owner 2026-11-10). GATE_COVER=all, on the
# command line, makes cover score every function and judge the 90% total, and so makes test run
# every package's tests, since only that run gives every function exact coverage. slow and fuzz
# keep the change's scope. ci/gate.sh passes it on a gate without --seat; a seat's gate stays
# scoped.
ifneq ($(origin GATE_COVER),command line)
override GATE_COVER :=
endif
ifeq ($(GATE_COVER),all)
override SCOPE_COVER := all
endif
# TEST_RUN: the packages make test runs, every one when cover scores every one.
TEST_RUN = $(if $(filter all,$(SCOPE_COVER)),all,$(SCOPE_TEST))
COVER_ALL_WHY := every package, since GATE_COVER=all scores cover over every one
TEST_WHY = $(if $(GATE_COVER),$(COVER_ALL_WHY),$(SCOPE_WHY))
# in_scope: ./... for all, or each package directory with ./ before it.
in_scope = $(if $(filter all,$(1)),./...,$(addprefix ./,$(1)))
# reach_of: for a step's ==> reach line, every package, or how many and which.
reach_of = $(if $(filter all,$(1)),every package,$(words $(1)) packages: $(1))

# Unit, property and API (testscript) tests. -timeout bounds every run, so a
# hung check fails instead of stalling CI.
# One run gives the verdicts and the coverage profile that `cover` reads.
# rapid runs 100 cases per property by default; CI can raise it with
# RAPID_CHECKS=1000 make test.
# -coverpkg instruments internal/, the product code the total of `cover`
# judges, and scripts/, whose tools `cover` holds to CRAP below 10 as well.
# cmd/errata has no tests of its own and is left out.
# On a gate, only the packages whose tests reach the change run (above).
test:
	@echo '==> reach test: $(call reach_of,$(TEST_RUN)); $(TEST_WHY)'
	$(GO) test -race -shuffle=on -timeout 2m -count=1 -coverprofile=coverage.out -coverpkg=./internal/...,./scripts/... $(call in_scope,$(TEST_RUN))

# Tests too slow for the race detector or for a rerun per mutant: each calls
# slowOnly, which skips under -race, -short and the mutation tag (except
# internal/minimatch's, which keeps running under mutation since its slow
# tests hold cases no other test holds, expansionMaxDepth among them), and
# runs here once, plain. SLOW_TESTS comes from the tree (#214): scripts/slowtests
# lists every test whose body calls slowOnly, and fails, listing none, on a
# call it would miss, so a new slow test needs no edit here. go test passes
# when -run matches nothing, so each must report its own PASS line, and an
# empty list is refused rather than pass with no test run. A test that checks
# less on this machine than it can elsewhere logs a line with "narrowed: ",
# and the target shows it. On a gate, only the slow tests of the packages make test runs do.
SLOW_TESTS = $(shell $(GO) run ./scripts/slowtests .)
slow:
	@list='$(strip $(SLOW_TESTS))'; \
	[ -n "$$list" ] || { echo "make slow: REFUSED: SLOW_TESTS is empty, so no slow test would run" >&2; exit 2; }; \
	ran=0; for t in $$list; do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		case " $(SCOPE_TEST) " in *" all "*|*" $${pkg#./} "*) ran=$$((ran + 1)) ;; *) continue ;; esac; \
		out=$$($(GO) test -count=1 -v -run "^$$name$$" $$pkg 2>&1); status=$$?; \
		echo "$$out" | grep -E "^(--- |ok|FAIL)|_test\.go:[0-9]+: narrowed: "; \
		[ $$status -eq 0 ] || { echo "$$out"; exit 1; }; \
		echo "$$out" | grep -q "^--- PASS: $$name " || \
		  { echo "make slow: $$name did not pass in $$pkg; it is skipped there, or SLOW_TESTS names a test that does not exist" >&2; exit 1; }; \
	done; \
	echo "==> reach slow: $$ran of $$(echo $$list | wc -w) slow tests, those of $(call reach_of,$(SCOPE_TEST))"

# The total is of the product code, internal/: it leaves out scripts/, the
# tools of the build, and the packages named like httptest, extracttest and
# coretest: only tests import them, and their failure branches run only
# when a test fails. Gremlins leaves them out too (.gremlins.yaml).
# Then scripts/crap scores each function of internal/ and scripts/ for CRAP
# from the same profile and holds the scores to scripts/crap-baseline.txt:
# below 10, or no higher than the function's row (CODING_STANDARDS.md). It
# leaves out the helper packages as the total does, and -require refuses a
# package of either directory that the run did not instrument.
#
# On a gate whose tests are scoped, the total is not judged, since the packages whose tests did
# not run would count as uncovered; the push pipeline judges it. scripts/crap then scores only
# the packages whose coverage the scoped run has exactly (-only), and every other function keeps
# its row (ci/mutation-scope.sh, "The gate's scope").
CRAP_SCOPE = $(if $(filter all,$(SCOPE_COVER)),-require internal -require scripts,$(addprefix -only ,$(SCOPE_COVER)))
cover: test
	@grep -vE '^[^:]*test/[^/:]*\.go:|^[^:]*/errata/scripts/' coverage.out > coverage.product.out; \
	total=$$($(GO) tool cover -func=coverage.product.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	if [ '$(TEST_RUN)' = all ]; then \
		echo "coverage $$total% (minimum $(COVERAGE_MIN)%)"; \
		awk -v t=$$total -v m=$(COVERAGE_MIN) 'BEGIN { exit !(t >= m) }' || exit 1; \
	else \
		echo "==> reach cover: the total, $$total% of the packages the scoped tests linked, is not judged; the push pipeline judges it"; \
	fi; \
	echo '==> reach cover: CRAP of $(call reach_of,$(SCOPE_COVER))'; \
	$(if $(strip $(SCOPE_COVER)),$(GO) run ./scripts/crap $(CRAP_SCOPE) -baseline scripts/crap-baseline.txt coverage.out,echo "==> reach cover: no package has exact coverage in this run; the push pipeline scores CRAP")

# The packages fuzz and mutation cover: all, none, or package directories such as
# "internal/config internal/cli". CI's test job sets it from ci/mutation-scope.sh and passes
# FUZZ_FLAGS=-parallel=1 (one fuzz worker, measured at 43% less CPU for the same wall clock).
# Left as they are, both steps cover every package, as locally and weekly. MUTATION_DIFF, a
# commit, narrows mutation to the lines each package in scope changed since it
# (scripts/mutation.sh --diff); CI's mutation job sets it to the base of the scope.
# MUTATION_DEADLINE, in seconds since the epoch, is when mutation must have ended; a package
# that would not end by then is left out and named (scripts/mutation.sh --deadline). CI's
# mutation job sets it from the job's start and timeout. MUTATION_JOBS is how many packages
# run at once (scripts/mutation.sh --jobs); CI's mutation job sets it from its pod's CPUs and
# memory. MUTATION_MOST caps a diff run's packages (--most); CI's mutation job sets
# it, and the packages over it are left out and named. MUTATION_MEMORY_REQUEST is the memory
# guard's request (--memory-request; ci/README.md, The weekly pass). MUTATION_NARROW=off runs
# every mutant against its package's whole tests (--narrow off), and MUTATION_SHOW_KILLED=yes
# names each killed mutant in the log (--show-killed yes), so two runs compare mutant by mutant
# (ci/README.md, Narrowing). MUTATION_REDUNDANT=yes lists, after each pass, the tests that add no
# block and no kill (--redundant yes; ci/README.md, Narrowing).
#
# Only a value given on make's command line counts. make exports command-line variables
# to every recipe, so `make check MUTATION_SCOPE=x` reaches each make a recipe starts, the
# tests' own included, as an environment variable; and a shell that exported
# MUTATION_SCOPE=none would make `make check` mutate and fuzz nothing, and pass. A value
# from the environment is therefore dropped.
ifeq ($(origin MUTATION_SCOPE),environment)
override MUTATION_SCOPE := all
endif
ifeq ($(origin FUZZ_FLAGS),environment)
override FUZZ_FLAGS :=
endif
ifeq ($(origin MUTATION_DIFF),environment)
override MUTATION_DIFF :=
endif
ifeq ($(origin MUTATION_DEADLINE),environment)
override MUTATION_DEADLINE :=
endif
ifeq ($(origin MUTATION_JOBS),environment)
override MUTATION_JOBS :=
endif
ifeq ($(origin MUTATION_MOST),environment)
override MUTATION_MOST :=
endif
ifeq ($(origin MUTATION_MEMORY_REQUEST),environment)
override MUTATION_MEMORY_REQUEST :=
endif
ifeq ($(origin MUTATION_MEASURE_LEAST),environment)
override MUTATION_MEASURE_LEAST :=
endif
ifeq ($(origin MUTATION_PER_PACKAGE),environment)
override MUTATION_PER_PACKAGE :=
endif
ifeq ($(origin MUTATION_NARROW),environment)
override MUTATION_NARROW :=
endif
ifeq ($(origin MUTATION_SHOW_KILLED),environment)
override MUTATION_SHOW_KILLED :=
endif
ifeq ($(origin MUTATION_REDUNDANT),environment)
override MUTATION_REDUNDANT :=
endif
MUTATION_SCOPE    ?= all
FUZZ_FLAGS        ?=
MUTATION_DIFF     ?=
MUTATION_DEADLINE ?=
MUTATION_JOBS     ?=
MUTATION_MOST     ?=
MUTATION_MEMORY_REQUEST ?=
MUTATION_NARROW   ?=
MUTATION_SHOW_KILLED ?=
MUTATION_REDUNDANT ?=

# An empty FUZZ_TARGETS, from a derivation that broke or set on the command line, is refused: it
# would pass with no target run. A package in FUZZ_SKIP is left out, as fuzz-rest leaves out what
# fuzz-first fuzzed. On a gate, a target of a changed package runs for FUZZTIME_NEAR, one of a
# package whose tests reach a changed one for FUZZTIME_FAR, and any other not at all
# (ci/mutation-scope.sh, "The gate's scope"). Without a gate's scope every target in
# MUTATION_SCOPE runs for FUZZTIME. scripts/fuzz-input.sh prints the input of a target that fails,
# base64 encoded, since the tree go writes it to is thrown away in a runner's pod and in CI.
fuzz:
	@[ -n "$(strip $(FUZZ_TARGETS))" ] || { echo "make fuzz: REFUSED: FUZZ_TARGETS is empty, so no fuzz target would run" >&2; exit 2; }
	@ran=0; near=0; skip=0; for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		case " $(MUTATION_SCOPE) " in *" all "*|*" $${pkg#./} "*) ;; *) continue ;; esac; \
		case " $(FUZZ_SKIP) " in *" $${pkg#./} "*) continue ;; esac; \
		case " $(SCOPE_FUZZ_NEAR) " in *" $${pkg#./} "*) ft=$(FUZZTIME_NEAR); near=$$((near + 1)) ;; \
		*) case " $(SCOPE_FUZZ_FAR) " in *" all "*|*" $${pkg#./} "*) ft=$(SCOPE_FUZZTIME_FAR) ;; *) skip=$$((skip + 1)); continue ;; esac ;; esac; \
		echo "fuzz $$name for $$ft"; ran=$$((ran + 1)); \
		sh scripts/fuzz-input.sh run $$pkg $$name $(GO) test -run '^$$' -fuzz "^$$name$$" -fuzztime $$ft -fuzzminimizetime $(FUZZMINIMIZETIME) $(FUZZ_FLAGS) $$pkg || exit 1; \
	done; echo "fuzz: $$ran targets in scope ($(MUTATION_SCOPE))"; \
	echo "==> reach fuzz: $$near targets of changed packages for $(FUZZTIME_NEAR), $$((ran - near)) for $(SCOPE_FUZZTIME_FAR), $$skip that cannot reach the change not run"

# The gate's two fuzz passes (#279). FUZZ_FIRST, package directories, is what fuzz-first fuzzes
# before test, and fuzz-rest fuzzes the rest of MUTATION_SCOPE at the end, so each target runs
# once. Without a FUZZ_FIRST on the command line it is MUTATION_SCOPE, so CI's test job, which
# scopes fuzz to the push, fuzzes it all before test; all and none put nothing first. On a gate's
# scope (GATE_BASE) it is the changed packages, SCOPE_FUZZ_NEAR, whose targets run for
# FUZZTIME_NEAR. Only command-line values of FUZZ_FIRST and FUZZ_SKIP count, as for
# MUTATION_SCOPE.
ifneq ($(origin FUZZ_FIRST),command line)
override FUZZ_FIRST := $(if $(GATE_BASE),$(SCOPE_FUZZ_NEAR),$(MUTATION_SCOPE))
endif
ifeq ($(origin FUZZ_SKIP),environment)
override FUZZ_SKIP :=
endif
FUZZ_FIRST_SCOPE := $(call canon_dirs,$(filter-out all none,$(FUZZ_FIRST)))
fuzz-first:
	$(MAKE) --no-print-directory fuzz MUTATION_SCOPE="$(or $(FUZZ_FIRST_SCOPE),none)" FUZZ_SKIP=
fuzz-rest:
	$(MAKE) --no-print-directory fuzz FUZZ_SKIP="$(FUZZ_FIRST_SCOPE)"

# scripts/mutation.sh runs gremlins from a copy of the tracked tree, one pass per package of
# MUTATION_SCOPE, each held to its own floors; it says why.
mutation: $(BIN)/gremlins
	GREMLINS=$(BIN)/gremlins scripts/mutation.sh $(if $(MUTATION_DIFF),--diff $(MUTATION_DIFF) )$(if $(MUTATION_DEADLINE),--deadline $(MUTATION_DEADLINE) )$(if $(MUTATION_JOBS),--jobs $(MUTATION_JOBS) )$(if $(MUTATION_MOST),--most $(MUTATION_MOST) )$(if $(MUTATION_MEMORY_REQUEST),--memory-request $(MUTATION_MEMORY_REQUEST) )$(if $(MUTATION_NARROW),--narrow $(MUTATION_NARROW) )$(if $(MUTATION_SHOW_KILLED),--show-killed $(MUTATION_SHOW_KILLED) )$(if $(MUTATION_REDUNDANT),--redundant $(MUTATION_REDUNDANT) )$(MUTATION_SCOPE)

vuln: $(BIN)/govulncheck
	$(BIN)/govulncheck ./...

# errata checks its own repository.
dogfood: build
	./errata check

# The tree a publish would send, from the working files (committed or not),
# written to ./public. It fails on a forbidden string, and then errata's own
# checks run over the result, which catches a public file linking into a
# stripped path. Nothing leaves this machine.
public-dry: build
	@idx=$$(mktemp -d)/index; \
	tree=$$(GIT_INDEX_FILE=$$idx sh -c 'git read-tree HEAD && git add -A && git write-tree'); \
	rm -rf "$$(dirname $$idx)"; \
	ci/public-tree.sh $$tree public && ./errata check --root public

# The publish and autopatch scripts, against local repositories only. Gremlins too: one test
# runs it on a module of its own, to show what a diff run mutates. jq reads renovate.json,
# and cosign checks the signatures in the tests of scripts/fetch-tool.sh.
# CI_TEST_SELECT=changed, which only CI's ci-test job passes, runs only the files of ci/tests a
# path changed since the pipeline's base can affect (ci/README.md, Choosing the suites). Only a
# command-line value counts, as for CI_TEST: an exported one would make a seat's gate skip
# files. Any value but changed stops make.
ifeq ($(origin CI_TEST_SELECT),environment)
override CI_TEST_SELECT :=
endif
ifneq ($(filter-out changed,$(CI_TEST_SELECT)),)
$(error CI_TEST_SELECT is '$(CI_TEST_SELECT)': it takes changed, or nothing for every suite)
endif
ci-test:$(BIN)/gremlins $(BIN)/jq $(BIN)/cosign
	cd ci/autopatch && $(GO) vet ./... && $(GO) test -count=1 ./...
	$(if $(GATE_BASE),CI_TEST_BASE=$(GATE_BASE) )GREMLINS=$(BIN)/gremlins JQ=$(BIN)/jq COSIGN=$(BIN)/cosign ci/test-ci.sh$(if $(CI_TEST_SELECT), --changed)

# The release image, built by ko with .goreleaser.yaml's base image and user
# into a tarball, pushed nowhere, and its config checked against what the
# READMEs promise. `release-dry` skips ko, so before this nothing tested the
# image until a tag: that is how v0.1.0 shipped a kos setting ko rejects.
IMAGE_DRY := $(CURDIR)/dist/image-dry
image-dry: $(BIN)/ko
	@rm -rf $(IMAGE_DRY) && mkdir -p $(IMAGE_DRY)
	KO_DOCKER_REPO=registry.invalid/errata \
	KO_DEFAULTBASEIMAGE="$$(sed -n 's/^ *base_image: *//p' .goreleaser.yaml)" \
	CGO_ENABLED=0 GOFLAGS=-trimpath $(BIN)/ko build ./cmd/errata --bare --push=false \
	  --platform=linux/amd64 --image-user="$$(sed -n 's/^ *user: *"\{0,1\}\([0-9]*\)"\{0,1\}$$/\1/p' .goreleaser.yaml)" \
	  --tarball=$(IMAGE_DRY)/image.tar
	@cd $(IMAGE_DRY) && tar -xf image.tar && cfg=$$(sed -n 's/.*"Config":"\([^"]*\)".*/\1/p' manifest.json) && \
	for want in '"Entrypoint":\["/ko-app/errata"\]' '"User":"0"' 'PATH=[^"]*/ko-app'; do \
	  grep -q "$$want" "$$cfg" || { echo "image config lacks $$want" >&2; exit 1; }; \
	done && echo "image-dry: entrypoint, user and PATH as documented"

# CI's gate with CI's directory layout: Go's module, build and lint caches
# inside the project, as the shared runner cache needs them. Two defects that
# only CI saw came from that layout (a cache directory named like a source
# file, and checks reading dependency sources), so run this before pushing a
# change to the caches or the directory layout (the .go job's variables, this
# target, GOFLAGS).
# TMPDIR is on disk and outside the project, as in CI: the gate's tests and
# builds write their temporary files there, and `make mutation` run the same
# way copies the tracked tree into it once per worker, which on a tmpfs /tmp
# would sit in memory.
# The in-tree module cache is filled first from the one go would use outside this target, the
# node's /cache in a runner's pod, so the gate does not download every module again, and then
# verified, as CI's .go job verifies the cache it restores. The build and lint caches start cold
# (ci/README.md, The gate in CI).
CI_LOCAL_TMP ?= $(HOME)/.cache/errata-ci-local
ci-local:
	@mkdir -p $(CI_LOCAL_TMP)
	GO=$(GO) scripts/seed-modcache.sh "$$($(GO) env GOMODCACHE)" $(CURDIR)/.gopath/pkg/mod
	GOPATH=$(CURDIR)/.gopath GOFLAGS=-modcacherw $(GO) mod verify
	TMPDIR=$(CI_LOCAL_TMP) GOPATH=$(CURDIR)/.gopath GOCACHE=$(CURDIR)/.cache/go-build \
	GOLANGCI_LINT_CACHE=$(CURDIR)/.cache/golangci-lint GOFLAGS=-modcacherw \
	$(MAKE) gate FUZZTIME=5s

# Every release binary for every platform, into ./dist. No token, no upload.
# The image is skipped: ko writes a snapshot image to a local Docker daemon.
release-dry: $(BIN)/goreleaser
	$(BIN)/goreleaser check
	$(BIN)/goreleaser release --snapshot --clean --skip=publish,ko

# docs/cli.md, the command line reference, from the help errata prints. A release runs
# cli-doc in the commit it tags (ci/README.md, Setting it up); the tag pipeline's
# check:cli-doc job runs cli-doc-check, which refuses a docs/cli.md that differs from the help.
cli-doc:
	$(GO) run ./scripts/clidoc docs/cli.md

cli-doc-check:
	$(GO) run ./scripts/clidoc --check docs/cli.md

# How many lines of the tree are code, by bucket (source, tests, tooling, examples, docs,
# generated) and language: files, raw lines, code lines, and code with Python's docstrings.
# scripts/loccensus says how it sorts and counts. It is for a reader; nothing in CI runs it.
loc:
	$(GO) run ./scripts/loccensus .

# The commit-msg hook: commit subjects are published on the mirror, so they
# are checked before the commit exists. CI runs the same check on every push.
hooks:
	git config core.hooksPath .githooks
	@echo "commit messages are now checked by ci/check-commit-msg.sh"

# Rewrite cosign's pinned sums for COSIGN_VERSION, from its checksums file, checked with the
# cosign already in BIN (scripts/tool-sums.sh says why that one).
tool-sums: | $(BIN)/cosign
	COSIGN=$(BIN)/cosign scripts/tool-sums.sh

clean:
	rm -rf errata bin coverage.out coverage.product.out public dist

# One entry point for a developer and for CI. `make check` is the gate.
# Tools install into ./bin at pinned versions and never enter errata's
# dependency graph. On Linux they are the projects' released binaries, checked
# against the SHA-256 written below; elsewhere they are built with go install.
# CI sets BIN to the CI image's tool directory, where they are already there.

GO        ?= go
BIN       ?= $(CURDIR)/bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X github.com/f-klement/errata/internal/cli.Version=$(VERSION)

GOLANGCI_LINT_VERSION := v2.15.0
GREMLINS_VERSION      := v0.6.0
GOVULNCHECK_VERSION   := v1.9.0
GORELEASER_VERSION    := v2.19.0
SYFT_VERSION          := v1.55.0
KO_VERSION            := v0.19.1

# SHA-256 of each released archive, from the project's own checksum file at
# that version. After a version bump, `make tool-sums` rewrites them; until
# then the download fails its check. Renovate runs it on its own bumps
# (renovate.json). govulncheck publishes no binaries and is always built.
GOLANGCI_LINT_SHA256_linux_amd64 := 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
GOLANGCI_LINT_SHA256_linux_arm64 := b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998
GREMLINS_SHA256_linux_amd64      := b02a42e47935f891c9a411d68c07e211c7082609e79c2435b67c85ee9658c538
GREMLINS_SHA256_linux_arm64      := 686542674e54559afb7e86d6994d342c19a856cfd970c315748989537936664c
GORELEASER_SHA256_linux_amd64    := ebc3194ad1d1b1787614405285a0039665cd4c5bd3305215a1b201600f90630b
GORELEASER_SHA256_linux_arm64    := ef33741b684c9f62d6a21e7a3c8b051f139ea7b1d301693b67cc819c46226d53
SYFT_SHA256_linux_amd64          := bb65249542db5fedeeb4d3f9b018b75e7264dbfbbb10486f044581a92f90490d
SYFT_SHA256_linux_arm64          := 929bd163db2d7cd2255d3bcb8967208079813d7b46b0c952f7b794d8e2c7d304
KO_SHA256_linux_amd64            := 635ac6ea3fd376c935fee597fbb29ab2c2449f49ef1655085fe3aa9c25fed7a5
KO_SHA256_linux_arm64            := 4099b2d1170d3b8a70e049237462efc2dd14d5fa30e9d2e5e108fb4f778cdd3f

PLATFORM := $(shell uname -s | tr A-Z a-z)_$(shell uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
# GoReleaser names its archives Linux_x86_64 and Linux_arm64.
GORELEASER_ASSET := $(subst linux_amd64,Linux_x86_64,$(subst linux_arm64,Linux_arm64,$(PLATFORM)))

# fetch <url> <sha256> <path inside the archive> <destination>
define fetch
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	curl -sSfL -o "$$tmp/a.tar.gz" "$(1)" && \
	{ echo "$(2)  $$tmp/a.tar.gz" | sha256sum -c --quiet - || \
	  { echo "checksum mismatch for $(1). After a version bump, run make tool-sums." >&2; exit 1; }; } && \
	tar -xzf "$$tmp/a.tar.gz" -C "$$tmp" "$(3)" && \
	mkdir -p "$(BIN)" && install -m 0755 "$$tmp/$(3)" "$(4)" && \
	echo "installed $(4)"
endef

COVERAGE_MIN := 90
FUZZTIME     ?= 15s
# How long Go minimizes each input that adds coverage, as a time or a count. A worker that
# minimizes does not fuzz, and the coordinator counts its execs only when it returns, so a
# pass reads 0/sec meanwhile. Go's default of 60 s let one minimization take most of a 10 s
# pass at one worker (measured 2026-10-22: 3 to 34 thousand execs, against 42 to 49 thousand
# at 100x). The nightly campaigns pass 60s, where the time for a smaller corpus is there.
FUZZMINIMIZETIME ?= 100x
# Each fuzz target as package:Name. `go test -fuzz` takes one target per run.
FUZZ_TARGETS := ./internal/config:FuzzParse ./internal/protocol:FuzzMessage \
                ./internal/report:FuzzJUnit ./internal/rule:FuzzAssert ./internal/report:FuzzSARIF \
                ./internal/extract/core:FuzzPath ./internal/extract/yaml:FuzzYAML \
                ./internal/extract/json:FuzzJSON ./internal/extract/toml:FuzzTOML \
                ./internal/extract/core:FuzzRegex ./internal/extract/core:FuzzExtract \
                ./internal/extract/core:FuzzParseIndex \
                ./internal/extract/text:FuzzText ./internal/extract/core:FuzzNumber \
                ./internal/extract/xml:FuzzXPath ./internal/extract/xml:FuzzXML \
                ./internal/extract/ini:FuzzINI ./internal/extract/properties:FuzzProperties \
                ./internal/extract/toolversion:FuzzToolVersion ./internal/semver:FuzzParse \
                ./internal/extract/packageversion:FuzzPackageVersion \
                ./internal/extract/envfile:FuzzEnvfile \
                ./internal/extract/gitlabci:FuzzGitLabCI \
                ./internal/extract/shell:FuzzShell \
                ./internal/extract/githubactions:FuzzGitHubActions \
                ./internal/extract/jenkins:FuzzJenkins

.PHONY: check gate precheck build tools lint fmt test cover slow fuzz mutation vuln dogfood public-dry release-dry image-dry ci-local ci-test tool-sums hooks clean

# ci/ is stripped from the public mirror, so a public clone runs the gate
# without the publish and autopatch targets.
INTERNAL := $(if $(wildcard ci/public-tree.sh),public-dry ci-test)

# `gate` is everything but mutation testing, for a quick run by hand: Gremlins
# over the whole module takes hours. CI's test job runs `gate` with fuzzing
# scoped to the packages a push changed (ci/mutation-scope.sh), its mutation
# job mutates the lines those packages changed (ci/push-mutation.sh), and the
# weekly pass runs every package whole.
gate: lint test cover slow fuzz vuln dogfood image-dry $(INTERNAL)

check: gate mutation

# The checks that sank whole gates for want of a minute's look, for a seat to run on its final
# commit before handing it back. In the two waves up to 2026-10-25 five gates of 12 to 15 minutes
# went red on one of them: three on lint or on a node name in a public file (public-dry), and two
# on a new package without its `//go:build mutation` mutation_test.go, which only
# TestEveryTestedPackageLinksTheWatchdog in make test catches. That test runs first, since it
# takes seconds where lint takes minutes. The public mirror has no ci/, so there it is the test
# and lint.
precheck:
	$(GO) test -count=1 -run '^TestEveryTestedPackageLinksTheWatchdog$$' ./internal/heapcap
	$(MAKE) --no-print-directory lint $(if $(wildcard ci/public-tree.sh),public-dry)

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o errata ./cmd/errata

tools: $(BIN)/golangci-lint $(BIN)/gremlins $(BIN)/govulncheck $(BIN)/goreleaser $(BIN)/syft $(BIN)/ko

$(BIN)/golangci-lint:
ifneq ($(GOLANGCI_LINT_SHA256_$(PLATFORM)),)
	$(call fetch,https://github.com/golangci/golangci-lint/releases/download/$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION:v%=%)-$(subst _,-,$(PLATFORM)).tar.gz,$(GOLANGCI_LINT_SHA256_$(PLATFORM)),golangci-lint-$(GOLANGCI_LINT_VERSION:v%=%)-$(subst _,-,$(PLATFORM))/golangci-lint,$@)
else
	GOBIN=$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
endif
$(BIN)/gremlins:
ifneq ($(GREMLINS_SHA256_$(PLATFORM)),)
	$(call fetch,https://github.com/go-gremlins/gremlins/releases/download/$(GREMLINS_VERSION)/gremlins_$(GREMLINS_VERSION:v%=%)_$(PLATFORM).tar.gz,$(GREMLINS_SHA256_$(PLATFORM)),gremlins,$@)
else
	GOBIN=$(BIN) $(GO) install github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)
endif
$(BIN)/govulncheck:
	GOBIN=$(BIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
$(BIN)/goreleaser:
ifneq ($(GORELEASER_SHA256_$(PLATFORM)),)
	$(call fetch,https://github.com/goreleaser/goreleaser/releases/download/$(GORELEASER_VERSION)/goreleaser_$(GORELEASER_ASSET).tar.gz,$(GORELEASER_SHA256_$(PLATFORM)),goreleaser,$@)
else
	GOBIN=$(BIN) $(GO) install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
endif
# syft writes the SPDX SBOM GoReleaser attaches to each release archive.
$(BIN)/syft:
ifneq ($(SYFT_SHA256_$(PLATFORM)),)
	$(call fetch,https://github.com/anchore/syft/releases/download/$(SYFT_VERSION)/syft_$(SYFT_VERSION:v%=%)_$(PLATFORM).tar.gz,$(SYFT_SHA256_$(PLATFORM)),syft,$@)
else
	GOBIN=$(BIN) $(GO) install github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)
endif
# ko builds the release image; make image-dry runs it the way GoReleaser does.
$(BIN)/ko:
ifneq ($(KO_SHA256_$(PLATFORM)),)
	$(call fetch,https://github.com/ko-build/ko/releases/download/$(KO_VERSION)/ko_$(KO_VERSION:v%=%)_$(GORELEASER_ASSET).tar.gz,$(KO_SHA256_$(PLATFORM)),ko,$@)
else
	GOBIN=$(BIN) $(GO) install github.com/google/ko@$(KO_VERSION)
endif

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

# Unit, property and API (testscript) tests. -timeout bounds every run, so a
# hung check fails instead of stalling CI.
# One run gives the verdicts and the coverage profile that `cover` reads.
# rapid runs 100 cases per property by default; CI can raise it with
# RAPID_CHECKS=1000 make test.
test:
	$(GO) test -race -shuffle=on -timeout 2m -count=1 -coverprofile=coverage.out -coverpkg=./internal/... ./...

# Tests too slow for the race detector or for a rerun per mutant: each calls
# slowOnly, which skips under -race, -short and the mutation tag, and runs
# here once, plain. go test passes when -run matches nothing, so each must
# report its own PASS line: a renamed test, or a name misspelt here, fails
# instead of running nowhere. A test that calls slowOnly and is missing here
# fails TestEverySlowTestIsInMakeSlow in make test. A test that checks less
# on this machine than it can elsewhere logs a line with "narrowed: ", and
# make slow shows it.
SLOW_TESTS := ./internal/report:TestSARIFBoundsTheReviewersUpload \
              ./internal/extract/core:TestFilterLimitsAtTheCheapestCost \
              ./internal/extract/toml:TestReplayMeasuredInputsAtFullSize \
              ./internal/extract/yaml:TestReplayMeasuredInputsAtFullSize \
              ./internal/extract/text:TestMarkdownBoundsOnLargeInputs \
              ./internal/extract/yaml:TestExcusedYAMLIsLinear \
              ./internal/extract/core:TestMessagesQuoteAShortPartOfALargeFile \
              ./internal/extract/core:TestFilterReadsALargeNumberOnce \
              ./internal/extract/xml:TestXMLValueLimitAtItsSize \
              ./internal/extract/xml:TestXMLComparisonsStopAtTheFilterLimitAtItsSize \
              ./internal/extract/xml:TestXMLDeclarationsLookAtALargeFileOnce \
              ./internal/extract/ini:TestGitAgreesWithGitConfig \
              ./internal/extract/ini:TestGitConfigReadsNoRepositoryAround \
              ./internal/extract/core:TestINIValueLimitAtItsSize \
              ./internal/extract/core:TestQueryWorkIsBounded \
              ./internal/extract/ini:TestReplayINIShapesAtFullSize \
              ./internal/extract/properties:TestReplayPropertiesShapesAtFullSize \
              ./internal/extract/toolversion:TestReplayToolVersionShapesAtFullSize \
              ./internal/extract/packageversion:TestReplayPackageVersionShapesAtFullSize \
              ./internal/extract/envfile:TestReplayEnvfileShapesAtFullSize \
              ./internal/extract/gitlabci:TestReplayGitLabCIShapesAtFullSize \
              ./internal/extract/githubactions:TestCostIsLinearInTheFile \
              ./internal/extract/shell:TestReplayShellShapesAtFullSize \
              ./internal/extract/shell:TestShellDeepTreeLineTakesNoStack \
              ./internal/extract/shell:TestShellMemoryDoesNotGrowWithStatementsAtFullSize \
              ./internal/extract/shell:TestShellMemoryDoesNotGrowWithStatements \
              ./internal/extract/shell:TestShellEmptyDocumentHoldsNoStatements \
              ./internal/extract/jenkins:TestReplayJenkinsShapesAtFullSize \
              ./internal/extract/ini:TestPHPDeepReferenceCopiesItsTextOnce
slow:
	@for t in $(SLOW_TESTS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		out=$$($(GO) test -count=1 -v -run "^$$name$$" $$pkg 2>&1); status=$$?; \
		echo "$$out" | grep -E "^(--- |ok|FAIL)|_test\.go:[0-9]+: narrowed: "; \
		[ $$status -eq 0 ] || { echo "$$out"; exit 1; }; \
		echo "$$out" | grep -q "^--- PASS: $$name " || \
		  { echo "make slow: $$name did not pass in $$pkg; it is skipped there, or SLOW_TESTS names a test that does not exist" >&2; exit 1; }; \
	done

# The total leaves out the packages named like httptest, extracttest and
# coretest: only tests import them, and their failure branches run only
# when a test fails. Gremlins leaves them out too (.gremlins.yaml).
cover: test
	@grep -vE '^[^:]*test/[^/:]*\.go:' coverage.out > coverage.product.out; \
	total=$$($(GO) tool cover -func=coverage.product.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	echo "coverage $$total% (minimum $(COVERAGE_MIN)%)"; \
	awk -v t=$$total -v m=$(COVERAGE_MIN) 'BEGIN { exit !(t >= m) }'

# The packages fuzz and mutation cover: all, none, or package directories such as
# "internal/config internal/cli". CI's test job sets it from ci/mutation-scope.sh and passes
# FUZZ_FLAGS=-parallel=1 (one fuzz worker, measured at 43% less CPU for the same wall clock).
# Left as they are, both steps cover every package, as locally and weekly. MUTATION_DIFF, a
# commit, narrows mutation to the lines each package in scope changed since it
# (scripts/mutation.sh --diff); CI's mutation job sets it to the base of the scope.
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
MUTATION_SCOPE ?= all
FUZZ_FLAGS     ?=
MUTATION_DIFF  ?=

fuzz:
	@ran=0; for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		case " $(MUTATION_SCOPE) " in *" all "*|*" $${pkg#./} "*) ;; *) continue ;; esac; \
		echo "fuzz $$name for $(FUZZTIME)"; ran=$$((ran + 1)); \
		$(GO) test -run '^$$' -fuzz "^$$name$$" -fuzztime $(FUZZTIME) -fuzzminimizetime $(FUZZMINIMIZETIME) $(FUZZ_FLAGS) $$pkg || exit 1; \
	done; echo "fuzz: $$ran targets in scope ($(MUTATION_SCOPE))"

# scripts/mutation.sh runs gremlins from a copy of the tracked tree, one pass per package of
# MUTATION_SCOPE, each held to its own floors; it says why.
mutation: $(BIN)/gremlins
	GREMLINS=$(BIN)/gremlins scripts/mutation.sh $(if $(MUTATION_DIFF),--diff $(MUTATION_DIFF) )$(MUTATION_SCOPE)

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
# runs it on a module of its own, to show what a diff run mutates.
ci-test: $(BIN)/gremlins
	cd ci/autopatch && $(GO) vet ./... && $(GO) test -count=1 ./...
	GREMLINS=$(BIN)/gremlins ci/test-ci.sh

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
CI_LOCAL_TMP ?= $(HOME)/.cache/errata-ci-local
ci-local:
	@mkdir -p $(CI_LOCAL_TMP)
	TMPDIR=$(CI_LOCAL_TMP) GOPATH=$(CURDIR)/.gopath GOCACHE=$(CURDIR)/.cache/go-build \
	GOLANGCI_LINT_CACHE=$(CURDIR)/.cache/golangci-lint GOFLAGS=-modcacherw \
	$(MAKE) gate FUZZTIME=5s

# Every release binary for every platform, into ./dist. No token, no upload.
# The image is skipped: ko writes a snapshot image to a local Docker daemon.
release-dry: $(BIN)/goreleaser
	$(BIN)/goreleaser check
	$(BIN)/goreleaser release --snapshot --clean --skip=publish,ko

# The commit-msg hook: commit subjects are published on the mirror, so they
# are checked before the commit exists. CI runs the same check on every push.
hooks:
	git config core.hooksPath .githooks
	@echo "commit messages are now checked by ci/check-commit-msg.sh"

# Rewrite the archive checksums for the pinned tool versions.
tool-sums:
	scripts/tool-sums.sh

clean:
	rm -rf errata bin coverage.out coverage.product.out public dist

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
GORELEASER_VERSION    := v2.18.3
SYFT_VERSION          := v1.54.0
KO_VERSION            := v0.19.1

# SHA-256 of each released archive, from the project's own checksum file at
# that version. After a version bump, `make tool-sums` rewrites them; until
# then the download fails its check. govulncheck publishes no binaries and is
# always built.
GOLANGCI_LINT_SHA256_linux_amd64 := 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
GOLANGCI_LINT_SHA256_linux_arm64 := b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998
GREMLINS_SHA256_linux_amd64      := b02a42e47935f891c9a411d68c07e211c7082609e79c2435b67c85ee9658c538
GREMLINS_SHA256_linux_arm64      := 686542674e54559afb7e86d6994d342c19a856cfd970c315748989537936664c
GORELEASER_SHA256_linux_amd64    := b8dee4728bca1aba0d71a832a17cd0a3799f75372878e7ffa36aef2343bce0bc
GORELEASER_SHA256_linux_arm64    := fa7011a7f8de3b504326b8552cf21c708ed3089519b01d372da32ef4e8d298d7
SYFT_SHA256_linux_amd64          := 54a87372498168b2d033e876fd41fa4e8035b872699e525a57046e1f2f09c860
SYFT_SHA256_linux_arm64          := ee6d4566373a05b344bc6b5f1706f14419bf9338ba39ff686e247deefe9b8818
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
# Each fuzz target as package:Name. `go test -fuzz` takes one target per run.
FUZZ_TARGETS := ./internal/config:FuzzParse ./internal/protocol:FuzzMessage ./internal/report:FuzzJUnit

.PHONY: check build tools lint fmt test cover fuzz mutation vuln dogfood public-dry release-dry image-dry ci-local ci-test tool-sums hooks clean

# ci/ is stripped from the public mirror, so a public clone runs the gate
# without the publish and autopatch targets.
INTERNAL := $(if $(wildcard ci/public-tree.sh),public-dry ci-test)

check: lint test cover fuzz mutation vuln dogfood image-dry $(INTERNAL)

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

lint: $(BIN)/golangci-lint
	$(GO) mod tidy -diff
	$(BIN)/golangci-lint run ./...

fmt: $(BIN)/golangci-lint
	$(BIN)/golangci-lint fmt ./...

# Unit, property and API (testscript) tests. -timeout bounds every run, so a
# hung check fails instead of stalling CI.
# One run gives the verdicts and the coverage profile that `cover` reads.
# rapid runs 100 cases per property by default; CI can raise it with
# RAPID_CHECKS=1000 make test.
test:
	$(GO) test -race -shuffle=on -timeout 2m -count=1 -coverprofile=coverage.out -coverpkg=./internal/... ./...

cover: test
	@total=$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	echo "coverage $$total% (minimum $(COVERAGE_MIN)%)"; \
	awk -v t=$$total -v m=$(COVERAGE_MIN) 'BEGIN { exit !(t >= m) }'

fuzz:
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		echo "fuzz $$name for $(FUZZTIME)"; \
		$(GO) test -run '^$$' -fuzz "^$$name$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
	done

mutation: $(BIN)/gremlins
	$(BIN)/gremlins unleash -S lt

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

# The publish and autopatch scripts, against local repositories only.
ci-test:
	cd ci/autopatch && $(GO) vet ./... && $(GO) test -count=1 ./...
	ci/test-ci.sh

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

# The gate with CI's directory layout: Go's module, build and lint caches
# inside the project, as the shared runner cache needs them. Two defects that
# only CI saw came from that layout (a cache directory named like a source
# file, and checks reading dependency sources), so run this before pushing a
# change to the pipeline, the Makefile or the checks.
# TMPDIR is on disk and outside the project: Gremlins copies the project,
# caches included, once per worker into it, which on a tmpfs /tmp fills
# memory, and a TMPDIR inside the project would be copied into itself.
CI_LOCAL_TMP ?= $(HOME)/.cache/errata-ci-local
ci-local:
	@mkdir -p $(CI_LOCAL_TMP)
	TMPDIR=$(CI_LOCAL_TMP) GOPATH=$(CURDIR)/.gopath GOCACHE=$(CURDIR)/.cache/go-build \
	GOLANGCI_LINT_CACHE=$(CURDIR)/.cache/golangci-lint GOFLAGS=-modcacherw \
	$(MAKE) check FUZZTIME=5s

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
	rm -rf errata bin coverage.out public dist

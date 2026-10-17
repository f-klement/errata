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

.PHONY: check build tools lint fmt test cover fuzz mutation vuln dogfood public-dry release-dry ci-test tool-sums clean

# ci/ is stripped from the public mirror, so a public clone runs the gate
# without the publish and autopatch targets.
INTERNAL := $(if $(wildcard ci/public-tree.sh),public-dry ci-test)

check: lint test cover fuzz mutation vuln dogfood $(INTERNAL)

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o errata ./cmd/errata

tools: $(BIN)/golangci-lint $(BIN)/gremlins $(BIN)/govulncheck $(BIN)/goreleaser

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

# Every release binary for every platform, into ./dist. No token, no upload.
# The image is skipped: ko writes a snapshot image to a local Docker daemon.
release-dry: $(BIN)/goreleaser
	$(BIN)/goreleaser check
	$(BIN)/goreleaser release --snapshot --clean --skip=publish,ko

# Rewrite the archive checksums for the pinned tool versions.
tool-sums:
	scripts/tool-sums.sh

clean:
	rm -rf errata bin coverage.out public dist

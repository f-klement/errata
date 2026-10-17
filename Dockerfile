# The errata image, for `docker build -t errata .`.
#
# Releases are built by GoReleaser with ko from the kos section of
# .goreleaser.yaml, which also attaches an SBOM per platform. This file builds
# the same image: the same Go toolchain, the same base image, the binary at the
# same path, the same user and entrypoint. CI builds it on every pipeline, and
# ci/test-ci.sh refuses a base image here that differs from .goreleaser.yaml's
# or a toolchain that differs from the CI image's, so the two recipes cannot
# drift apart.

FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/f-klement/errata/internal/cli.Version=${VERSION}" \
    -o /out/errata ./cmd/errata

# Docker's official bash image, which is Alpine with bash: checks are programs,
# usually shell scripts, and may use bash syntax.
FROM bash:5.3-alpine3.24@sha256:61962062d969cb46dfc2bad061d36342406fa485f64f246aa7e95693ca07df1f
COPY --from=build /out/errata /ko-app/errata
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/ko-app
# Root, as in the released image: CI systems mount a workspace owned by the
# runner, which a non-root container cannot write. `docker run --user` narrows it.
USER 0
ARG VERSION=dev
LABEL org.opencontainers.image.title=errata \
      org.opencontainers.image.description="Checks the claims a repository makes about itself against its files, live systems and next apply." \
      org.opencontainers.image.url=https://hub.docker.com/r/fklement/errata \
      org.opencontainers.image.source=https://github.com/f-klement/errata \
      org.opencontainers.image.documentation=https://github.com/f-klement/errata#readme \
      org.opencontainers.image.licenses=MIT \
      org.opencontainers.image.version=${VERSION}
ENTRYPOINT ["/ko-app/errata"]

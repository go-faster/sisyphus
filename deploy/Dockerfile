# syntax=docker/dockerfile:1
ARG BUILD_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=ubuntu:26.04
# Not pinned to anydoc's MSRV: pinning the compiler to the exact minimum means
# the next anydoc bump breaks the build instead of just needing a newer image.
ARG RUST_IMAGE=rust:1-alpine
# anydoc IS pinned: it is pre-1.0 and its Markdown changes between versions,
# which re-chunks and re-embeds every converted document. Bump deliberately.
# `make anydoc` reads this line, so it stays the one place the version lives.
ARG ANYDOC_VERSION=0.1.7

# The office-document converter ssingest runs as a subprocess. Its own stage,
# so nothing about it reaches the Go build: every binary stays CGO_ENABLED=0
# and the runtime image gets one static 8 MB executable. Alpine's host target
# is musl, and anydoc has no C dependencies, so the result is fully static and
# runs on the Ubuntu runtime unchanged.
#
# This builds natively for one architecture, which is what a compose/dev image
# needs. A multi-platform `buildx` build would compile this stage once per
# platform under QEMU, which for a Rust compile is far too slow to accept -
# so the release image (deploy/Dockerfile.goreleaser, one buildx invocation
# across amd64+arm64, compiling nothing) does NOT ship the converter yet. It
# needs the same treatment goreleaser gives the Go binaries: cross-compile on
# the host, COPY per $TARGETARCH.
FROM $RUST_IMAGE AS anydoc
ARG ANYDOC_VERSION
ARG TARGETARCH
RUN apk add --no-cache musl-dev
RUN --mount=type=cache,target=/usr/local/cargo/registry,id=cargo-$TARGETARCH \
    cargo install anydoc --version $ANYDOC_VERSION --example convert --root /out

FROM $BUILD_IMAGE AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/ssapi ./cmd/ssapi && \
    CGO_ENABLED=0 go build -o /out/ssbot ./cmd/ssbot && \
    CGO_ENABLED=0 go build -o /out/ssagent ./cmd/ssagent && \
    CGO_ENABLED=0 go build -o /out/ssmcp ./cmd/ssmcp && \
    CGO_ENABLED=0 go build -o /out/ssingest ./cmd/ssingest && \
    CGO_ENABLED=0 go build -o /out/shits ./cmd/shits

FROM $RUNTIME_IMAGE
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=anydoc /out/bin/convert /usr/local/bin/anydoc
COPY --from=build /out/ssapi /usr/local/bin/ssapi
COPY --from=build /out/ssbot /usr/local/bin/ssbot
COPY --from=build /out/ssagent /usr/local/bin/ssagent
COPY --from=build /out/ssmcp /usr/local/bin/ssmcp
COPY --from=build /out/ssingest /usr/local/bin/ssingest
COPY --from=build /out/shits /usr/local/bin/shits
ENTRYPOINT ["ssapi"]

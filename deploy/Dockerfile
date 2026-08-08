# syntax=docker/dockerfile:1
ARG BUILD_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=ubuntu:26.04
ARG RUST_IMAGE=rust:1-alpine
# Pinned: anydoc's Markdown changes between versions, re-embedding every
# converted document. `make anydoc` reads this line.
ARG ANYDOC_VERSION=0.1.7

# Office-document converter, run as a subprocess by ssingest. Static musl, so
# the Go build stays CGO_ENABLED=0. Native single-arch: Rust under QEMU is too
# slow for a multi-platform build, which is why the goreleaser release image
# does not ship it yet.
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

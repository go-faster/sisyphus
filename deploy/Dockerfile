# syntax=docker/dockerfile:1
ARG BUILD_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=ubuntu:26.04

FROM $BUILD_IMAGE AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/scpbot ./cmd/scpbot && \
    CGO_ENABLED=0 go build -o /out/scpmcp ./cmd/scpmcp && \
    CGO_ENABLED=0 go build -o /out/scpingest ./cmd/scpingest

FROM $RUNTIME_IMAGE
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/scpbot /usr/local/bin/scpbot
COPY --from=build /out/scpmcp /usr/local/bin/scpmcp
COPY --from=build /out/scpingest /usr/local/bin/scpingest
ENTRYPOINT ["scpbot"]

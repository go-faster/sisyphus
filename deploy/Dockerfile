# syntax=docker/dockerfile:1
ARG BUILD_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=ubuntu:26.04

FROM $BUILD_IMAGE AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/ssapi ./cmd/ssapi && \
    CGO_ENABLED=0 go build -o /out/ssbot ./cmd/ssbot && \
    CGO_ENABLED=0 go build -o /out/ssmcp ./cmd/ssmcp && \
    CGO_ENABLED=0 go build -o /out/ssingest ./cmd/ssingest

FROM $RUNTIME_IMAGE
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ssapi /usr/local/bin/ssapi
COPY --from=build /out/ssbot /usr/local/bin/ssbot
COPY --from=build /out/ssmcp /usr/local/bin/ssmcp
COPY --from=build /out/ssingest /usr/local/bin/ssingest
ENTRYPOINT ["ssapi"]

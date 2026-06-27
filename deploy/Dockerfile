# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/scpbot ./cmd/scpbot

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/scpbot /usr/local/bin/scpbot
ENTRYPOINT ["scpbot"]

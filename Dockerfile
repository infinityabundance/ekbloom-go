# syntax=docker/dockerfile:1
# Minimal multi-stage image for the pure-Go EKBLOOM codec.
#
#   docker build --target runtime -t ekbloom-go:latest .
#   docker build --target test    -t ekbloom-go:test   .
#   docker run --rm ekbloom-go:latest inspect /data/archive.ekb
#
# The build needs no network: the module has no external dependencies.

FROM golang:1.23-alpine AS base
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
COPY go.mod ./
COPY . .

# --- test stage: vet + full test suite (race needs gcc; keep it lean without) ---
FROM base AS test
RUN go vet ./... && go test ./...

# --- build stage: static binary ---
FROM base AS build
RUN go build -trimpath -ldflags "-s -w" -o /out/ekbloom ./cmd/ekbloom

# --- runtime stage: minimal, non-root ---
FROM alpine:3.20 AS runtime
LABEL org.opencontainers.image.title="ekbloom-go" \
      org.opencontainers.image.description="Pure-Go EKBLOOM codec (byte-for-byte compatible with the Rust reference)" \
      org.opencontainers.image.source="https://github.com/infinityabundance/ekbloom-go" \
      org.opencontainers.image.licenses="MIT"
RUN adduser -D -H -u 10001 ekbloom
COPY --from=build /out/ekbloom /usr/local/bin/ekbloom
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/ekbloom"]
CMD ["version"]

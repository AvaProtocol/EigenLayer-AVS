# Multi-stage build for Railway deployment
# Produces a single binary (./ap) that runs as gateway, worker, or aggregator
# depending on the start command configured per Railway service.

FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build with version metadata. SEMVER + REVISION are derived from git
# inside the builder (the build context retains .git — see .dockerignore).
# CI workflows can still inject explicit overrides via --build-arg, e.g.
# `docker build --build-arg SEMVER=3.2.0 --build-arg REVISION=abc1234`.
ARG SEMVER=
ARG REVISION=
RUN _git_tag=$(git describe --tags --abbrev=0 2>/dev/null); \
    SEMVER="${SEMVER:-$([ -n "${_git_tag}" ] && echo "${_git_tag#v}" || echo dev)}"; \
    REVISION="${REVISION:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"; \
    echo "Building with semver=${SEMVER} revision=${REVISION}" && \
    go build \
      -o ./ap \
      -ldflags "-X 'github.com/AvaProtocol/EigenLayer-AVS/version.semver=${SEMVER}' -X 'github.com/AvaProtocol/EigenLayer-AVS/version.revision=${REVISION}'"

# Runtime image
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/ap .
COPY --from=builder /app/config ./config
COPY --from=builder /app/scripts/operator-entrypoint.sh ./scripts/

ENTRYPOINT ["./ap"]

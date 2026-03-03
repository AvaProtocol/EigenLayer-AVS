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

# Build with version info
ARG GIT_REVISION=unknown
RUN go build \
    -o ./ap \
    -ldflags "-X github.com/AvaProtocol/EigenLayer-AVS/version.revision=${GIT_REVISION}"

# Runtime image
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/ap .
COPY --from=builder /app/config ./config
COPY --from=builder /app/token_whitelist ./token_whitelist

ENTRYPOINT ["./ap"]

FROM golang:1.24-alpine AS builder
ARG RELEASE_TAG
ARG COMMIT_SHA
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -v \
    -ldflags "-X github.com/AvaProtocol/EigenLayer-AVS/version.semver=$RELEASE_TAG -X github.com/AvaProtocol/EigenLayer-AVS/version.revision=$COMMIT_SHA" \
    -o /ava


FROM debian:stable-slim

WORKDIR /app

RUN useradd -ms /bin/bash ava && \
    apt update && apt-get install -y ca-certificates socat telnet

COPY --from=builder /ava /ava

# Bundle the in-repo sample/local-dev configs so external operators can reach
# a template (e.g. config/operator_sample.yaml) from inside the container.
# Production Railway services do NOT use these: each receives its config via
# the AP_CONFIG_YAML env var, which the entrypoint writes to config/runtime.yaml
# at boot. The prod *-railway.yaml configs live in the avs-infra repo, not here.
COPY --from=builder /app/config /app/config

# Bundle the shared entrypoint. It materializes per-deployment inputs from env
# vars at boot — operator ECDSA/BLS keystores AND the service config
# (AP_CONFIG_YAML -> config/runtime.yaml) — then execs the binary. Auto-detects
# /ava (this image) vs ./ap (root Dockerfile). Railway Start Commands route
# through it, e.g.:
#   /app/scripts/operator-entrypoint.sh operator --config=config/runtime.yaml
COPY --from=builder /app/scripts/operator-entrypoint.sh /app/scripts/operator-entrypoint.sh

ENTRYPOINT ["/ava"]

# No default CMD: this image is shared with external operators, and
# silently flipping a no-args invocation from cobra-help-exit-0 to
# aggregator-boot-fail would change exit codes / monitoring signals
# for their existing setups. Callers always pass a subcommand:
#   - Operators: `docker run avaprotocol/ap-avs operator --config=...`
#   - Railway gateway: Start Command set in the Railway dashboard.

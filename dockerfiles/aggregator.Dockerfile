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

# Bundle the in-repo configs so the image is self-contained: the Railway
# gateway service can run with no external mount, and operators reach
# config/operator_sample.yaml from inside the container if they want.
# Without this, `/ava aggregator --config=config/gateway-railway.yaml`
# fails with "file not found" — see EigenLayer-AVS gateway switchover.
COPY --from=builder /app/config /app/config

ENTRYPOINT ["/ava"]

# No default CMD: this image is shared with external operators, and
# silently flipping a no-args invocation from cobra-help-exit-0 to
# aggregator-boot-fail would change exit codes / monitoring signals
# for their existing setups. Callers always pass a subcommand:
#   - Operators: `docker run avaprotocol/ap-avs operator --config=...`
#   - Railway gateway: Start Command set in the Railway dashboard.

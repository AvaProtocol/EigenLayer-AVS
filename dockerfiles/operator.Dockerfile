FROM golang:1.23 AS builder
ARG RELEASE_TAG
ARG COMMIT_SHA

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/AvaProtocol/EigenLayer-AVS/version.semver=$RELEASE_TAG" \
    -ldflags "-X github.com/AvaProtocol/EigenLayer-AVS/version.revision=$COMMIT_SHA" \
    -o /ava


FROM debian:stable-slim

WORKDIR /app

RUN useradd -ms /bin/bash ava && \
    apt update && apt-get install -y ca-certificates socat telnet

COPY --from=builder /ava /ava

ENTRYPOINT ["/ava"]

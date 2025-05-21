FROM --platform=$BUILDPLATFORM golang:1.22.1-alpine AS builder
ARG RELEASE_TAG
ARG COMMIT_SHA
ARG TARGETPLATFORM
ARG BUILDPLATFORM

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(echo $TARGETPLATFORM | cut -d/ -f2) go build -v \
    -ldflags "-X github.com/AvaProtocol/EigenLayer-AVS/version.semver=$RELEASE_TAG -X github.com/AvaProtocol/EigenLayer-AVS/version.revision=$COMMIT_SHA" \
    -o /ava


FROM --platform=$TARGETPLATFORM debian:stable-slim

WORKDIR /app

RUN useradd -ms /bin/bash ava && \
    apt update && apt-get install -y ca-certificates socat telnet

COPY --from=builder /ava /ava

ENTRYPOINT ["/ava"]

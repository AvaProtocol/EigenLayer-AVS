FROM golang:1.22 as builder
ARG RELEASE_TAG

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/AvaProtocol/ap-avs/version.semver=$RELEASE_TAG" \
    -o /ava


FROM debian:stable-slim

WORKDIR /app

RUN useradd -ms /bin/bash ava && \
    apt update && apt-get install -y ca-certificates

COPY --from=builder /ava /ava

ENTRYPOINT ["/ava"]

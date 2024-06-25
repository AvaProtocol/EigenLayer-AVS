FROM golang:1.22 as builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux go build -o /ava


FROM debian:stable-slim

COPY --from=builder /ava /ava
ENTRYPOINT ["/ava"]

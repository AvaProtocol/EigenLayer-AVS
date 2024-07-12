# Ava Protocol

To interact with Ava Protocol, you start by making request to our grpc endpoint. Our protocol is defined inside `protobuf` directory and can code gen client for your language.

# Endpoint

## Prod(Ethereum)

**aggregator-holesky.avaprotocol.org:2206**

## Staging(Holesky)

**aggregator-holesky.avaprotocol.org:2206**

## Local dev

If using our docker compose, the default port is `2206` so our endpoint is `http://localhost:2206`

You can interactively develop with grpui on http://localhost:8080

# Authentication

To start interacting with our protocol for task management, the process is
generally:

## 1. Exchange an `auth token`

Call `GetKey` method with below data.

- owner: your wallet address
- expired_at: epoch when your key will be expired
- signature: sign a message in form of `key request for ${wallet.address}
  expired at ${expired_at)}`

The response will have the key which can set on the metadata of subsequent
request. The token will be expired at the `expired_at` epoch.

Please check `examples/signature.js` for reference code on how to generate this
signature.

# 2. Making GRPC request

After having auth token, any request that require authentication,set `authkey: ${your-key-from-above}` header in the request.

Because an account need to send over an auth key generate from the signature
above, no one will be able to view your data and therefore your task and
parameter data will be private.

# API client

Using protocol definition in `protobuf` anyone can generate a client use
traditional grpc tool.

# API Method

## Create Task

## List An Account Task

## Delete Task

## Get Smart Account Address

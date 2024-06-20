# Ava Protocol

To interact with Ava Protocol, you start by making request to our grpc endpoint. Our protocol is defined inside `protobuf` directory and can code gen client for your language.

# Endpoint

## Prod

## Staging

## Local dev

If using our docker compose, the default port is `2206` so our endpoint is `http://localhost:2206`

You can interactively develop with grpui on http://localhost:8080
# Authentication

Before doing anything, send a request to `GetKey` with 3 information:

- owner: your wallet address
- expired_at: when your key will be expired
- signature: sign a message in form of `RequestKey,owner=${0x123},expired=${epoch}`

The response will have the key which can set on the metadata of subsequent
request.

# Making GRPC request

In subsequent request to the endpoint, set `authkey: ${your-key-from-above}` request.

# Create a task

# List my task

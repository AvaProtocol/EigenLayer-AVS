# Install protoc

```
# For working with grpc
brew install grpc protobuf

# For generate abibinding
go install github.com/ethereum/go-ethereum/cmd/abigen@latest
```

# Interactively Test RPC Server

```
brew install grpcui
```

Then run 

```
grpcui -plaintext localhost:2206
```

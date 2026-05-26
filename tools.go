//go:build tools
// +build tools

// Package tools pins build-time tool dependencies so `go mod tidy`
// doesn't drop them. None of these are imported by production code;
// the build tag keeps them out of binaries.
package tools

import (
	// oapi-codegen generates Go types and the Echo server interface
	// from api/openapi.yaml. Invoked via `make rest-gen`.
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)

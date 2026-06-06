// Package tokenwhitelist embeds the per-chain token catalog JSON
// files into the compiled binary so the runtime doesn't depend on the
// gateway's working directory containing a token_whitelist/ subtree.
// The files in this package are the only thing committed to the repo;
// they're populated from `@avaprotocol/protocols` via `make sync-tokens`
// and a CI drift gate fails any hand-edit.
//
// Consumers should NOT read these files via `os.ReadFile` — go through
// the exported `FS` (an `embed.FS`) so the data stays in lockstep with
// what's baked into the binary.
package tokenwhitelist

import "embed"

// FS holds the per-chain token catalog files keyed by their filename
// (e.g. "ethereum.json"). Filename → chain ID mapping lives next to
// the consumers in core/taskengine/{token_metadata.go, token_catalog.go};
// this package owns only the data, never the routing.
//
//go:embed *.json
var FS embed.FS

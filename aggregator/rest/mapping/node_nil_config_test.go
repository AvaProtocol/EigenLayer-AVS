package mapping

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// Every Node variant's OpenAPI Config is a `*Config` with `omitempty`, so a
// caller that POSTs `"config": null` — or omits the field entirely — reaches
// OpenAPIToProtoNode with a nil Config. The customCode and loop arms read
// through that pointer directly (the other nine hand it to jsonRetargetProto,
// which marshals a nil pointer to `null` and lets protojson reject it), so
// they panicked instead of returning a 400. The panic surfaced as a 500 via
// Echo's recover middleware — Sentry EIGENLAYER-AVS-29.
//
// This table covers every variant so a future arm that dereferences Config
// without a nil check is caught here rather than in production.
func TestOpenAPIToProtoNodeNilConfigIsRejectedNotPanic(t *testing.T) {
	variants := []string{
		"ethTransfer",
		"contractWrite",
		"contractRead",
		"graphqlQuery",
		"restApi",
		"branch",
		"filter",
		"loop",
		"customCode",
		"balance",
		"await",
	}

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			for _, body := range []string{
				`{"id":"n1","type":"` + variant + `","config":null}`, // explicit null
				`{"id":"n1","type":"` + variant + `"}`,               // config omitted
			} {
				var node generated.Node
				require.NoError(t, json.Unmarshal([]byte(body), &node))

				require.NotPanics(t, func() {
					out, err := OpenAPIToProtoNode(node)
					assert.Error(t, err, "a node with no config must be rejected: %s", body)
					assert.Nil(t, out)
				}, "payload must not panic: %s", body)
			}
		})
	}
}

// The nil-config rejection must name the offending node so the 400 the caller
// receives is actionable rather than an opaque decode failure.
func TestOpenAPIToProtoNodeNilConfigErrorNamesNode(t *testing.T) {
	for variant, want := range map[string]string{
		"customCode": "CustomCodeNode missing config",
		"loop":       "LoopNode missing config",
	} {
		t.Run(variant, func(t *testing.T) {
			var node generated.Node
			require.NoError(t, json.Unmarshal([]byte(`{"id":"bad-node","type":"`+variant+`","config":null}`), &node))

			_, err := OpenAPIToProtoNode(node)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "bad-node")
			assert.Contains(t, err.Error(), want)
		})
	}
}

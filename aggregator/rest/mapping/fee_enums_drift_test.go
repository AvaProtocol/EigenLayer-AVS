package mapping

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
)

// TestFeeEnumsDriftFromOpenAPI guards the engine-side canonical
// enum constants (core/taskengine/fee_enums.go) against the
// OpenAPI-generated REST enum types in this package's `generated`
// sibling. If either side adds/removes/renames a value, this test
// fails — which is the intended catch.
//
// Background: NodeCOGS.cost_type, ValueFee.classification_method,
// and FeeDiscount.discount_type are declared as protobuf `string`
// fields, not protobuf enums. protojson camelCases field NAMES but
// passes field VALUES through verbatim. So the canonical wire
// value is whatever the engine literally writes into the field —
// and the only way to keep that aligned with the REST contract
// (which DOES typed-enum these fields) is to source the strings
// from one canonical place and assert the sets match.
func TestFeeEnumsDriftFromOpenAPI(t *testing.T) {
	cases := []struct {
		name    string
		openAPI []string
		engine  []string
	}{
		{
			name: "NodeCOGS.costType",
			openAPI: []string{
				string(generated.Gas),
				string(generated.ExternalApi),
				string(generated.WalletCreation),
			},
			engine: []string{
				taskengine.CostTypeGas,
				taskengine.CostTypeExternalAPI,
				taskengine.CostTypeWalletCreation,
			},
		},
		{
			name: "ValueFee.classificationMethod",
			openAPI: []string{
				string(generated.RuleBased),
				string(generated.Llm),
			},
			engine: []string{
				taskengine.ClassificationMethodRuleBased,
				taskengine.ClassificationMethodLLM,
			},
		},
		{
			name: "FeeDiscount.discountType",
			openAPI: []string{
				string(generated.NewUser),
				string(generated.Volume),
				string(generated.Promotional),
				string(generated.BetaProgram),
			},
			engine: []string{
				taskengine.DiscountTypeNewUser,
				taskengine.DiscountTypeVolume,
				taskengine.DiscountTypePromotional,
				taskengine.DiscountTypeBetaProgram,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			openAPI := append([]string(nil), c.openAPI...)
			engine := append([]string(nil), c.engine...)
			sort.Strings(openAPI)
			sort.Strings(engine)
			assert.Equal(t, openAPI, engine,
				"%s: engine constants in core/taskengine/fee_enums.go diverged from OpenAPI enum in api/openapi.yaml. Update one side to match.",
				c.name,
			)
		})
	}
}

package mapping

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestFeeEnumsDriftCountFromGeneratedSource parses the OpenAPI-generated
// types file directly and confirms the explicit enumeration in
// TestFeeEnumsDriftFromOpenAPI above covers EVERY const of each typed
// enum the spec declares — not just the ones a maintainer remembered
// to list.
//
// Without this count check, OpenAPI could grow a new value (e.g. add
// "studentDiscount" to FeeDiscountDiscountType), regen
// aggregator/rest/generated, and the value-level drift test above
// would keep passing — silently, because nobody added it to the
// explicit slice. This test catches that by counting `const`
// declarations of each typed enum and comparing to what the value
// test enumerates.
func TestFeeEnumsDriftCountFromGeneratedSource(t *testing.T) {
	fset := token.NewFileSet()
	src := filepath.Join("..", "generated", "types.gen.go")
	file, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "failed to parse %s", src)

	// Expected count per typed enum — must match the explicit slices
	// above. Update both together.
	targets := map[string]int{
		"NodeCOGSCostType":             3, // gas, externalApi, walletCreation
		"ValueFeeClassificationMethod": 2, // ruleBased, llm
		"FeeDiscountDiscountType":      4, // newUser, volume, promotional, betaProgram
	}
	found := map[string]int{}

	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		// Const specs in the same group inherit the previous spec's
		// type when omitted (canonical Go behavior). The generated file
		// today spells the type on every spec, but track lastType in
		// case the generator changes.
		var lastType string
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				if id, ok := vs.Type.(*ast.Ident); ok {
					lastType = id.Name
				} else {
					lastType = ""
				}
			}
			if _, isTarget := targets[lastType]; isTarget {
				found[lastType] += len(vs.Names)
			}
		}
	}

	for name, expected := range targets {
		assert.Equalf(t, expected, found[name],
			"%s has %d const(s) in aggregator/rest/generated/types.gen.go but TestFeeEnumsDriftFromOpenAPI enumerates %d. Update BOTH the engine constants in core/taskengine/fee_enums.go AND the explicit slices in TestFeeEnumsDriftFromOpenAPI above (and bump the count in `targets`).",
			name, found[name], expected,
		)
	}
}

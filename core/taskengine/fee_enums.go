package taskengine

// Canonical string values for the proto-string-typed enum fields the
// fee estimator emits into NodeCOGS, ValueFee, and FeeDiscount. These
// values are the wire format the REST surface advertises in
// api/openapi.yaml (e.g. NodeCOGS.costType ∈ {gas, externalApi,
// walletCreation}). Field NAMES are camelCased automatically by
// protojson on the way out; field VALUES are passed through
// verbatim, so any engine emit site has to spell the canonical
// camelCase form itself — there's no implicit translation layer.
//
// The aggregator/rest/mapping package owns a drift-check test that
// fails the build if these diverge from the OpenAPI-generated enum
// sets in aggregator/rest/generated.

// CostType values for NodeCOGS.cost_type. Mirrors the OpenAPI enum
// NodeCOGSCostType in api/openapi.yaml.
const (
	CostTypeGas            = "gas"
	CostTypeExternalAPI    = "externalApi"
	CostTypeWalletCreation = "walletCreation"
)

// ClassificationMethod values for ValueFee.classification_method.
// Mirrors the OpenAPI enum ValueFeeClassificationMethod.
const (
	ClassificationMethodRuleBased = "ruleBased"
	ClassificationMethodLLM       = "llm"
)

// DiscountType values for FeeDiscount.discount_type. Mirrors the
// OpenAPI enum FeeDiscountDiscountType. No emit sites today
// (Discounts is always returned as an empty slice), but constants
// land here so future wiring spells the right value the first time.
const (
	DiscountTypeNewUser     = "newUser"
	DiscountTypeVolume      = "volume"
	DiscountTypePromotional = "promotional"
	DiscountTypeBetaProgram = "betaProgram"
)

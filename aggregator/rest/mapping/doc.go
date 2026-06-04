// Package mapping translates between the OpenAPI-generated REST types
// (aggregator/rest/generated) and the protobuf-generated engine types
// (protobuf). The two layers can't share a Go type system: the REST
// surface uses oapi-codegen's union encoding (a `json.RawMessage` plus
// As* / From* helpers keyed by a `type` discriminator), while the engine
// uses protobuf's `oneof` (a sealed interface implemented by per-variant
// wrapper structs).
//
// All conversion lives here so handlers stay thin. Each pair of
// functions follows the same naming convention:
//
//	OpenAPIToProto<X>(api) (*proto.X, error)
//	ProtoToOpenAPI<X>(proto) (api.X, error)
//
// Both directions return an error rather than panicking on an unknown
// variant — the REST layer turns that into a 400 problem+json so the
// caller sees a precise reason rather than a 500.
package mapping

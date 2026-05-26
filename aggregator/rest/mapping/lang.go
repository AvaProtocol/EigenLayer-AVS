package mapping

import (
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// openAPILangToProto maps the wire-form Lang strings used by the OpenAPI
// surface (lowercase: "javascript", "json", "graphql", "handlebars") to
// the proto Lang enum. Unknown values collapse to LANG_UNSPECIFIED so
// the engine can reject them with a clear validation error rather than
// silently picking a default.
func openAPILangToProto(in generated.Lang) avsproto.Lang {
	switch in {
	case generated.Javascript:
		return avsproto.Lang_LANG_JAVASCRIPT
	case generated.Json:
		return avsproto.Lang_LANG_JSON
	case generated.Graphql:
		return avsproto.Lang_LANG_GRAPHQL
	case generated.Handlebars:
		return avsproto.Lang_LANG_HANDLEBARS
	default:
		return avsproto.Lang_LANG_UNSPECIFIED
	}
}

// protoLangToOpenAPI is the inverse of openAPILangToProto. Used when
// rendering stored workflows back to API consumers.
func protoLangToOpenAPI(in avsproto.Lang) generated.Lang {
	switch in {
	case avsproto.Lang_LANG_JAVASCRIPT:
		return generated.Javascript
	case avsproto.Lang_LANG_JSON:
		return generated.Json
	case avsproto.Lang_LANG_GRAPHQL:
		return generated.Graphql
	case avsproto.Lang_LANG_HANDLEBARS:
		return generated.Handlebars
	default:
		return generated.Javascript
	}
}

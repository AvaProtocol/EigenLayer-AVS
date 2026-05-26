package mapping

import (
	"encoding/json"
	"strconv"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ProtoToOpenAPIEstimateFees translates the engine's EstimateFeesResp
// into the OpenAPI EstimateFeesResponse shape. Per-node COGS, fee
// objects and discount entries roundtrip via protojson because the
// camelCase field names already match. chainId crosses a small type
// boundary (string in proto, int64 in OpenAPI) so it's hand-mapped.
func ProtoToOpenAPIEstimateFees(in *avsproto.EstimateFeesResp) generated.EstimateFeesResponse {
	out := generated.EstimateFeesResponse{}

	if cid := in.GetChainId(); cid != "" {
		if parsed, err := strconv.ParseInt(cid, 10, 64); err == nil {
			out.ChainId = parsed
		}
	}

	if fee := in.GetExecutionFee(); fee != nil {
		var f generated.Fee
		_ = protoRetargetJSON(fee, &f)
		out.ExecutionFee = f
	}
	if vf := in.GetValueFee(); vf != nil {
		var f generated.ValueFee
		_ = protoRetargetJSON(vf, &f)
		out.ValueFee = f
	}
	if nt := in.GetNativeToken(); nt != nil {
		var n generated.NativeToken
		_ = protoRetargetJSON(nt, &n)
		out.NativeToken = &n
	}

	if cogs := in.GetCogs(); len(cogs) > 0 {
		mapped := make([]generated.NodeCOGS, 0, len(cogs))
		for _, c := range cogs {
			var n generated.NodeCOGS
			if err := protoRetargetJSON(c, &n); err == nil {
				mapped = append(mapped, n)
			}
		}
		out.Cogs = mapped
	}

	if discounts := in.GetDiscounts(); len(discounts) > 0 {
		mapped := make([]generated.FeeDiscount, 0, len(discounts))
		for _, d := range discounts {
			var dd generated.FeeDiscount
			if err := protoRetargetJSON(d, &dd); err == nil {
				mapped = append(mapped, dd)
			}
		}
		out.Discounts = &mapped
	}

	if pm := in.GetPricingModel(); pm != "" {
		out.PricingModel = &pm
	}
	// Warnings are not surfaced via OpenAPI today — clients get the
	// pricing model + chainId + the structured fee fields. Add a
	// `warnings` array to EstimateFeesResponse in the spec if the SDK
	// starts depending on them.
	return out
}

// (silence unused-import lints for the JSON/protojson packages in case
// future helpers need them — both are already used elsewhere in the
// mapping package, but the linter checks per-file.)
var _ = json.Marshal
var _ = protojson.Marshal

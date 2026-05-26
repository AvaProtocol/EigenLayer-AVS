package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Tokens resource — see api/openapi.yaml `tags: [Tokens]`.

// GetToken — GET /api/v1/tokens/{address}
//
// Looks up token metadata for the supplied contract address. The
// chainId query param selects which chain's enrichment service answers
// (defaults to the aggregator's primary chain). Returns 200 with
// `found: false` if the token isn't in the whitelist and no on-chain
// fallback succeeded — there's no 404 here because partial information
// (just the address) is still useful to the caller.
func (s *Server) GetToken(ctx echo.Context, address generated.EthereumAddress, params generated.GetTokenParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.GetTokenMetadataReq{Address: string(address)}
	if params.ChainId != nil {
		req.ChainId = *params.ChainId
	}

	resp, err := s.engine.GetTokenMetadata(user, req)
	if err != nil {
		return err
	}

	out := generated.TokenMetadataResponse{Found: resp.GetFound()}
	if src := resp.GetSource(); src != "" {
		s := generated.TokenMetadataResponseSource(src)
		out.Source = &s
	}
	if req.ChainId != 0 {
		cid := req.ChainId
		out.ChainId = &cid
	}
	if t := resp.GetToken(); t != nil {
		if a := t.GetId(); a != "" {
			a := generated.EthereumAddress(a)
			out.Address = &a
		}
		if n := t.GetName(); n != "" {
			out.Name = &n
		}
		if sym := t.GetSymbol(); sym != "" {
			out.Symbol = &sym
		}
		if d := t.GetDecimals(); d != 0 {
			d := int32(d)
			out.Decimals = &d
		}
	}
	return ctx.JSON(http.StatusOK, out)
}

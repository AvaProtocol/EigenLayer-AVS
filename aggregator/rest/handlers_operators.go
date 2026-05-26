package rest

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// Operators resource — see api/openapi.yaml `tags: [Operators]`.

// ListOperators — GET /api/v1/operators
//
// Read-only monitoring endpoint. Returns each connected operator's
// capabilities, lastSeen, and version. Routing decisions still live
// inside the gateway; this is for dashboards and debugging multi-chain
// task routing.
//
// The supportedChainIds field is part of the response shape but the
// operator Checkin payload doesn't yet carry it (Phase 2 multi-chain
// work still in flight) — it returns as an empty array until that
// lands. Clients should treat empty as "unknown", not "none".
func (s *Server) ListOperators(ctx echo.Context) error {
	if s.operators == nil {
		return ctx.JSON(http.StatusOK, generated.OperatorList{Data: []generated.OperatorInfo{}})
	}

	views := s.operators.List()
	data := make([]generated.OperatorInfo, 0, len(views))
	for _, v := range views {
		info := generated.OperatorInfo{
			Address:           generated.EthereumAddress(v.Address),
			SupportedChainIds: v.SupportedChainIDs,
		}
		if info.SupportedChainIds == nil {
			info.SupportedChainIds = []generated.ChainId{}
		}
		if v.Version != "" {
			vv := v.Version
			info.Version = &vv
		}
		if v.BlockNumber != 0 {
			b := v.BlockNumber
			info.BlockNumber = &b
		}
		if v.EventCount != 0 {
			ec := v.EventCount
			info.EventCount = &ec
		}
		if v.LastPingEpochMs != 0 {
			ts := lastSeenFromEpoch(v.LastPingEpochMs)
			info.LastSeen = &ts
		}
		data = append(data, info)
	}
	return ctx.JSON(http.StatusOK, generated.OperatorList{Data: data})
}

// lastSeenFromEpoch normalizes the OperatorNode.LastPingEpoch field,
// which historically can be seconds or milliseconds depending on when
// the row was written. >1e12 means milliseconds (anything dated after
// the year 2001 in millisecond epoch units).
func lastSeenFromEpoch(epoch int64) time.Time {
	if epoch > 1e12 {
		return time.UnixMilli(epoch).UTC()
	}
	return time.Unix(epoch, 0).UTC()
}

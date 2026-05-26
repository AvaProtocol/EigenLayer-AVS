package aggregator

import (
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest"
)

// operatorPoolAdapter adapts the aggregator's *OperatorPool to the
// rest.OperatorLister interface so the REST package doesn't need to
// import aggregator (which would cycle). The adapter copies the fields
// the REST layer actually surfaces; the internal *OperatorNode keeps
// extra columns (RemoteIP, MetricsPort) that aren't part of the public
// API shape.
type operatorPoolAdapter struct {
	pool *OperatorPool
}

func (a *operatorPoolAdapter) List() []rest.OperatorView {
	if a == nil || a.pool == nil {
		return nil
	}
	nodes := a.pool.GetAll()
	out := make([]rest.OperatorView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, rest.OperatorView{
			Address:         n.Address,
			Name:            n.Name,
			Version:         n.Version,
			BlockNumber:     n.BlockNumer,
			EventCount:      n.EventCount,
			LastPingEpochMs: n.LastPingEpoch,
			// SupportedChainIDs is left empty until the operator Checkin
			// payload carries the field (Phase 2 multi-chain work).
		})
	}
	return out
}

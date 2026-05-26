package aggregator

import (
	"context"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
)

// buildRestDeps gathers every dependency the REST layer needs from the
// rest of the aggregator's state. Called from startHttpServer after
// startRpcServer + startTaskEngine have populated agg.smartWalletRpc,
// agg.priceService, and agg.rpcServer.
func (agg *Aggregator) buildRestDeps() rest.ServerDeps {
	return rest.ServerDeps{
		Operators:      &operatorPoolAdapter{pool: agg.operatorPool},
		SmartWalletRpc: agg.smartWalletRpc,
		PriceService:   agg.priceService,
		WithdrawSvc:    &withdrawServiceAdapter{rpc: agg.rpcServer},
	}
}

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

// withdrawServiceAdapter adapts RpcServer.ExecuteWithdraw to the
// rest.WithdrawService interface. The full UserOp pipeline (bundler
// client, paymaster, balance + gas estimation, max-amount calc) stays
// inside the aggregator package — the REST layer just supplies the
// authed user + request shape and translates the protobuf response
// back to the OpenAPI WithdrawResponse.
type withdrawServiceAdapter struct {
	rpc *RpcServer
}

func (a *withdrawServiceAdapter) Withdraw(ctx context.Context, req rest.WithdrawRequest) (rest.WithdrawResult, error) {
	if a == nil || a.rpc == nil {
		return rest.WithdrawResult{}, fmt.Errorf("withdraw service not initialized")
	}
	if !common.IsHexAddress(req.Owner) {
		return rest.WithdrawResult{}, fmt.Errorf("owner is not a valid hex address")
	}
	user := &model.User{Address: common.HexToAddress(req.Owner)}
	// SmartAccountAddress is intentionally not derived here — the
	// gRPC handler doesn't read it for the withdraw flow either; it
	// uses payload.SmartWalletAddress instead.

	payload := &avsproto.WithdrawFundsReq{
		RecipientAddress:   req.RecipientAddress,
		Amount:             req.Amount,
		Token:              req.Token,
		SmartWalletAddress: req.SmartWalletAddress,
		ChainId:            req.ChainID,
	}

	resp, err := a.rpc.ExecuteWithdraw(ctx, user, payload)
	if err != nil {
		return rest.WithdrawResult{}, err
	}
	return rest.WithdrawResult{
		UserOpHash:      resp.GetUserOpHash(),
		TransactionHash: resp.GetTransactionHash(),
		Status:          resp.GetStatus(),
	}, nil
}

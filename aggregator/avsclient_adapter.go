package aggregator

import (
	"context"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/avsclient"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
)

// AvsClientSurface bundles every dependency a consumer needs into an
// avsclient.Surface. Called from startHttpServer after startRpcServer
// + startTaskEngine have populated agg.smartWalletRpc,
// agg.priceService, and agg.rpcServer.
//
// Exported so a future out-of-process consumer (e.g. a private gateway
// binary) can construct a Surface by importing this package, instead
// of every consumer rewriting the adapter glue.
func (agg *Aggregator) AvsClientSurface() avsclient.Surface {
	return avsclient.Surface{
		Operators:             &operatorPoolAdapter{pool: agg.operatorPool},
		SmartWalletRpc:        agg.smartWalletRpc,
		SmartWalletRpcByChain: agg.smartWalletRpcByChain,
		PriceService:          agg.priceService,
		WithdrawSvc:           &withdrawServiceAdapter{rpc: agg.rpcServer},
	}
}

// operatorPoolAdapter adapts the aggregator's *OperatorPool to the
// avsclient.OperatorLister interface. The adapter copies the fields
// consumers surface; the internal *OperatorNode keeps extra columns
// (RemoteIP, MetricsPort) that aren't part of the public shape.
type operatorPoolAdapter struct {
	pool *OperatorPool
}

func (a *operatorPoolAdapter) List() []avsclient.OperatorView {
	if a == nil || a.pool == nil {
		return nil
	}
	nodes := a.pool.GetAll()
	out := make([]avsclient.OperatorView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, avsclient.OperatorView{
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
// avsclient.WithdrawService interface. The full UserOp pipeline
// (bundler client, paymaster, balance + gas estimation, max-amount
// calc) stays inside the aggregator package — consumers just supply
// the authed user + request shape and get the hashes back.
type withdrawServiceAdapter struct {
	rpc *RpcServer
}

func (a *withdrawServiceAdapter) Withdraw(ctx context.Context, req avsclient.WithdrawRequest) (avsclient.WithdrawResult, error) {
	if a == nil || a.rpc == nil {
		return avsclient.WithdrawResult{}, fmt.Errorf("withdraw service not initialized")
	}
	if !common.IsHexAddress(req.Owner) {
		return avsclient.WithdrawResult{}, fmt.Errorf("owner is not a valid hex address")
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
		return avsclient.WithdrawResult{}, err
	}
	return avsclient.WithdrawResult{
		UserOpHash:      resp.GetUserOpHash(),
		TransactionHash: resp.GetTransactionHash(),
		Status:          resp.GetStatus(),
	}, nil
}

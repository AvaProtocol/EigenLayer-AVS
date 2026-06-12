package worker

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ethereumCallMsgFromProto maps a WorkerEstimateGasReq into the CallMsg
// shape ethclient.EstimateGas takes. From is optional (chain default
// applies when empty). Value defaults to 0.
func ethereumCallMsgFromProto(req *avsproto.WorkerEstimateGasReq, to common.Address) ethereum.CallMsg {
	msg := ethereum.CallMsg{
		To:   &to,
		Data: req.Data,
	}
	if req.From != "" {
		msg.From = common.HexToAddress(req.From)
	}
	if req.Value != "" {
		if v, ok := new(big.Int).SetString(req.Value, 10); ok {
			msg.Value = v
		}
	}
	return msg
}

type Server struct {
	avsproto.UnimplementedChainWorkerServer
	worker *Worker
}

func (s *Server) WorkerHealthCheck(ctx context.Context, req *avsproto.WorkerHealthCheckReq) (*avsproto.WorkerHealthCheckResp, error) {
	resp := &avsproto.WorkerHealthCheckResp{
		Status:    "OK",
		ChainId:   s.worker.config.ChainID,
		ChainName: s.worker.config.ChainName,
	}

	// Get latest block number from chain RPC
	blockNumber, err := s.worker.rpcClient.BlockNumber(ctx)
	if err != nil {
		s.worker.logger.Warn("Failed to get latest block number", "error", err)
		resp.Status = "DEGRADED"
	} else {
		resp.LatestBlock = int64(blockNumber)
	}

	return resp, nil
}

func (s *Server) ExecuteUserOp(ctx context.Context, req *avsproto.ExecuteUserOpReq) (*avsproto.ExecuteUserOpResp, error) {
	if s.worker.smartWalletCfg == nil {
		return &avsproto.ExecuteUserOpResp{
			Success: false,
			Error:   "smart wallet config not initialized",
		}, nil
	}

	ownerAddr := common.HexToAddress(req.Owner)

	var senderOverride *common.Address
	if req.SmartWalletAddress != "" {
		addr := common.HexToAddress(req.SmartWalletAddress)
		senderOverride = &addr
	}

	var paymasterReq *preset.VerifyingPaymasterRequest
	if req.UsePaymaster && s.worker.smartWalletCfg.PaymasterAddress != (common.Address{}) {
		paymasterReq = preset.GetVerifyingPaymasterRequestForDuration(
			s.worker.smartWalletCfg.PaymasterAddress,
			15*time.Minute,
		)
	}

	userOp, receipt, err := preset.SendUserOp(
		s.worker.smartWalletCfg,
		ownerAddr,
		req.CallData,
		paymasterReq,
		senderOverride,
		nil, // saltOverride: not exposed in ExecuteUserOpReq; sender override is used instead
		nil, // executionFeeWei: value-capture fee not yet wired through worker RPC
		s.worker.logger,
	)
	if err != nil {
		return &avsproto.ExecuteUserOpResp{
			Success: false,
			Error:   fmt.Sprintf("UserOp execution failed: %v", err),
		}, nil
	}

	resp := &avsproto.ExecuteUserOpResp{
		Success: true,
	}

	if userOp != nil {
		opHash := userOp.GetUserOpHash(
			s.worker.smartWalletCfg.EntrypointAddress,
			big.NewInt(s.worker.config.ChainID),
		)
		resp.UserOpHash = opHash.Hex()
	}

	if receipt != nil {
		resp.TxHash = receipt.TxHash.Hex()
		resp.GasUsed = receipt.GasUsed
		gasCost := new(big.Int).Mul(
			new(big.Int).SetUint64(receipt.GasUsed),
			receipt.EffectiveGasPrice,
		)
		resp.GasCostWei = gasCost.String()
	}

	return resp, nil
}

func (s *Server) GetNonce(ctx context.Context, req *avsproto.WorkerGetNonceReq) (*avsproto.WorkerGetNonceResp, error) {
	ownerAddr := common.HexToAddress(req.Owner)
	salt := big.NewInt(req.Salt)

	nonce, err := aa.GetNonce(s.worker.rpcClient, ownerAddr, salt)
	if err != nil {
		return nil, fmt.Errorf("getting nonce for %s: %w", req.Owner, err)
	}

	return &avsproto.WorkerGetNonceResp{
		Nonce: nonce.String(),
	}, nil
}

func (s *Server) GetSmartWalletAddress(ctx context.Context, req *avsproto.WorkerGetSmartWalletAddressReq) (*avsproto.WorkerGetSmartWalletAddressResp, error) {
	if s.worker.smartWalletCfg == nil {
		return nil, fmt.Errorf("smart wallet config not initialized")
	}

	ownerAddr := common.HexToAddress(req.Owner)
	salt := big.NewInt(req.Salt)

	addr, err := aa.GetSenderAddressForFactory(
		s.worker.rpcClient,
		ownerAddr,
		s.worker.smartWalletCfg.FactoryAddress,
		salt,
	)
	if err != nil {
		return nil, fmt.Errorf("getting smart wallet address for %s: %w", req.Owner, err)
	}

	return &avsproto.WorkerGetSmartWalletAddressResp{
		Address: addr.Hex(),
	}, nil
}

func (s *Server) GetTokenMetadata(ctx context.Context, req *avsproto.WorkerGetTokenMetadataReq) (*avsproto.WorkerGetTokenMetadataResp, error) {
	if s.worker.tokenService == nil {
		return &avsproto.WorkerGetTokenMetadataResp{
			Found: false,
		}, nil
	}

	metadata, err := s.worker.tokenService.GetTokenMetadata(req.ContractAddress)
	if err != nil {
		return &avsproto.WorkerGetTokenMetadataResp{
			Found: false,
		}, nil
	}

	return &avsproto.WorkerGetTokenMetadataResp{
		Name:     metadata.Name,
		Symbol:   metadata.Symbol,
		Decimals: metadata.Decimals,
		Found:    true,
		Source:   metadata.Source,
	}, nil
}

// GetNonceByAddress reads the EntryPoint nonce for an arbitrary smart
// wallet address. REST callers know the address from gateway storage
// and don't want to re-derive (owner, salt). The pre-existing GetNonce
// happens to do the same thing (its param is misnamed "owner" but is
// passed to EntryPoint.getNonce as the sender) — but exposing a clearly
// named alias keeps the gateway-side caller honest about what it's
// querying.
func (s *Server) GetNonceByAddress(ctx context.Context, req *avsproto.WorkerGetNonceByAddressReq) (*avsproto.WorkerGetNonceResp, error) {
	walletAddr := common.HexToAddress(req.WalletAddress)

	key := big.NewInt(0)
	if req.NonceKey != "" {
		parsed, ok := new(big.Int).SetString(req.NonceKey, 10)
		if !ok {
			return nil, fmt.Errorf("invalid nonce_key %q (expected base-10 big.Int string)", req.NonceKey)
		}
		key = parsed
	}

	nonce, err := aa.GetNonce(s.worker.rpcClient, walletAddr, key)
	if err != nil {
		return nil, fmt.Errorf("getting nonce for %s: %w", req.WalletAddress, err)
	}

	return &avsproto.WorkerGetNonceResp{
		Nonce: nonce.String(),
	}, nil
}

// SuggestGasPrice wraps ethclient.SuggestGasPrice for the chain this
// worker is bound to. Used by the gateway's fee estimator.
func (s *Server) SuggestGasPrice(ctx context.Context, req *avsproto.WorkerSuggestGasPriceReq) (*avsproto.WorkerSuggestGasPriceResp, error) {
	price, err := s.worker.rpcClient.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("SuggestGasPrice on chain %d: %w", s.worker.config.ChainID, err)
	}
	return &avsproto.WorkerSuggestGasPriceResp{
		GasPriceWei: price.String(),
	}, nil
}

// EstimateGas wraps ethclient.EstimateGas. Used by the fee estimator
// to budget UserOp executions before submitting to the bundler.
func (s *Server) EstimateGas(ctx context.Context, req *avsproto.WorkerEstimateGasReq) (*avsproto.WorkerEstimateGasResp, error) {
	to := common.HexToAddress(req.To)
	msg := ethereumCallMsgFromProto(req, to)

	gas, err := s.worker.rpcClient.EstimateGas(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("EstimateGas on chain %d to %s: %w", s.worker.config.ChainID, req.To, err)
	}
	return &avsproto.WorkerEstimateGasResp{
		Gas: gas,
	}, nil
}

// GetCode wraps ethclient.CodeAt(addr, nil). Used by the fee estimator
// to detect whether the runner contract is deployed before issuing a
// UserOp against it.
func (s *Server) GetCode(ctx context.Context, req *avsproto.WorkerGetCodeReq) (*avsproto.WorkerGetCodeResp, error) {
	addr := common.HexToAddress(req.Address)
	code, err := s.worker.rpcClient.CodeAt(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("CodeAt on chain %d for %s: %w", s.worker.config.ChainID, req.Address, err)
	}
	return &avsproto.WorkerGetCodeResp{
		Code: code,
	}, nil
}

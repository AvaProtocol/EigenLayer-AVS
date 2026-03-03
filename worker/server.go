package worker

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

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
			15*60, // 15 minutes in seconds
		)
	}

	userOp, receipt, err := preset.SendUserOp(
		s.worker.smartWalletCfg,
		ownerAddr,
		req.CallData,
		paymasterReq,
		senderOverride,
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

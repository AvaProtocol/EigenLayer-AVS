package worker

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ethereumCallMsgFromProto maps a WorkerEstimateGasReq into the CallMsg
// shape ethclient.EstimateGas takes. From is optional (chain default
// applies when empty). Value defaults to 0. The caller is responsible
// for validating req.To and supplying the parsed address as `to`; we
// pass `to` straight through as *Address so the contract-deployment
// case (To == nil) can be expressed by passing toPtr == nil.
func ethereumCallMsgFromProto(req *avsproto.WorkerEstimateGasReq, toPtr *common.Address) (ethereum.CallMsg, error) {
	msg := ethereum.CallMsg{
		To:   toPtr,
		Data: req.Data,
	}
	if req.From != "" {
		if !common.IsHexAddress(req.From) {
			return ethereum.CallMsg{}, fmt.Errorf("invalid from address %q", req.From)
		}
		msg.From = common.HexToAddress(req.From)
	}
	if req.Value != "" {
		v, ok := new(big.Int).SetString(req.Value, 10)
		if !ok {
			return ethereum.CallMsg{}, fmt.Errorf("invalid value %q (expected base-10 big.Int string)", req.Value)
		}
		msg.Value = v
	}
	return msg, nil
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
	if !common.IsHexAddress(req.Owner) {
		return nil, fmt.Errorf("invalid owner address %q", req.Owner)
	}

	ownerAddr := common.HexToAddress(req.Owner)

	// Empty salt defaults to 0 (proto3 omits empty strings; this matches
	// the gateway's nil-salt → "0" convention). CREATE2 salt is a uint256,
	// so reject negative or >256-bit values rather than letting them
	// overflow ABI encoding downstream.
	salt := big.NewInt(0)
	if req.Salt != "" {
		parsed, ok := new(big.Int).SetString(req.Salt, 10)
		if !ok {
			return nil, fmt.Errorf("invalid salt %q (expected base-10 big.Int string)", req.Salt)
		}
		salt = parsed
	}
	if salt.Sign() < 0 {
		return nil, fmt.Errorf("invalid salt %q: must be non-negative", req.Salt)
	}
	if salt.BitLen() > 256 {
		return nil, fmt.Errorf("invalid salt %q: exceeds uint256", req.Salt)
	}

	// Honor the caller's factory override when provided; otherwise use the
	// worker's configured factory. The gateway passes its per-chain /
	// per-request factory so worker-derived addresses match the gateway's
	// direct-RPC derivation exactly.
	factory := s.worker.smartWalletCfg.FactoryAddress
	if req.FactoryAddress != "" {
		if !common.IsHexAddress(req.FactoryAddress) {
			return nil, fmt.Errorf("invalid factory address %q", req.FactoryAddress)
		}
		factory = common.HexToAddress(req.FactoryAddress)
	}

	addr, err := aa.GetSenderAddressForFactory(
		s.worker.rpcClient,
		ownerAddr,
		factory,
		salt,
	)
	if err != nil {
		return nil, fmt.Errorf("getting smart wallet address for %s: %w", req.Owner, err)
	}
	if addr == nil {
		return nil, fmt.Errorf("nil sender address for owner=%s factory=%s salt=%s", req.Owner, factory.Hex(), salt.String())
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
	if !common.IsHexAddress(req.WalletAddress) {
		return nil, fmt.Errorf("invalid wallet_address %q", req.WalletAddress)
	}
	walletAddr := common.HexToAddress(req.WalletAddress)

	key := big.NewInt(0)
	if req.NonceKey != "" {
		parsed, ok := new(big.Int).SetString(req.NonceKey, 10)
		if !ok {
			return nil, fmt.Errorf("invalid nonce_key %q (expected base-10 big.Int string)", req.NonceKey)
		}
		// EntryPoint.getNonce takes a 192-bit key; negative values
		// underflow the contract's uint192 cast and would either
		// revert or read an unrelated nonce space. Reject up front.
		if parsed.Sign() < 0 {
			return nil, fmt.Errorf("invalid nonce_key %q (must be non-negative)", req.NonceKey)
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
//
// The gateway-side ChainStateReader wrap already includes the chain ID
// in its error message ("worker SuggestGasPrice (chain N): ..."), so
// we don't duplicate it here.
func (s *Server) SuggestGasPrice(ctx context.Context, req *avsproto.WorkerSuggestGasPriceReq) (*avsproto.WorkerSuggestGasPriceResp, error) {
	price, err := s.worker.rpcClient.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("SuggestGasPrice: %w", err)
	}
	return &avsproto.WorkerSuggestGasPriceResp{
		GasPriceWei: price.String(),
	}, nil
}

// EstimateGas wraps ethclient.EstimateGas. Used by the fee estimator
// to budget UserOp executions before submitting to the bundler.
//
// An empty req.To means "contract deployment" (ethclient.CallMsg.To ==
// nil); we forward that intent to ethclient rather than silently
// estimating against the zero address. Same for req.From: empty means
// "chain default sender", not "0x0...0".
func (s *Server) EstimateGas(ctx context.Context, req *avsproto.WorkerEstimateGasReq) (*avsproto.WorkerEstimateGasResp, error) {
	var toPtr *common.Address
	if req.To != "" {
		if !common.IsHexAddress(req.To) {
			return nil, fmt.Errorf("invalid to address %q", req.To)
		}
		addr := common.HexToAddress(req.To)
		toPtr = &addr
	}

	msg, err := ethereumCallMsgFromProto(req, toPtr)
	if err != nil {
		return nil, err
	}

	gas, err := s.worker.rpcClient.EstimateGas(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("EstimateGas to %s: %w", req.To, err)
	}
	return &avsproto.WorkerEstimateGasResp{
		Gas: gas,
	}, nil
}

// GetCode wraps ethclient.CodeAt(addr, nil). Used by the fee estimator
// to detect whether the runner contract is deployed before issuing a
// UserOp against it.
func (s *Server) GetCode(ctx context.Context, req *avsproto.WorkerGetCodeReq) (*avsproto.WorkerGetCodeResp, error) {
	if !common.IsHexAddress(req.Address) {
		return nil, fmt.Errorf("invalid address %q", req.Address)
	}
	addr := common.HexToAddress(req.Address)
	code, err := s.worker.rpcClient.CodeAt(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("CodeAt for %s: %w", req.Address, err)
	}
	return &avsproto.WorkerGetCodeResp{
		Code: code,
	}, nil
}

// CallContract wraps ethclient.CallContract (eth_call). Used by the
// contractRead node so the gateway issues no direct eth_call against
// execution chains.
func (s *Server) CallContract(ctx context.Context, req *avsproto.WorkerCallContractReq) (*avsproto.WorkerCallContractResp, error) {
	if !common.IsHexAddress(req.To) {
		return nil, fmt.Errorf("invalid to address %q", req.To)
	}
	to := common.HexToAddress(req.To)
	msg := ethereum.CallMsg{
		To:   &to,
		Data: req.Data,
	}
	if req.From != "" {
		if !common.IsHexAddress(req.From) {
			return nil, fmt.Errorf("invalid from address %q", req.From)
		}
		msg.From = common.HexToAddress(req.From)
	}
	if req.Value != "" {
		v, ok := new(big.Int).SetString(req.Value, 10)
		if !ok {
			return nil, fmt.Errorf("invalid value %q (expected base-10 big.Int string)", req.Value)
		}
		if v.Sign() < 0 {
			return nil, fmt.Errorf("invalid value %q: must be non-negative", req.Value)
		}
		if v.BitLen() > 256 {
			return nil, fmt.Errorf("invalid value %q: exceeds uint256", req.Value)
		}
		msg.Value = v
	}

	var blockNumber *big.Int
	if req.BlockNumber != "" {
		b, ok := new(big.Int).SetString(req.BlockNumber, 10)
		if !ok {
			return nil, fmt.Errorf("invalid block_number %q (expected base-10 big.Int string)", req.BlockNumber)
		}
		if b.Sign() < 0 {
			return nil, fmt.Errorf("invalid block_number %q: must be non-negative", req.BlockNumber)
		}
		blockNumber = b
	}

	result, err := s.worker.rpcClient.CallContract(ctx, msg, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("CallContract to %s: %w", req.To, err)
	}
	return &avsproto.WorkerCallContractResp{
		Result: result,
	}, nil
}

// GetBlockHeader wraps ethclient.HeaderByNumber, returning the number,
// hash, and timestamp the gateway reads (a full header can't be carried
// over gRPC faithfully). block_number empty = latest.
func (s *Server) GetBlockHeader(ctx context.Context, req *avsproto.WorkerGetBlockHeaderReq) (*avsproto.WorkerGetBlockHeaderResp, error) {
	var number *big.Int
	if req.BlockNumber != "" {
		b, ok := new(big.Int).SetString(req.BlockNumber, 10)
		if !ok {
			return nil, fmt.Errorf("invalid block_number %q (expected base-10 big.Int string)", req.BlockNumber)
		}
		if b.Sign() < 0 {
			return nil, fmt.Errorf("invalid block_number %q: must be non-negative", req.BlockNumber)
		}
		if !b.IsUint64() {
			return nil, fmt.Errorf("invalid block_number %q: exceeds uint64", req.BlockNumber)
		}
		number = b
	}
	header, err := s.worker.rpcClient.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, fmt.Errorf("HeaderByNumber: %w", err)
	}
	if header == nil {
		return nil, fmt.Errorf("HeaderByNumber returned nil header")
	}
	difficulty := "0"
	if header.Difficulty != nil {
		difficulty = header.Difficulty.String()
	}
	return &avsproto.WorkerGetBlockHeaderResp{
		Number:     header.Number.Uint64(),
		Hash:       header.Hash().Hex(),
		Time:       header.Time,
		ParentHash: header.ParentHash.Hex(),
		Difficulty: difficulty,
		GasLimit:   header.GasLimit,
		GasUsed:    header.GasUsed,
	}, nil
}

// GetBlockNumber wraps ethclient.BlockNumber (latest block).
func (s *Server) GetBlockNumber(ctx context.Context, req *avsproto.WorkerGetBlockNumberReq) (*avsproto.WorkerGetBlockNumberResp, error) {
	number, err := s.worker.rpcClient.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("BlockNumber: %w", err)
	}
	return &avsproto.WorkerGetBlockNumberResp{Number: number}, nil
}

// maxWalletSaltScan caps the salt range FindMatchingWalletSalt will scan, so a
// caller can't make the worker issue an unbounded number of factory calls.
const maxWalletSaltScan = 2000

// FindMatchingWalletSalt scans salts [0, max_salts) for the one whose CREATE2
// smart-wallet address (under factory_address, or the worker's configured
// factory) matches target_address. The loop + comparison run worker-side so a
// wide range is a single round-trip. Returns found=false when no salt matches.
func (s *Server) FindMatchingWalletSalt(ctx context.Context, req *avsproto.WorkerFindMatchingWalletSaltReq) (*avsproto.WorkerFindMatchingWalletSaltResp, error) {
	if s.worker.smartWalletCfg == nil {
		return nil, fmt.Errorf("smart wallet config not initialized")
	}
	if !common.IsHexAddress(req.Owner) {
		return nil, fmt.Errorf("invalid owner address %q", req.Owner)
	}
	if !common.IsHexAddress(req.TargetAddress) {
		return nil, fmt.Errorf("invalid target address %q", req.TargetAddress)
	}
	if req.MaxSalts <= 0 {
		return nil, fmt.Errorf("max_salts must be positive, got %d", req.MaxSalts)
	}
	if req.MaxSalts > maxWalletSaltScan {
		return nil, fmt.Errorf("max_salts %d exceeds cap %d", req.MaxSalts, maxWalletSaltScan)
	}

	factory := s.worker.smartWalletCfg.FactoryAddress
	if req.FactoryAddress != "" {
		if !common.IsHexAddress(req.FactoryAddress) {
			return nil, fmt.Errorf("invalid factory address %q", req.FactoryAddress)
		}
		factory = common.HexToAddress(req.FactoryAddress)
	}

	owner := common.HexToAddress(req.Owner)
	target := common.HexToAddress(req.TargetAddress)

	for salt := int64(0); salt < req.MaxSalts; salt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		addr, err := aa.GetSenderAddressForFactory(s.worker.rpcClient, owner, factory, big.NewInt(salt))
		if err != nil {
			// A single failed derivation shouldn't abort the whole scan.
			continue
		}
		if addr != nil && *addr == target {
			return &avsproto.WorkerFindMatchingWalletSaltResp{Found: true, Salt: salt}, nil
		}
	}
	return &avsproto.WorkerFindMatchingWalletSaltResp{Found: false}, nil
}

// GetTransactionReceipt wraps ethclient.TransactionReceipt. found=false (not
// an error) when the receipt isn't available yet (pending / unknown hash),
// so the gateway's confirmation-waiting loop can keep polling.
func (s *Server) GetTransactionReceipt(ctx context.Context, req *avsproto.WorkerGetTransactionReceiptReq) (*avsproto.WorkerGetTransactionReceiptResp, error) {
	// Validate the hash shape: common.HexToHash silently coerces a malformed
	// or empty value to the zero hash, which would then look like "pending"
	// (found=false) and mask a caller bug instead of surfacing it.
	if len(req.TxHash) != 66 || !strings.HasPrefix(req.TxHash, "0x") {
		return nil, fmt.Errorf("invalid tx_hash %q", req.TxHash)
	}
	receipt, err := s.worker.rpcClient.TransactionReceipt(ctx, common.HexToHash(req.TxHash))
	if errors.Is(err, ethereum.NotFound) {
		return &avsproto.WorkerGetTransactionReceiptResp{Found: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("TransactionReceipt for %s: %w", req.TxHash, err)
	}
	resp := &avsproto.WorkerGetTransactionReceiptResp{
		Found:   true,
		Status:  receipt.Status,
		GasUsed: receipt.GasUsed,
		TxHash:  receipt.TxHash.Hex(),
	}
	if receipt.EffectiveGasPrice != nil {
		resp.EffectiveGasPrice = receipt.EffectiveGasPrice.String()
	}
	if receipt.BlockNumber != nil {
		resp.BlockNumber = receipt.BlockNumber.Uint64()
	}
	return resp, nil
}

// GetStorageAt wraps ethclient.StorageAt (latest block). Used by the
// gateway's simulation balance-slot probing.
func (s *Server) GetStorageAt(ctx context.Context, req *avsproto.WorkerGetStorageAtReq) (*avsproto.WorkerGetStorageAtResp, error) {
	if !common.IsHexAddress(req.Address) {
		return nil, fmt.Errorf("invalid address %q", req.Address)
	}
	// A malformed slot would be silently coerced to slot 0 (a valid slot that
	// returns real data), which could let a wrong-slot probe pass ERC-20
	// balance-slot validation. Reject anything that isn't a 32-byte hex word.
	if len(req.Slot) != 66 || !strings.HasPrefix(req.Slot, "0x") {
		return nil, fmt.Errorf("invalid slot %q", req.Slot)
	}
	value, err := s.worker.rpcClient.StorageAt(ctx, common.HexToAddress(req.Address), common.HexToHash(req.Slot), nil)
	if err != nil {
		return nil, fmt.Errorf("StorageAt %s slot %s: %w", req.Address, req.Slot, err)
	}
	return &avsproto.WorkerGetStorageAtResp{Value: value}, nil
}

// GetBalance wraps ethclient.BalanceAt(addr, latest). Used by the gateway's
// withdraw preflight to validate / size native-coin withdrawals.
func (s *Server) GetBalance(ctx context.Context, req *avsproto.WorkerGetBalanceReq) (*avsproto.WorkerGetBalanceResp, error) {
	if !common.IsHexAddress(req.Address) {
		return nil, fmt.Errorf("invalid address %q", req.Address)
	}
	balance, err := s.worker.rpcClient.BalanceAt(ctx, common.HexToAddress(req.Address), nil)
	if err != nil {
		return nil, fmt.Errorf("BalanceAt for %s: %w", req.Address, err)
	}
	return &avsproto.WorkerGetBalanceResp{
		BalanceWei: balance.String(),
	}, nil
}

// GetTokenBalance reads an ERC-20 balance via erc20.BalanceOf. Used by the
// gateway's withdraw preflight to validate / size ERC-20 withdrawals. The
// returned balance is raw token units (no decimals applied).
func (s *Server) GetTokenBalance(ctx context.Context, req *avsproto.WorkerGetTokenBalanceReq) (*avsproto.WorkerGetTokenBalanceResp, error) {
	if !common.IsHexAddress(req.TokenAddress) {
		return nil, fmt.Errorf("invalid token address %q", req.TokenAddress)
	}
	if !common.IsHexAddress(req.OwnerAddress) {
		return nil, fmt.Errorf("invalid owner address %q", req.OwnerAddress)
	}
	token, err := erc20.NewErc20(common.HexToAddress(req.TokenAddress), s.worker.rpcClient)
	if err != nil {
		return nil, fmt.Errorf("erc20 binding for %s: %w", req.TokenAddress, err)
	}
	balance, err := token.BalanceOf(&bind.CallOpts{Context: ctx}, common.HexToAddress(req.OwnerAddress))
	if err != nil {
		return nil, fmt.Errorf("BalanceOf token=%s owner=%s: %w", req.TokenAddress, req.OwnerAddress, err)
	}
	return &avsproto.WorkerGetTokenBalanceResp{
		Balance: balance.String(),
	}, nil
}

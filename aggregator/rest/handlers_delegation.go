package rest

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/holiman/uint256"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
)

// POST /wallets/{address}/delegation:prepare
func (s *Server) PrepareEoaDelegation(ctx echo.Context, address generated.EthereumAddress) error {
	p, err := s.ensurePermission(ctx, OpPrepareEoaDelegation)
	if err != nil {
		return err
	}
	var body generated.PrepareEoaDelegationJSONBody
	if err := ctx.Bind(&body); err != nil && ctx.Request().ContentLength > 0 {
		return badRequest("DELEGATION_BAD_BODY", "Invalid request body", err.Error())
	}
	eoa, chainID, _, rpc, err := s.delegationPreamble(ctx, p.User.Address, address, body.ChainId)
	if err != nil {
		return err
	}
	nonce, err := rpc.PendingNonceAt(ctx.Request().Context(), eoa)
	if err != nil {
		return fmt.Errorf("reading EOA nonce: %w", err)
	}
	digest := setCodeDigest(chainID, config.SMA7702Delegate(), nonce)
	del := generated.EthereumAddress(config.SMA7702Delegate().Hex())
	return ctx.JSON(http.StatusOK, generated.PreparedDelegation{
		ChainId:  chainID,
		Delegate: del,
		Nonce:    int64(nonce),
		Digest:   generated.Hex(digest.Hex()),
	})
}

// POST /wallets/{address}/delegation:submit
func (s *Server) SubmitEoaDelegation(ctx echo.Context, address generated.EthereumAddress) error {
	p, err := s.ensurePermission(ctx, OpSubmitEoaDelegation)
	if err != nil {
		return err
	}
	var body generated.SubmitDelegationRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("DELEGATION_BAD_BODY", "Invalid request body", err.Error())
	}
	eoa, chainID, sw, rpc, err := s.delegationPreamble(ctx, p.User.Address, address, &body.ChainId)
	if err != nil {
		return err
	}
	if body.Nonce < 0 {
		return badRequest("DELEGATION_BAD_NONCE", "Invalid nonce", "nonce must be non-negative.")
	}
	auth, err := parseSetCodeAuth(chainID, config.SMA7702Delegate(), uint64(body.Nonce), string(body.Signature))
	if err != nil {
		return badRequest("DELEGATION_BAD_SIGNATURE", "Invalid 7702 authorization", err.Error())
	}
	authority, err := auth.Authority()
	if err != nil {
		return badRequest("DELEGATION_BAD_SIGNATURE", "Invalid 7702 authorization", err.Error())
	}
	if authority != eoa {
		return badRequest("DELEGATION_BAD_AUTHORITY", "Authorization is not from this EOA",
			fmt.Sprintf("recovered %s, path is %s", authority.Hex(), eoa.Hex()))
	}

	var statusCode int
	var payload generated.DelegationStatus
	err = s.engine.RunWithSessionAuthorityLock(chainID, eoa, eoa, func() error {
		reqCtx := ctx.Request().Context()
		if s.engine.AssertEOA7702Delegation(reqCtx, chainID, eoa) == nil {
			if err := s.engine.UpsertEOA7702Wallet(chainID, eoa); err != nil {
				return err
			}
			statusCode = http.StatusOK
			payload = s.delegationStatus(reqCtx, chainID, eoa, sw, rpc)
			return nil
		}
		current, nErr := rpc.PendingNonceAt(reqCtx, eoa)
		if nErr != nil {
			return fmt.Errorf("reading EOA nonce: %w", nErr)
		}
		if !eoaNonceIsCurrent(uint64(body.Nonce), current) {
			return badRequest("DELEGATION_STALE_NONCE", "Authorization nonce is not current",
				fmt.Sprintf("signed nonce %d, EOA pending nonce %d; call prepare again and re-sign. A stale nonce still spends gas if broadcast.", body.Nonce, current))
		}
		if err := s.broadcastSetCode(reqCtx, rpc, sw, auth, eoa); err != nil {
			return err
		}
		if s.engine.AssertEOA7702Delegation(reqCtx, chainID, eoa) == nil {
			if err := s.engine.UpsertEOA7702Wallet(chainID, eoa); err != nil {
				return err
			}
			statusCode = http.StatusOK
			payload = s.delegationStatus(reqCtx, chainID, eoa, sw, rpc)
			return nil
		}
		statusCode = http.StatusAccepted
		payload = s.delegationStatus(reqCtx, chainID, eoa, sw, rpc)
		payload.Status = generated.DelegationStatusStatusPending
		return nil
	})
	if err != nil {
		return mapDelegationError(err)
	}
	return ctx.JSON(statusCode, payload)
}

// GET /wallets/{address}/delegation
func (s *Server) GetEoaDelegation(ctx echo.Context, address generated.EthereumAddress, params generated.GetEoaDelegationParams) error {
	p, err := s.ensurePermission(ctx, OpGetEoaDelegation)
	if err != nil {
		return err
	}
	var explicit *int64
	if params.ChainId != nil {
		v := int64(*params.ChainId)
		explicit = &v
	}
	eoa, chainID, sw, rpc, err := s.delegationPreamble(ctx, p.User.Address, address, explicit)
	if err != nil {
		return err
	}
	reqCtx := ctx.Request().Context()
	var st generated.DelegationStatus
	err = s.engine.RunWithSessionAuthorityLock(chainID, eoa, eoa, func() error {
		st = s.delegationStatus(reqCtx, chainID, eoa, sw, rpc)
		if st.Status == generated.DelegationStatusStatusDelegated {
			return s.engine.UpsertEOA7702Wallet(chainID, eoa)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, st)
}

func (s *Server) delegationPreamble(ctx echo.Context, user common.Address, address generated.EthereumAddress, explicitChain *int64) (common.Address, int64, *config.SmartWalletConfig, *ethclient.Client, error) {
	if !common.IsHexAddress(string(address)) {
		return common.Address{}, 0, nil, nil, badRequest("DELEGATION_BAD_ADDRESS", "Invalid EOA", "path address must be a hex address.")
	}
	eoa := common.HexToAddress(string(address))
	if eoa != user {
		return common.Address{}, 0, nil, nil, &restmw.HTTPError{
			Status: http.StatusForbidden, Code: "DELEGATION_NOT_SELF",
			Title: "Can only delegate your own EOA", Detail: "path address must equal the authenticated EOA.",
		}
	}
	chainID := policyChainID(ctx, explicitChain)
	if err := config.Check7702AuthorizationChainID(big.NewInt(chainID)); err != nil {
		return common.Address{}, 0, nil, nil, badRequest("DELEGATION_BAD_CHAIN_ID", "Invalid chain", err.Error())
	}
	rpc, sw := s.resolveSmartWalletForChain(chainID)
	if sw == nil || !sw.HasSMA7702Pin() {
		return common.Address{}, 0, nil, nil, badRequest("DELEGATION_NO_PIN", "SMA-7702 pin missing",
			fmt.Sprintf("chain_id=%d has no sma_7702_delegate/sma_7702_impl_hash", chainID))
	}
	if !config.SMA7702FirstChain(chainID) {
		return common.Address{}, 0, nil, nil, badRequest("DELEGATION_CHAIN_NOT_FIRST", "Chain not in Track B first set",
			fmt.Sprintf("first production chains are Sepolia (%d) and Base (%d)", config.SMA7702ChainSepolia, config.SMA7702ChainBase))
	}
	if rpc == nil {
		return common.Address{}, 0, nil, nil, &restmw.HTTPError{
			Status: http.StatusServiceUnavailable, Code: "DELEGATION_NO_RPC",
			Title: "Chain RPC unavailable", Detail: "Cannot read or broadcast 7702 without a chain client.",
		}
	}
	if taskengine.GetChainStateReaderForChain(uint64(chainID)) == nil {
		return common.Address{}, 0, nil, nil, &restmw.HTTPError{
			Status: http.StatusServiceUnavailable, Code: "DELEGATION_NO_READER",
			Title:  "Chain state reader unavailable",
			Detail: fmt.Sprintf("chain_id=%d has an RPC but no registered state reader; K13 cannot run and a 202 would never resolve.", chainID),
		}
	}
	return eoa, chainID, sw, rpc, nil
}

func (s *Server) delegationStatus(ctx context.Context, chainID int64, eoa common.Address, sw *config.SmartWalletConfig, rpc *ethclient.Client) generated.DelegationStatus {
	out := generated.DelegationStatus{Status: generated.DelegationStatusStatusMissing, ChainId: &chainID}
	del := generated.EthereumAddress(config.SMA7702Delegate().Hex())
	out.Delegate = &del
	if err := s.engine.AssertEOA7702Delegation(ctx, chainID, eoa); err != nil {
		return out
	}
	out.Status = generated.DelegationStatusStatusDelegated
	if rpc != nil && sw != nil {
		if impl, err := rpc.CodeAt(ctx, sw.SMA7702Delegate, nil); err == nil {
			h := crypto.Keccak256Hash(impl).Hex()
			hh := generated.Hex(h)
			out.CodeHash = &hh
		}
	}
	return out
}

func eoaNonceIsCurrent(signed, pending uint64) bool {
	return signed == pending
}

func setCodeDigest(chainID int64, delegate common.Address, nonce uint64) common.Hash {
	auth := types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(big.NewInt(chainID)),
		Address: delegate,
		Nonce:   nonce,
	}
	return auth.SigHash()
}

func parseSetCodeAuth(chainID int64, delegate common.Address, nonce uint64, sigHex string) (types.SetCodeAuthorization, error) {
	sig := common.FromHex(sigHex)
	if len(sig) != 65 {
		return types.SetCodeAuthorization{}, fmt.Errorf("signature is %d bytes, want 65", len(sig))
	}
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	return types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(big.NewInt(chainID)),
		Address: delegate,
		Nonce:   nonce,
		V:       v,
		R:       *uint256.MustFromBig(new(big.Int).SetBytes(sig[0:32])),
		S:       *uint256.MustFromBig(new(big.Int).SetBytes(sig[32:64])),
	}, nil
}

func (s *Server) broadcastSetCode(ctx context.Context, rpc *ethclient.Client, sw *config.SmartWalletConfig, auth types.SetCodeAuthorization, eoa common.Address) error {
	if sw == nil || sw.ControllerPrivateKey == nil {
		return &restmw.HTTPError{Status: http.StatusServiceUnavailable, Code: "DELEGATION_NO_SIGNER",
			Title:  "Controller signer unavailable",
			Detail: "type-4 broadcast uses the per-chain controller_private_key (funded on this chain), not the AVS identity key."}
	}
	from := crypto.PubkeyToAddress(sw.ControllerPrivateKey.PublicKey)
	nonce, err := rpc.PendingNonceAt(ctx, from)
	if err != nil {
		return fmt.Errorf("controller nonce: %w", err)
	}
	tip, err := rpc.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	head, err := rpc.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	fee := eip1559.MaxFeeFromTipAndBase(tip, head.BaseFee)
	chainID := sw.ChainID
	if chainID == 0 {
		id, idErr := rpc.ChainID(ctx)
		if idErr != nil {
			return idErr
		}
		chainID = id.Int64()
	}
	inner := &types.SetCodeTx{
		ChainID:   uint256.MustFromBig(big.NewInt(chainID)),
		Nonce:     nonce,
		GasTipCap: uint256.MustFromBig(tip),
		GasFeeCap: uint256.MustFromBig(fee),
		Gas:       150_000,
		To:        eoa,
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{auth},
	}
	signed, err := types.SignNewTx(sw.ControllerPrivateKey, types.LatestSignerForChainID(big.NewInt(chainID)), inner)
	if err != nil {
		return err
	}
	if err := rpc.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("broadcast type-4: %w", err)
	}
	if _, err := waitMinedIgnoreStatus(ctx, rpc, signed.Hash()); err != nil {
		return err
	}
	return nil
}

const delegationMineWait = 30 * time.Second

func waitMinedIgnoreStatus(ctx context.Context, chain *ethclient.Client, hash common.Hash) (*types.Receipt, error) {
	deadline := time.Now().Add(delegationMineWait)
	for time.Now().Before(deadline) {
		receipt, err := chain.TransactionReceipt(ctx, hash)
		if err == nil {
			return receipt, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return nil, nil
}

func mapDelegationError(err error) error {
	if errors.Is(err, taskengine.ErrEOADelegationMissing) {
		return &restmw.HTTPError{Status: http.StatusConflict, Code: "EOA_DELEGATION_MISSING",
			Title: "EOA is not delegated to SMA-7702", Detail: err.Error()}
	}
	return err
}

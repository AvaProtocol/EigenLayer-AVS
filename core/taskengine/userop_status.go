package taskengine

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	"github.com/ethereum/go-ethereum/common"
)

// UserOpStatus is the typed lookup result for GET /userops/{userOpHash}.
// Pending is not failed.
type UserOpStatus struct {
	UserOpHash      string
	Sender          string
	ExecutionStatus string
	TransactionHash string
	BlockNumber     string
	Success         *bool
	Calls           []InnerCall
	FailedCall      *InnerCall
}

// ErrUserOpNotFound is returned for unknown hashes and for hashes whose
// sender is not one of the caller's smart wallets (same 404 so this is
// not a public AA explorer).
var ErrUserOpNotFound = fmt.Errorf("user operation not found")

// ErrUserOpHashInvalid is a 400: the path param is not a 32-byte hex hash.
var ErrUserOpHashInvalid = fmt.Errorf("invalid userOpHash")

type userOpBundler interface {
	GetUserOperationByHash(ctx context.Context, hash string) (interface{}, error)
	GetUserOperationReceipt(ctx context.Context, hash string) (interface{}, error)
	Close()
}

var newUserOpBundler = func(url string) (userOpBundler, error) {
	return bundler.NewBundlerClient(url)
}

// LookupUserOpStatus re-polls a UserOp this user submitted. Same gate as
// nodes:run: the caller is a concrete user; the sender must be one of
// their smart wallets. Internally: bundler GetUserOperationByHash +
// GetUserOperationReceipt, then UnpackExecuteCalldata on the UserOp
// callData.
func (n *Engine) LookupUserOpStatus(ctx context.Context, user *model.User, userOpHash string, chainID int64) (*UserOpStatus, error) {
	normalized, err := normalizeUserOpHash(userOpHash)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserOpNotFound
	}

	if chainID <= 0 && user.ChainID > 0 {
		chainID = user.ChainID
	}
	sw := n.ResolveSmartWalletConfig(chainID)
	if sw == nil {
		return nil, fmt.Errorf("smart wallet config is not available for chain %d", chainID)
	}
	bundlerURL, err := sw.ActiveBundlerURL()
	if err != nil {
		return nil, fmt.Errorf("resolve bundler endpoint: %w", err)
	}
	client, err := newUserOpBundler(bundlerURL)
	if err != nil {
		return nil, fmt.Errorf("bundler client: %w", err)
	}
	defer client.Close()

	rawOp, err := client.GetUserOperationByHash(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("eth_getUserOperationByHash: %w", err)
	}
	sender, callData, txFromOp, found := parseBundlerUserOp(rawOp)
	if !found || (sender == (common.Address{})) {
		return nil, ErrUserOpNotFound
	}

	owns, err := n.userOwnsWalletOnAnyChain(user, sender)
	if err != nil {
		return nil, err
	}
	if !owns {
		return nil, ErrUserOpNotFound
	}

	out := &UserOpStatus{
		UserOpHash:      normalized,
		Sender:          sender.Hex(),
		ExecutionStatus: "pending",
		TransactionHash: txFromOp,
	}
	if calls, unpackErr := InnerCallsFromExecuteCalldata(callData); unpackErr == nil {
		out.Calls = calls
	}

	rawReceipt, recErr := client.GetUserOperationReceipt(ctx, normalized)
	if recErr != nil {
		// Receipt lookup failing while the op itself exists is still pending
		// (bundler lag / method not implemented). Do not convert that to failed.
		return out, nil
	}
	success, txHash, blockNumber := parseBundlerReceipt(rawReceipt)
	if txHash == "" && txFromOp != "" {
		txHash = txFromOp
	}
	if success == nil && txHash == "" && blockNumber == "" {
		// Bundler returned a null receipt: still pending.
		return out, nil
	}
	out.TransactionHash = txHash
	out.BlockNumber = blockNumber
	out.Success = success
	switch {
	case success != nil && *success:
		out.ExecutionStatus = "confirmed"
	case success != nil && !*success:
		out.ExecutionStatus = "failed"
		if len(out.Calls) == 1 {
			failed := out.Calls[0]
			out.FailedCall = &failed
		}
	default:
		if txHash != "" {
			out.ExecutionStatus = "confirmed"
		}
	}
	return out, nil
}

func normalizeUserOpHash(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	if len(s) != 64 {
		return "", ErrUserOpHashInvalid
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return "", ErrUserOpHashInvalid
	}
	return common.BytesToHash(b).Hex(), nil
}

func asStringMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func mapString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func parseHexBytes(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" || s == "0x" || s == "0X" {
		return nil
	}
	return common.FromHex(s)
}

// parseBundlerUserOp accepts both the ERC-4337 object-with-userOperation
// envelope and a flat UserOp map. Returns found=false on nil/empty.
func parseBundlerUserOp(raw interface{}) (sender common.Address, callData []byte, txHash string, found bool) {
	m := asStringMap(raw)
	if m == nil {
		return common.Address{}, nil, "", false
	}
	inner := asStringMap(m["userOperation"])
	src := inner
	if src == nil {
		src = m
	}
	senderStr := mapString(src, "sender")
	if senderStr == "" {
		return common.Address{}, nil, "", false
	}
	if !common.IsHexAddress(senderStr) {
		return common.Address{}, nil, "", false
	}
	sender = common.HexToAddress(senderStr)
	callData = parseHexBytes(mapString(src, "callData"))
	txHash = mapString(m, "transactionHash")
	if txHash == "" {
		txHash = mapString(src, "transactionHash")
	}
	return sender, callData, txHash, true
}

func parseBundlerReceipt(raw interface{}) (success *bool, txHash string, blockNumber string) {
	m := asStringMap(raw)
	if m == nil {
		return nil, "", ""
	}
	if v, ok := m["success"].(bool); ok {
		success = &v
	}
	txHash = mapString(m, "transactionHash")
	blockNumber = stringifyBlockNumber(m["blockNumber"])
	if receipt := asStringMap(m["receipt"]); receipt != nil {
		if txHash == "" {
			txHash = mapString(receipt, "transactionHash")
		}
		if blockNumber == "" {
			blockNumber = stringifyBlockNumber(receipt["blockNumber"])
		}
	}
	return success, txHash, blockNumber
}

func stringifyBlockNumber(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("0x%x", uint64(t))
	case int64:
		return fmt.Sprintf("0x%x", uint64(t))
	default:
		return ""
	}
}

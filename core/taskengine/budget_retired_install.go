package taskengine

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Only the canonical EntryPoint's handleOps format is recovered. Unsupported
// bundler wrappers, pruned history, ambiguous installs and old cached candidates
// all use the explicit empty-hook fallback, never guessed hook data.
var budgetHistoricalEntryPointABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"handleOps","inputs":[{"name":"ops","type":"tuple[]","components":[{"name":"sender","type":"address"},{"name":"nonce","type":"uint256"},{"name":"initCode","type":"bytes"},{"name":"callData","type":"bytes"},{"name":"accountGasLimits","type":"bytes32"},{"name":"preVerificationGas","type":"uint256"},{"name":"gasFees","type":"bytes32"},{"name":"paymasterAndData","type":"bytes"},{"name":"signature","type":"bytes"}]},{"name":"beneficiary","type":"address"}]}]`))
	if err != nil {
		panic(err) // Static ABI: a programming error.
	}
	return parsed
}()

type budgetHistoricalUserOp struct {
	Sender             common.Address
	Nonce              *big.Int
	InitCode           []byte
	CallData           []byte
	AccountGasLimits   [32]byte
	PreVerificationGas *big.Int
	GasFees            [32]byte
	PaymasterAndData   []byte
	Signature          []byte
}

func (v *pinnedBudgetView) recoverRetiredBudgetInstall(ctx context.Context, wallet common.Address, candidate budgetAuthorityCandidate, signer common.Address) ([]byte, error) {
	source := candidate.Install
	if source == nil {
		return nil, nil
	}
	tx, pending, err := v.chain.TransactionByHash(ctx, source.Transaction)
	if err != nil {
		return nil, fmt.Errorf("historical install transaction unavailable: %w", err)
	}
	if pending || tx.Hash() != source.Transaction {
		return nil, fmt.Errorf("historical install transaction is pending or mismatched")
	}
	receipt, err := v.chain.TransactionReceipt(ctx, source.Transaction)
	if err != nil {
		return nil, fmt.Errorf("historical install receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.BlockNumber.Uint64() != source.Block.Number || receipt.BlockHash != source.Block.Hash || source.Block.Number > v.block.Number {
		return nil, fmt.Errorf("historical install receipt does not match inventory")
	}
	// One matching event, at precisely the cached position. Multiple installs
	// of the same entity in a transaction are deliberately not reconstructed.
	matches := 0
	for _, log := range receipt.Logs {
		if log == nil || log.Removed || log.Address != wallet || len(log.Topics) != 3 || log.Topics[0] != budgetValidationInstalled || log.Topics[1] != common.BytesToHash(candidate.Module.Bytes()) || log.Topics[2] != common.BigToHash(new(big.Int).SetUint64(uint64(candidate.Entity))) {
			continue
		}
		if log.Index != source.LogIndex || log.BlockHash != source.Block.Hash || log.TxHash != source.Transaction {
			return nil, fmt.Errorf("ambiguous historical install event")
		}
		matches++
	}
	if matches != 1 {
		return nil, fmt.Errorf("historical install event missing or ambiguous")
	}
	return retiredBudgetInstallInTransaction(tx, wallet, candidate.Entity, signer)
}

func retiredBudgetInstallInTransaction(tx *types.Transaction, wallet common.Address, entity uint32, signer common.Address) ([]byte, error) {
	if tx.To() == nil {
		return nil, nil
	}
	if *tx.To() == wallet {
		return aa.HistoricalSessionSignerInstall(wallet, entity, signer, tx.Data())
	}
	method := budgetHistoricalEntryPointABI.Methods["handleOps"]
	if *tx.To() != common.HexToAddress(config.EntryPointV07AddressHex) || len(tx.Data()) < 4 || !bytes.Equal(tx.Data()[:4], method.ID) {
		return nil, nil
	}
	args, err := method.Inputs.Unpack(tx.Data()[4:])
	if err != nil {
		return nil, err
	}
	ops := *abi.ConvertType(args[0], new([]budgetHistoricalUserOp)).(*[]budgetHistoricalUserOp)
	var found []byte
	for _, op := range ops {
		if op.Sender != wallet {
			continue
		}
		calls := [][]byte{op.CallData}
		if new(big.Int).Rsh(new(big.Int).Set(op.Nonce), 64).Uint64()&userop.ValidationOptionDeferredAction != 0 {
			if len(op.Signature) < 4 {
				return nil, fmt.Errorf("missing historical deferred signature")
			}
			length := uint64(binary.BigEndian.Uint32(op.Signature[:4]))
			if length < 31 || length > uint64(len(op.Signature)-4) {
				return nil, fmt.Errorf("invalid historical deferred length")
			}
			calls = append(calls, op.Signature[4+21+6:4+length])
		}
		for _, call := range calls {
			install, err := aa.HistoricalSessionSignerInstall(wallet, entity, signer, call)
			if err != nil {
				return nil, err
			}
			if install != nil {
				if found != nil {
					return nil, fmt.Errorf("multiple matching historical operation installs")
				}
				found = install
			}
		}
	}
	return found, nil
}

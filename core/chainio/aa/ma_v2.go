package aa

import (
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Execution calldata encoding for Alchemy Modular Account v2.
//
// How this differs from the v0.6 SimpleAccount encoding in aa.go, verified
// against the deployed SemiModularAccountBytecode implementation
// (0x000000000000c5A9089039570Dd36455b5C07383):
//
//	execute(address,uint256,bytes)                      0xb61d27f6  present — unchanged
//	executeBatch((address,uint256,bytes)[])             0x34fcd5be  present — MA v2 form
//	executeBatch(address[],bytes[])                     0x18dfb3c7  ABSENT  — v0.6 form
//	executeBatchWithValues(address[],uint256[],bytes[]) 0xc3ff72fc  ABSENT  — our fork's
//
// Two consequences worth stating plainly:
//
//   - `execute` carries over byte-for-byte. Single-call encoding needs no
//     migration at all.
//   - `executeBatchWithValues` was a function we added to our SimpleAccount
//     fork so a batch could carry a per-call ETH value. MA v2's `executeBatch`
//     does that natively, so the fork function has no successor and needs
//     none. This is what retires the sponsor-then-reimburse-from-wallet trick
//     (avs-infra Smart_Wallet_MA_v2_Spend_Policy.md §3.2).
const (
	SelectorExecuteMAv2      = "0xb61d27f6"
	SelectorExecuteBatchMAv2 = "0x34fcd5be"
)

// Call is one entry in an MA v2 batch. Field order and names must match the
// on-chain tuple — the ABI encoder maps by the `abi` tag, and a reordering
// here would produce calldata that decodes to different arguments rather than
// failing loudly.
type Call struct {
	Target common.Address `abi:"target"`
	Value  *big.Int       `abi:"value"`
	Data   []byte         `abi:"data"`
}

const modularAccountABIJSON = `[
  {"type":"function","name":"execute","stateMutability":"payable",
   "inputs":[{"name":"target","type":"address"},{"name":"value","type":"uint256"},{"name":"data","type":"bytes"}],
   "outputs":[{"name":"result","type":"bytes"}]},
  {"type":"function","name":"executeBatch","stateMutability":"payable",
   "inputs":[{"name":"calls","type":"tuple[]","components":[
     {"name":"target","type":"address"},{"name":"value","type":"uint256"},{"name":"data","type":"bytes"}]}],
   "outputs":[{"name":"results","type":"bytes[]"}]}
]`

var (
	modularAccountABIOnce sync.Once
	modularAccountABI     abi.ABI
	modularAccountABIErr  error
)

func ensureModularAccountABI() (abi.ABI, error) {
	modularAccountABIOnce.Do(func() {
		modularAccountABI, modularAccountABIErr = abi.JSON(strings.NewReader(modularAccountABIJSON))
	})
	return modularAccountABI, modularAccountABIErr
}

// PackExecuteMAv2 encodes a single call. Identical on the wire to the v0.6
// PackExecute; kept as its own symbol so call sites read unambiguously and so
// the v0.6 helper can be deleted at cutover without touching these.
func PackExecuteMAv2(target common.Address, value *big.Int, data []byte) ([]byte, error) {
	parsed, err := ensureModularAccountABI()
	if err != nil {
		return nil, err
	}
	if value == nil {
		value = big.NewInt(0)
	}
	if data == nil {
		data = []byte{}
	}
	return parsed.Pack("execute", target, value, data)
}

// PackExecuteBatchMAv2 encodes an atomic batch. Every call carries its own ETH
// value, which is what makes the v0.6 executeBatchWithValues workaround
// unnecessary.
//
// A nil Value or Data is normalised to zero/empty rather than rejected: the
// common batch entry is a contract call with no ETH attached, and forcing
// callers to spell that out invites big.NewInt(0) boilerplate at every site.
// An empty batch IS rejected — it encodes fine and burns gas doing nothing,
// which is never what the caller meant.
func PackExecuteBatchMAv2(calls []Call) ([]byte, error) {
	parsed, err := ensureModularAccountABI()
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, fmt.Errorf("executeBatch requires at least one call")
	}
	normalised := make([]Call, len(calls))
	for i, c := range calls {
		if c.Value == nil {
			c.Value = big.NewInt(0)
		}
		if c.Data == nil {
			c.Data = []byte{}
		}
		normalised[i] = c
	}
	return parsed.Pack("executeBatch", normalised)
}

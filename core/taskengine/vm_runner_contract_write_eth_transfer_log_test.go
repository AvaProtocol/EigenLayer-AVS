package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const erc20TransferTopic0 = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

const transferEventABI = `[{
	"anonymous": false,
	"inputs": [
		{"indexed": true, "name": "from", "type": "address"},
		{"indexed": true, "name": "to", "type": "address"},
		{"indexed": false, "name": "value", "type": "uint256"}
	],
	"name": "Transfer",
	"type": "event"
}]`

func testContractWriteProcessor() *ContractWriteProcessor {
	return &ContractWriteProcessor{
		CommonProcessor: &CommonProcessor{
			vm: &VM{logger: &MockLogger{}},
		},
	}
}

func mustTransferABI(t *testing.T) *abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(transferEventABI))
	require.NoError(t, err)
	return &parsed
}

func addressTopic(addr string) string {
	return common.BytesToHash(common.HexToAddress(addr).Bytes()).Hex()
}

func uint256Word(n *big.Int) string {
	return "0x" + common.Bytes2Hex(common.LeftPadBytes(n.Bytes(), 32))
}

func erc20TransferLog(emitter, from, to string, value *big.Int) map[string]interface{} {
	return map[string]interface{}{
		"address": emitter,
		"topics": []interface{}{
			erc20TransferTopic0,
			addressTopic(from),
			addressTopic(to),
		},
		"data": uint256Word(value),
	}
}

func TestDecodeRelevantMethodEvents_KeepsTokenTransfer(t *testing.T) {
	const (
		usdc       = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner     = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		recipient  = "0x633791DdC147B59345dc56D3A1F0606c23703154"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		// Mixed-case form of the EIP-7708 system address.
		system = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	payment := big.NewInt(100000)
	gasRefund := big.NewInt(584351393496208)

	processor := testContractWriteProcessor()
	parsedABI := mustTransferABI(t)

	t.Run("token transfer followed by the bundler refund", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(usdc, runner, recipient, payment),
			erc20TransferLog(system, entryPoint, bundler, gasRefund),
		}

		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI)

		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, common.HexToAddress(recipient).Hex(), events["to"])
		assert.Equal(t, payment.String(), events["value"])
		assert.NotEqual(t, common.HexToAddress(entryPoint).Hex(), events["from"])
		assert.NotEqual(t, common.HexToAddress(bundler).Hex(), events["to"])
		assert.NotEqual(t, gasRefund.String(), events["value"])
	})

	t.Run("refund alone is not a payment", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(system, entryPoint, bundler, gasRefund),
		}

		// Even if the method target were the system address, the refund is not the step's transfer.
		events := processor.decodeRelevantMethodEvents(logs, ethTransferLogAddress.Hex(), "transfer", parsedABI)

		assert.Empty(t, events)
	})
}

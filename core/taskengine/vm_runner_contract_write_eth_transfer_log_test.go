package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
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

	t.Run("refund before the token transfer", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(system, entryPoint, bundler, gasRefund),
			erc20TransferLog(usdc, runner, recipient, payment),
		}

		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI)

		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, common.HexToAddress(recipient).Hex(), events["to"])
		assert.Equal(t, payment.String(), events["value"])
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

func nestUnderRaw(log map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	for _, key := range []string{"address", "topics", "data"} {
		if value, ok := log[key]; ok {
			raw[key] = value
		}
	}
	return map[string]interface{}{"raw": raw}
}

func mustABI(t *testing.T, jsonABI string) *abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(jsonABI))
	require.NoError(t, err)
	return &parsed
}

func abiWords(vals ...*big.Int) string {
	buf := make([]byte, 32*len(vals))
	for i, v := range vals {
		if v == nil {
			continue
		}
		copy(buf[i*32:(i+1)*32], common.LeftPadBytes(v.Bytes(), 32))
	}
	return "0x" + common.Bytes2Hex(buf)
}

const routerSwapABI = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"sender","type":"address"},
		{"indexed":true,"name":"recipient","type":"address"},
		{"indexed":false,"name":"amount0","type":"int256"},
		{"indexed":false,"name":"amount1","type":"int256"},
		{"indexed":false,"name":"sqrtPriceX96","type":"uint160"},
		{"indexed":false,"name":"liquidity","type":"uint128"},
		{"indexed":false,"name":"tick","type":"int24"}
	],"name":"Swap","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"from","type":"address"},
		{"indexed":true,"name":"to","type":"address"},
		{"indexed":false,"name":"value","type":"uint256"}
	],"name":"Transfer","type":"event"}
]`

func TestDecodeRelevantMethodEvents_SwapAfterTransferStillMerges(t *testing.T) {
	const (
		router     = "0x3333333333333333333333333333333333333333"
		pool       = "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50"
		token      = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner     = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		system     = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	parsedABI := mustABI(t, routerSwapABI)
	swapTopic := parsedABI.Events["Swap"].ID.Hex()
	require.Equal(t, "0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", swapTopic)

	amount0 := big.NewInt(12345)
	payment := big.NewInt(100000)
	processor := testContractWriteProcessor()
	swapLog := map[string]interface{}{
		"address": pool,
		"topics": []interface{}{
			swapTopic,
			addressTopic(router),
			addressTopic(runner),
		},
		"data": abiWords(amount0, big.NewInt(0), big.NewInt(0), big.NewInt(0), big.NewInt(0)),
	}
	tokenLog := erc20TransferLog(token, runner, router, payment)
	refundLog := erc20TransferLog(system, entryPoint, bundler, big.NewInt(584351393496208))

	t.Run("pool swap after the token transfer still merges", func(t *testing.T) {
		logs := []interface{}{tokenLog, swapLog, refundLog}
		events := processor.decodeRelevantMethodEvents(logs, router, "exactInputSingle", parsedABI)
		assert.Equal(t, amount0.String(), events["amount0"])
		assert.Equal(t, payment.String(), events["value"])
		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
	})

	t.Run("raw pool swap after a raw token transfer is still merged", func(t *testing.T) {
		logs := []interface{}{nestUnderRaw(tokenLog), nestUnderRaw(swapLog), nestUnderRaw(refundLog)}
		events := processor.decodeRelevantMethodEvents(logs, router, "exactInputSingle", parsedABI)
		assert.Equal(t, amount0.String(), events["amount0"])
		assert.Equal(t, payment.String(), events["value"])
		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
	})
}

func TestFlattenDecodedEvent_UnwrapsOrDropsEventName(t *testing.T) {
	wrapped := map[string]interface{}{
		"Transfer":  map[string]interface{}{"from": "0xabc", "value": "1"},
		"eventName": "Transfer",
	}
	flat := flattenDecodedEvent(wrapped, "Transfer")
	assert.Equal(t, "0xabc", flat["from"])
	_, hasName := flat["eventName"]
	assert.False(t, hasName)

	unwrapped := map[string]interface{}{
		"from":      "0xdef",
		"value":     "2",
		"eventName": "Transfer",
	}
	flat = flattenDecodedEvent(unwrapped, "Transfer")
	assert.Equal(t, "0xdef", flat["from"])
	_, hasName = flat["eventName"]
	assert.False(t, hasName)

	notAMap := map[string]interface{}{
		"Transfer":  "nope",
		"from":      "0x123",
		"eventName": "Transfer",
	}
	flat = flattenDecodedEvent(notAMap, "Transfer")
	assert.Equal(t, "nope", flat["Transfer"])
	assert.Equal(t, "0x123", flat["from"])
	_, hasName = flat["eventName"]
	assert.False(t, hasName)
}

func TestDecodeRelevantMethodEvents_SkipsSelfdestructTopicFromSystemAddress(t *testing.T) {
	const selfdestructABI = `[{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "name": "beneficiary", "type": "address"},
			{"indexed": false, "name": "balance", "type": "uint256"}
		],
		"name": "Selfdestruct",
		"type": "event"
	}]`
	const entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
	parsedABI := mustABI(t, selfdestructABI)
	require.Equal(t,
		"0x4bfaba3443c1a1836cd362418edc679fc96cae8449cbefccb6457cdf2c943083",
		parsedABI.Events["Selfdestruct"].ID.Hex(),
	)
	burn := map[string]interface{}{
		"address": ethTransferLogAddress.Hex(),
		"topics": []interface{}{
			parsedABI.Events["Selfdestruct"].ID.Hex(),
			addressTopic(entryPoint),
		},
		"data": uint256Word(big.NewInt(1)),
	}
	processor := testContractWriteProcessor()
	events := processor.decodeRelevantMethodEvents(
		[]interface{}{burn},
		ethTransferLogAddress.Hex(),
		"execute",
		parsedABI,
	)
	assert.Empty(t, events)
}

func TestFlattenReceiptLog_SkipsSystemAddress(t *testing.T) {
	const (
		usdc       = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner     = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		recipient  = "0x633791DdC147B59345dc56D3A1F0606c23703154"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		system     = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	payment := big.NewInt(100000)
	handler := &ContractWriteOutputHandler{engine: &Engine{}}
	parsedABI := mustTransferABI(t)

	refund := handler.flattenReceiptLog(1, erc20TransferLog(system, entryPoint, bundler, big.NewInt(99)), parsedABI)
	assert.Empty(t, refund)

	fields := handler.flattenReceiptLog(1, nestUnderRaw(erc20TransferLog(usdc, runner, recipient, payment)), parsedABI)
	inner, ok := fields["Transfer"].(map[string]interface{})
	require.True(t, ok, "handler keeps the event wrapper when the parser omits eventName: %#v", fields)
	assert.Equal(t, payment.String(), inner["value"])
	assert.Equal(t, common.HexToAddress(runner).Hex(), inner["from"])
}

func TestExtractResolvedContractAddressFromMetadata_SkipsEthTransferLog(t *testing.T) {
	const (
		usdc   = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		system = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
		router = "0x3333333333333333333333333333333333333333"
	)
	withLogs := func(to string, logs []interface{}) []interface{} {
		receipt := map[string]interface{}{"logs": logs}
		if to != "" {
			receipt["to"] = to
		}
		return []interface{}{map[string]interface{}{"receipt": receipt}}
	}

	assert.Equal(t, usdc, extractResolvedContractAddressFromMetadata(withLogs("", []interface{}{
		map[string]interface{}{"address": system},
		map[string]interface{}{"address": usdc},
	})))
	assert.Equal(t, router, extractResolvedContractAddressFromMetadata(withLogs(router, []interface{}{
		map[string]interface{}{"address": system},
	})))
	assert.Empty(t, extractResolvedContractAddressFromMetadata(withLogs("", []interface{}{
		map[string]interface{}{"address": system},
	})))
	assert.Equal(t, usdc, extractResolvedContractAddressFromMetadata(withLogs("", []interface{}{
		map[string]interface{}{"raw": map[string]interface{}{"address": system}},
		map[string]interface{}{"raw": map[string]interface{}{"address": usdc}},
	})))
}

func TestEnrichEventWithTokenMetadata_SkipsEthTransferSystemLog(t *testing.T) {
	result, err := EnrichEventWithTokenMetadata(SharedEventEnrichmentParams{
		EventLog: &types.Log{
			Address: ethTransferLogAddress,
			Topics:  []common.Hash{common.HexToHash(erc20TransferTopic0)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsTransferEvent)
	assert.Empty(t, result.ParsedData)
}

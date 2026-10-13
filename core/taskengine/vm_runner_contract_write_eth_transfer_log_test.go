package taskengine

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
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

// nestUnderRaw is the Tenderly shape: address, topics, and data live under raw.
func nestUnderRaw(log map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	for _, key := range []string{"address", "topics", "data"} {
		if value, ok := log[key]; ok {
			raw[key] = value
		}
	}
	return map[string]interface{}{"raw": raw}
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

		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, "", "")

		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, common.HexToAddress(recipient).Hex(), events["to"])
		assert.Equal(t, payment.String(), events["value"])
		assert.NotEqual(t, common.HexToAddress(entryPoint).Hex(), events["from"])
		assert.NotEqual(t, common.HexToAddress(bundler).Hex(), events["to"])
		assert.NotEqual(t, gasRefund.String(), events["value"])
		assert.Equal(t, []string{"Transfer"}, eventNames(t, events))
	})

	t.Run("refund before the token transfer", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(system, entryPoint, bundler, gasRefund),
			erc20TransferLog(usdc, runner, recipient, payment),
		}

		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, "", "")

		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, common.HexToAddress(recipient).Hex(), events["to"])
		assert.Equal(t, payment.String(), events["value"])
		assert.Equal(t, []string{"Transfer"}, eventNames(t, events))
	})

	t.Run("refund alone is not a payment", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(system, entryPoint, bundler, gasRefund),
		}

		// Even if the method target were the system address, the refund is not the step's transfer.
		events := processor.decodeRelevantMethodEvents(logs, ethTransferLogAddress.Hex(), "transfer", parsedABI, "", "")

		assert.Empty(t, events)
	})
}

const (
	userOperationEventTopic0 = "0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f"
	beforeExecutionTopic0    = "0xbb47ee3e183a558b1a2ff0874b079f3fc5478b7454eacf2bfc5af2ff5878f972"
)

const tokenApprovalTransferABI = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"owner","type":"address"},
		{"indexed":true,"name":"spender","type":"address"},
		{"indexed":false,"name":"value","type":"uint256"}
	],"name":"Approval","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"from","type":"address"},
		{"indexed":true,"name":"to","type":"address"},
		{"indexed":false,"name":"value","type":"uint256"}
	],"name":"Transfer","type":"event"}
]`

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

func mustABI(t *testing.T, jsonABI string) *abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(jsonABI))
	require.NoError(t, err)
	return &parsed
}

func markerLog(address, topic0 string, extra ...string) map[string]interface{} {
	topics := make([]interface{}, 0, 1+len(extra))
	topics = append(topics, topic0)
	for _, topic := range extra {
		topics = append(topics, topic)
	}
	return map[string]interface{}{
		"address": address,
		"topics":  topics,
		"data":    "0x",
	}
}

func namedValueLog(topic0, emitter, left, right string, value *big.Int) map[string]interface{} {
	return map[string]interface{}{
		"address": emitter,
		"topics":  []interface{}{topic0, addressTopic(left), addressTopic(right)},
		"data":    uint256Word(value),
	}
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

func eventRecords(t *testing.T, events map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := events["events"].([]interface{})
	require.True(t, ok, "events list missing: %#v", events)
	out := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		rec, ok := item.(map[string]interface{})
		require.True(t, ok)
		out = append(out, rec)
	}
	return out
}

func eventNames(t *testing.T, events map[string]interface{}) []string {
	t.Helper()
	records := eventRecords(t, events)
	names := make([]string, 0, len(records))
	for _, rec := range records {
		name, ok := rec["name"].(string)
		require.True(t, ok)
		names = append(names, name)
	}
	return names
}

func eventValue(t *testing.T, events map[string]interface{}, index int) interface{} {
	t.Helper()
	records := eventRecords(t, events)
	require.Greater(t, len(records), index)
	fields, ok := records[index]["fields"].(map[string]interface{})
	require.True(t, ok)
	return fields["value"]
}

func TestDecodeRelevantMethodEvents_UserOpWindow(t *testing.T) {
	const (
		usdc       = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runnerA    = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		runnerB    = "0x1111111111111111111111111111111111111111"
		recipient  = "0x633791DdC147B59345dc56D3A1F0606c23703154"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		system     = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	hashA := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	hashB := common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	paymentA := big.NewInt(100000)
	paymentB := big.NewInt(200000)

	logs := []interface{}{
		// Validation log. It sits before BeforeExecution and is not this op's transfer.
		erc20TransferLog(usdc, runnerA, recipient, big.NewInt(1)),
		markerLog(entryPoint, beforeExecutionTopic0),
		erc20TransferLog(usdc, runnerA, recipient, paymentA),
		markerLog(entryPoint, userOperationEventTopic0, hashA.Hex()),
		erc20TransferLog(usdc, runnerB, recipient, paymentB),
		markerLog(entryPoint, userOperationEventTopic0, hashB.Hex()),
		erc20TransferLog(system, entryPoint, bundler, big.NewInt(584351393496208)),
	}

	processor := testContractWriteProcessor()
	parsedABI := mustTransferABI(t)

	t.Run("op A keeps its own transfer", func(t *testing.T) {
		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, hashA.Hex(), runnerA)
		assert.Equal(t, common.HexToAddress(runnerA).Hex(), events["from"])
		assert.Equal(t, common.HexToAddress(recipient).Hex(), events["to"])
		assert.Equal(t, paymentA.String(), events["value"])
		assert.Equal(t, []string{"Transfer"}, eventNames(t, events))
		assert.Equal(t, paymentA.String(), eventValue(t, events, 0))
		_, err := structpb.NewValue(events)
		require.NoError(t, err)
	})

	t.Run("op B keeps its own transfer", func(t *testing.T) {
		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, hashB.Hex(), runnerB)
		assert.Equal(t, common.HexToAddress(runnerB).Hex(), events["from"])
		assert.Equal(t, paymentB.String(), events["value"])
		assert.Equal(t, []string{"Transfer"}, eventNames(t, events))
	})

	t.Run("missing hash decodes nothing", func(t *testing.T) {
		missing := common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, missing.Hex(), runnerA)
		assert.Empty(t, events)
	})

	t.Run("raw-shaped receipt still scopes to the userOp", func(t *testing.T) {
		rawLogs := make([]interface{}, 0, len(logs))
		for _, item := range logs {
			logMap, ok := item.(map[string]interface{})
			require.True(t, ok)
			rawLogs = append(rawLogs, nestUnderRaw(logMap))
		}
		events := processor.decodeRelevantMethodEvents(rawLogs, usdc, "transfer", parsedABI, hashA.Hex(), runnerA)
		assert.Equal(t, common.HexToAddress(runnerA).Hex(), events["from"])
		assert.Equal(t, paymentA.String(), events["value"])
		assert.Equal(t, []string{"Transfer"}, eventNames(t, events))
	})
}

func TestDecodeRelevantMethodEvents_ApproveDoesNotTakeTransferValue(t *testing.T) {
	const (
		usdc    = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner  = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		spender = "0x2222222222222222222222222222222222222222"
	)
	allowance := big.NewInt(999)
	payment := big.NewInt(100000)
	parsedABI := mustABI(t, tokenApprovalTransferABI)
	logs := []interface{}{
		namedValueLog(parsedABI.Events["Approval"].ID.Hex(), usdc, runner, spender, allowance),
		erc20TransferLog(usdc, runner, spender, payment),
	}
	processor := testContractWriteProcessor()

	approve := processor.decodeRelevantMethodEvents(logs, usdc, "approve", parsedABI, "", runner)
	assert.Equal(t, allowance.String(), approve["value"])
	assert.Equal(t, common.HexToAddress(runner).Hex(), approve["owner"])
	assert.Equal(t, []string{"Approval", "Transfer"}, eventNames(t, approve))
	assert.Equal(t, allowance.String(), eventValue(t, approve, 0))
	assert.Equal(t, payment.String(), eventValue(t, approve, 1))

	transfer := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, "", runner)
	assert.Equal(t, payment.String(), transfer["value"])
	assert.Equal(t, common.HexToAddress(runner).Hex(), transfer["from"])
	assert.Equal(t, []string{"Approval", "Transfer"}, eventNames(t, transfer))
}

func TestDecodeRelevantMethodEvents_TokenMethodFallsBackToTargetEvent(t *testing.T) {
	const (
		token      = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		other      = "0x2222222222222222222222222222222222222222"
		runner     = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		recipient  = "0x633791DdC147B59345dc56D3A1F0606c23703154"
		minter     = "0x0000000000000000000000000000000000000000"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		system     = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	minted := big.NewInt(100000)
	otherPayment := big.NewInt(1)
	processor := testContractWriteProcessor()
	parsedABI := mustABI(t, tokenApprovalTransferABI)
	logs := []interface{}{
		erc20TransferLog(system, entryPoint, bundler, big.NewInt(584351393496208)),
		erc20TransferLog(other, runner, recipient, otherPayment),
		erc20TransferLog(token, minter, recipient, minted),
	}

	for _, method := range []string{"mint", "burn", "safeTransferFrom"} {
		t.Run(method, func(t *testing.T) {
			events := processor.decodeRelevantMethodEvents(logs, token, method, parsedABI, "", runner)
			assert.Equal(t, common.HexToAddress(minter).Hex(), events["from"])
			assert.Equal(t, common.HexToAddress(recipient).Hex(), events["to"])
			assert.Equal(t, minted.String(), events["value"])
			assert.NotEqual(t, otherPayment.String(), events["value"])
			assert.Equal(t, []string{"Transfer", "Transfer"}, eventNames(t, events))
			assert.Equal(t, otherPayment.String(), eventValue(t, events, 0))
			assert.Equal(t, minted.String(), eventValue(t, events, 1))
		})
	}

	allowance := big.NewInt(999)
	approvalLogs := []interface{}{
		namedValueLog(parsedABI.Events["Approval"].ID.Hex(), token, runner, recipient, allowance),
		erc20TransferLog(system, entryPoint, bundler, big.NewInt(1)),
	}
	for _, method := range []string{"permit", "increaseAllowance"} {
		t.Run(method, func(t *testing.T) {
			events := processor.decodeRelevantMethodEvents(approvalLogs, token, method, parsedABI, "", runner)
			assert.Equal(t, allowance.String(), events["value"])
			assert.Equal(t, common.HexToAddress(runner).Hex(), events["owner"])
			assert.Equal(t, common.HexToAddress(recipient).Hex(), events["spender"])
			_, hasFrom := events["from"]
			assert.False(t, hasFrom)
			assert.Equal(t, []string{"Approval"}, eventNames(t, events))
		})
	}
}

func TestDecodeRelevantMethodEvents_PrefersRunnerTransfer(t *testing.T) {
	const (
		usdc      = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner    = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		other     = "0x1111111111111111111111111111111111111111"
		recipient = "0x633791DdC147B59345dc56D3A1F0606c23703154"
	)
	payment := big.NewInt(100000)
	processor := testContractWriteProcessor()
	parsedABI := mustTransferABI(t)

	t.Run("runner transfer is first", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(usdc, runner, recipient, payment),
			erc20TransferLog(usdc, other, recipient, big.NewInt(1)),
		}
		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, "", runner)
		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, payment.String(), events["value"])
		assert.Equal(t, []string{"Transfer", "Transfer"}, eventNames(t, events))

		formatting := NewDecimalFormattingContext(big.NewInt(6), []string{"transfer.value"}, "decimals")
		formatting.ApplyDecimalFormattingToEventData(events, "transfer", nil)
		assert.Equal(t, "0.1", events["value"])
		assert.Equal(t, payment.String(), eventValue(t, events, 0))
		assert.Equal(t, "1", eventValue(t, events, 1))
	})

	t.Run("runner transfer is not first", func(t *testing.T) {
		logs := []interface{}{
			erc20TransferLog(usdc, other, recipient, big.NewInt(1)),
			erc20TransferLog(usdc, runner, recipient, payment),
		}
		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, "", runner)
		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, payment.String(), events["value"])
		assert.Equal(t, []string{"Transfer", "Transfer"}, eventNames(t, events))
		assert.Equal(t, "1", eventValue(t, events, 0))
		assert.Equal(t, payment.String(), eventValue(t, events, 1))
	})
}

func TestDecodeRelevantMethodEvents_RouterKeepsSwapFields(t *testing.T) {
	const (
		router = "0x3333333333333333333333333333333333333333"
		pool   = "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50"
		token  = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
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

	t.Run("swap fields stay and the token transfer is listed", func(t *testing.T) {
		logs := []interface{}{swapLog, tokenLog}
		events := processor.decodeRelevantMethodEvents(logs, router, "exactInputSingle", parsedABI, "", "")
		assert.Equal(t, amount0.String(), events["amount0"])
		_, hasValue := events["value"]
		assert.False(t, hasValue)
		assert.Equal(t, []string{"Swap", "Transfer"}, eventNames(t, events))
		assert.Equal(t, payment.String(), eventValue(t, events, 1))
		_, err := structpb.NewValue(events)
		require.NoError(t, err)
	})

	t.Run("pool swap after the token transfer still fills the flat map", func(t *testing.T) {
		system := "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
		entryPoint := "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler := "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		logs := []interface{}{
			tokenLog,
			swapLog,
			erc20TransferLog(system, entryPoint, bundler, big.NewInt(584351393496208)),
		}
		events := processor.decodeRelevantMethodEvents(logs, router, "exactInputSingle", parsedABI, "", "")
		assert.Equal(t, amount0.String(), events["amount0"])
		_, hasValue := events["value"]
		assert.False(t, hasValue)
		assert.Equal(t, []string{"Transfer", "Swap"}, eventNames(t, events))
		assert.Equal(t, payment.String(), eventValue(t, events, 0))
	})

	t.Run("raw pool swap after a raw token transfer is still merged", func(t *testing.T) {
		logs := []interface{}{nestUnderRaw(tokenLog), nestUnderRaw(swapLog)}
		events := processor.decodeRelevantMethodEvents(logs, router, "exactInputSingle", parsedABI, "", "")
		assert.Equal(t, amount0.String(), events["amount0"])
		_, hasValue := events["value"]
		assert.False(t, hasValue)
		assert.Equal(t, []string{"Transfer", "Swap"}, eventNames(t, events))
	})

	t.Run("a token transfer alone does not replace the return value", func(t *testing.T) {
		logs := []interface{}{tokenLog}
		events := processor.decodeRelevantMethodEvents(logs, router, "exactInputSingle", parsedABI, "", "")
		assert.Empty(t, events)
	})
}

func TestFlattenDecodedEvent_UnwrapsOrDropsEventName(t *testing.T) {
	wrapped := map[string]interface{}{
		"Transfer":  map[string]interface{}{"from": "0xabc", "value": "1"},
		"eventName": "Transfer",
	}
	flat := flattenDecodedEvent(wrapped, "Transfer")
	assert.Equal(t, "0xabc", flat["from"])
	assert.Equal(t, "1", flat["value"])
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

	// A non-map value under the event name is not a wrapper.
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
	const selfdestructAndTransferABI = `[
		{"anonymous":false,"inputs":[
			{"indexed":true,"name":"beneficiary","type":"address"},
			{"indexed":false,"name":"balance","type":"uint256"}
		],"name":"Selfdestruct","type":"event"},
		{"anonymous":false,"inputs":[
			{"indexed":true,"name":"from","type":"address"},
			{"indexed":true,"name":"to","type":"address"},
			{"indexed":false,"name":"value","type":"uint256"}
		],"name":"Transfer","type":"event"}
	]`
	const (
		usdc       = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		runner     = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		recipient  = "0x633791DdC147B59345dc56D3A1F0606c23703154"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		system     = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	parsedABI := mustABI(t, selfdestructAndTransferABI)
	// EIP-7708's historical burn log. The signature does not depend on `indexed`.
	require.Equal(t,
		"0x4bfaba3443c1a1836cd362418edc679fc96cae8449cbefccb6457cdf2c943083",
		parsedABI.Events["Selfdestruct"].ID.Hex(),
	)
	burn := map[string]interface{}{
		"address": system,
		"topics": []interface{}{
			parsedABI.Events["Selfdestruct"].ID.Hex(),
			addressTopic(entryPoint),
		},
		"data": uint256Word(big.NewInt(1)),
	}
	processor := testContractWriteProcessor()

	t.Run("burn alone is not a method event", func(t *testing.T) {
		// The method target is the system address, so relevance would accept the
		// log. The address skip drops it before the ABI is applied.
		events := processor.decodeRelevantMethodEvents(
			[]interface{}{burn},
			ethTransferLogAddress.Hex(),
			"execute",
			parsedABI,
			"",
			"",
		)
		assert.Empty(t, events)
	})

	t.Run("token transfer stays when the burn log is present", func(t *testing.T) {
		payment := big.NewInt(100000)
		logs := []interface{}{
			burn,
			erc20TransferLog(usdc, runner, recipient, payment),
		}
		events := processor.decodeRelevantMethodEvents(logs, usdc, "transfer", parsedABI, "", runner)
		assert.Equal(t, payment.String(), events["value"])
		assert.Equal(t, common.HexToAddress(runner).Hex(), events["from"])
		assert.Equal(t, []string{"Transfer"}, eventNames(t, events))
	})
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

func transferABIValues(t *testing.T) []*structpb.Value {
	t.Helper()
	var items []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(transferEventABI), &items))
	values := make([]*structpb.Value, 0, len(items))
	for _, item := range items {
		value, err := structpb.NewValue(item)
		require.NoError(t, err)
		values = append(values, value)
	}
	return values
}

func TestEnrichEventWithTokenMetadata_EthTransferSystemLogIsNative(t *testing.T) {
	const (
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
	)
	gasRefund := big.NewInt(584351393496208)
	refundLog := &types.Log{
		Address: ethTransferLogAddress,
		Topics: []common.Hash{
			common.HexToHash(erc20TransferTopic0),
			common.HexToHash(addressTopic(entryPoint)),
			common.HexToHash(addressTopic(bundler)),
		},
		Data: common.LeftPadBytes(gasRefund.Bytes(), 32),
	}

	t.Run("enriched as the native token without a token lookup", func(t *testing.T) {
		tokenService, err := NewWorkerRoutedTokenEnrichmentService(11155111, func(contractAddress string) (*TokenMetadata, error) {
			t.Errorf("looked up %s as a token; the system address is not a token contract", contractAddress)
			return nil, nil
		}, nil)
		require.NoError(t, err)

		result, err := EnrichEventWithTokenMetadata(SharedEventEnrichmentParams{
			EventLog:               refundLog,
			ContractABI:            transferABIValues(t),
			TokenEnrichmentService: tokenService,
			ChainID:                11155111,
		})

		require.NoError(t, err)
		assert.True(t, result.IsTransferEvent)
		assert.Equal(t, "ETH", result.ParsedData["tokenSymbol"])
		assert.Equal(t, uint32(18), result.ParsedData["tokenDecimals"])
		assert.Equal(t, gasRefund.String(), result.ParsedData["value"])
		assert.Equal(t, "0.000584351393496208", result.ParsedData["valueFormatted"])
		assert.Equal(t, common.HexToAddress(entryPoint).Hex(), result.ParsedData["fromAddress"])
		assert.Equal(t, common.HexToAddress(bundler).Hex(), result.ParsedData["toAddress"])
		assert.Equal(t, ethTransferLogAddress.Hex(), result.RawEventData["address"])
	})

	t.Run("decoded fields are kept without a token service", func(t *testing.T) {
		result, err := EnrichEventWithTokenMetadata(SharedEventEnrichmentParams{
			EventLog:    refundLog,
			ContractABI: transferABIValues(t),
		})

		require.NoError(t, err)
		transfer, isMap := result.ParsedData["Transfer"].(map[string]interface{})
		require.True(t, isMap, "parsed data: %#v", result.ParsedData)
		assert.Equal(t, common.HexToAddress(entryPoint).Hex(), transfer["from"])
		assert.Equal(t, common.HexToAddress(bundler).Hex(), transfer["to"])
		assert.Equal(t, gasRefund.String(), transfer["value"])
	})

	t.Run("basic log metadata is kept without an ABI", func(t *testing.T) {
		result, err := EnrichEventWithTokenMetadata(SharedEventEnrichmentParams{EventLog: refundLog})

		require.NoError(t, err)
		assert.Equal(t, ethTransferLogAddress.Hex(), result.ParsedData["contractAddress"])
	})
}

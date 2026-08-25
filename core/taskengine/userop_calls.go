package taskengine

import (
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/types/known/structpb"
)

// InnerCall is one decoded smart-wallet execute / executeBatch entry:
// the destination, native value, 4-byte selector, and raw calldata.
// This is the typed shape stamped on nodes:run receipts (N14.a).
type InnerCall struct {
	To       string
	Value    string
	Selector string
	Data     string
}

// InnerCallsFromExecuteCalldata unpacks smart-wallet execute calldata
// (v0.6 execute / executeBatch / executeBatchWithValues, or MA v2 tuple-batch)
// into typed inner calls. Empty input yields a nil slice, not an error.
func InnerCallsFromExecuteCalldata(packed []byte) ([]InnerCall, error) {
	if len(packed) == 0 {
		return nil, nil
	}
	targets, values, datas, err := aa.UnpackExecuteCalldata(packed)
	if err != nil {
		return nil, err
	}
	if len(targets) != len(values) || len(targets) != len(datas) {
		return nil, fmt.Errorf("unpacked execute calldata length mismatch")
	}
	out := make([]InnerCall, len(targets))
	for i := range targets {
		out[i] = InnerCall{
			To:       targets[i].Hex(),
			Value:    weiString(values[i]),
			Selector: SelectorFromCalldata(datas[i]),
			Data:     "0x" + common.Bytes2Hex(datas[i]),
		}
	}
	return out, nil
}

func weiString(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func innerCallAsMap(c InnerCall) map[string]interface{} {
	return map[string]interface{}{
		"to":       c.To,
		"value":    c.Value,
		"selector": c.Selector,
		"data":     c.Data,
	}
}

func innerCallsAsInterface(calls []InnerCall) []interface{} {
	out := make([]interface{}, len(calls))
	for i, c := range calls {
		out[i] = innerCallAsMap(c)
	}
	return out
}

// stampInnerCalls writes receipt.calls (and receipt.failedCall when a single
// inner call reverted or was refused) onto an existing receipt map.
func stampInnerCalls(receiptMap map[string]interface{}, packed []byte, innerFailed bool) {
	if receiptMap == nil || len(packed) == 0 {
		return
	}
	calls, err := InnerCallsFromExecuteCalldata(packed)
	if err != nil || len(calls) == 0 {
		return
	}
	receiptMap["calls"] = innerCallsAsInterface(calls)
	if innerFailed && len(calls) == 1 {
		receiptMap["failedCall"] = innerCallAsMap(calls[0])
	}
}

// failedReceiptWithInnerCalls builds a failed receipt that still carries the
// inner calls we packed — used on AA23 / bundler reject after calldata exists.
func failedReceiptWithInnerCalls(packed []byte) *structpb.Value {
	receiptMap := map[string]interface{}{
		"executionStatus": "failed",
	}
	stampInnerCalls(receiptMap, packed, true)
	v, err := structpb.NewValue(receiptMap)
	if err != nil {
		return nil
	}
	return v
}

// attachFailedInnerCalls stamps a failed receipt with inner calls onto every
// method result that does not already have one (atomic-batch AA23 path).
func attachFailedInnerCalls(results []*avsproto.ContractWriteNode_MethodResult, packed []byte) {
	if len(packed) == 0 {
		return
	}
	for _, mr := range results {
		if mr == nil {
			continue
		}
		if mr.Receipt == nil {
			mr.Receipt = failedReceiptWithInnerCalls(packed)
			continue
		}
		m, ok := mr.Receipt.AsInterface().(map[string]interface{})
		if !ok || m == nil {
			mr.Receipt = failedReceiptWithInnerCalls(packed)
			continue
		}
		stampInnerCalls(m, packed, true)
		if v, err := structpb.NewValue(m); err == nil {
			mr.Receipt = v
		}
	}
}

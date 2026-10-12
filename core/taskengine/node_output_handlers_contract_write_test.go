package taskengine

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestContractWriteOutputHandler_KeepsVMData(t *testing.T) {
	const (
		runner     = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
		recipient  = "0x633791DdC147B59345dc56D3A1F0606c23703154"
		entryPoint = "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
		bundler    = "0x813781939B0D041A0b8d9d0ACc2f29632B786a27"
		system     = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
	)
	payment := map[string]interface{}{
		"from":  common.HexToAddress(runner).Hex(),
		"to":    common.HexToAddress(recipient).Hex(),
		"value": "100000",
	}
	receipt, err := structpb.NewValue(map[string]interface{}{
		"logs": []interface{}{
			erc20TransferLog(system, entryPoint, bundler, big.NewInt(584351393496208)),
		},
	})
	require.NoError(t, err)
	methodABI, err := structpb.NewValue(map[string]interface{}{
		"contractABI": transferEventABI,
	})
	require.NoError(t, err)

	handler := NewContractWriteOutputHandler(nil)
	output, metadata, err := handler.ConvertToProtobuf(11155111, map[string]interface{}{
		"data": map[string]interface{}{"transfer": payment},
		"results": []interface{}{
			&avsproto.ContractWriteNode_MethodResult{
				MethodName: "transfer",
				Success:    true,
				Receipt:    receipt,
				MethodAbi:  methodABI,
			},
		},
	})
	require.NoError(t, err)

	wrapped, ok := output.(*avsproto.RunNodeWithInputsResp_ContractWrite)
	require.True(t, ok)
	data, ok := wrapped.ContractWrite.GetData().AsInterface().(map[string]interface{})
	require.True(t, ok)
	got, ok := data["transfer"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, payment["from"], got["from"])
	assert.Equal(t, payment["to"], got["to"])
	assert.Equal(t, payment["value"], got["value"])
	assert.NotEqual(t, common.HexToAddress(entryPoint).Hex(), got["from"])
	assert.NotEqual(t, "584351393496208", got["value"])

	require.NotNil(t, metadata)
	results, ok := metadata.AsInterface().([]interface{})
	require.True(t, ok)
	require.Len(t, results, 1)
	row, ok := results[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "transfer", row["methodName"])
	assert.Equal(t, true, row["success"])
	assert.NotNil(t, row["receipt"])
}

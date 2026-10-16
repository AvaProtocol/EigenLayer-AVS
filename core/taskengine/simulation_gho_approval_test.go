package taskengine

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

type approvalLayoutTransport func(*http.Request) (*http.Response, error)

func (f approvalLayoutTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Base UpgradeableGhoToken has allowance at mapping slot 5, not the slots
// used by OpenZeppelin/USDC. Keep this regression independent of live funds.
func TestSimulatedGHOApprovalPropagatesAllowance(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "successful_approval"
		if !success {
			name = "rejected_approval"
		}
		t.Run(name, func(t *testing.T) {
			const token = "0x6Bb7a212910682DCFdbd5BCBb3e28FB4E8da10Ee"
			const owner = "0x1111111111111111111111111111111111111111"
			const spender = "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"
			const amount = "1000000000000000000"
			vm := NewVM().WithLogger(testutil.GetLogger()).SetSimulation(true)
			vm.AddVar("aa_sender", owner)
			vm.AddVar("settings", map[string]any{"runner": owner, "chain_id": int64(8453)})
			vm.tenderlyClient = NewTenderlyClient(&config.Config{
				TenderlyAccount: "fixture", TenderlyProject: "fixture", TenderlyAccessKey: "fixture",
			}, testutil.GetLogger())
			vm.tenderlyClient.httpClient.SetTransport(approvalLayoutTransport(func(req *http.Request) (*http.Response, error) {
				var input map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&input))
				require.Equal(t, token, input["to"])
				require.Equal(t, owner, input["from"])
				require.True(t, strings.HasPrefix(input["input"].(string), "0x095ea7b3"))
				body, err := json.Marshal(map[string]any{
					"simulation": map[string]any{"status": success},
					"transaction": map[string]any{
						"gas_used": 21000, "gas_price": 1,
						"error_message": "approval rejected",
						"transaction_info": map[string]any{
							"call_trace":     map[string]any{"output": "0x" + strings.Repeat("0", 63) + "1"},
							"raw_state_diff": nil,
						},
					},
				})
				require.NoError(t, err)
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(string(body))), Request: req,
				}, nil
			}))
			node, err := CreateNodeFromType("contractWrite", map[string]any{
				"chainId": 8453, "contractAddress": token,
				"contractAbi": []any{map[string]any{
					"type": "function", "name": "approve", "stateMutability": "nonpayable",
					"inputs": []any{
						map[string]any{"name": "spender", "type": "address"},
						map[string]any{"name": "amount", "type": "uint256"},
					},
					"outputs": []any{map[string]any{"type": "bool"}},
				}},
				"methodCalls": []any{map[string]any{"methodName": "approve", "methodParams": []any{spender, amount}}},
			}, "")
			require.NoError(t, err)
			processor := NewContractWriteProcessor(vm, nil, nil, common.Address{})
			step, _ := processor.Execute("approve", node.GetContractWrite())
			require.NotNil(t, step)
			require.Equal(t, success, step.Success, step.Error)
			objects := vm.simulationState.BuildStateObjects(owner, "0x0")
			if !success {
				require.NotContains(t, objects, strings.ToLower(token), "rejected approval must not invent an allowance")
				return
			}
			storage := objects[strings.ToLower(token)].(map[string]any)["storage"].(map[string]string)
			ghoSlot := erc20AllowanceSlot(common.HexToAddress(owner), common.HexToAddress(spender), 5)
			require.Equal(t, "0x0000000000000000000000000000000000000000000000000de0b6b3a7640000", storage[ghoSlot.Hex()])
		})
	}
}

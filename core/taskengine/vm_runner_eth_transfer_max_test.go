package taskengine

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

const (
	maxTestWallet      = "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6"
	maxTestDestination = "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557"
	maxTestChain       = int64(11155111)
)

// maxTestBalance is the wallet's balance, 0.01 ETH.
var maxTestBalance = big.NewInt(10_000_000_000_000_000)

// maxTestChainRPC answers the reads a MAX transfer makes: the balance, the
// tip and base fee behind the current max fee (1 + 2 × 2 = 5 gwei), the
// EntryPoint deposit and the remaining native limit.
type maxTestChainRPC struct {
	balance      *big.Int
	deposit      *big.Int
	remaining    *big.Int
	limitFails   bool
	balanceReads atomic.Int32
	limitReads   atomic.Int32
}

func newMaxTestChain(t *testing.T, balance *big.Int) (*maxTestChainRPC, *ethclient.Client, string) {
	t.Helper()
	chain := &maxTestChainRPC{balance: balance}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&call))
		var result interface{}
		var rpcError string
		switch call.Method {
		case "eth_getCode":
			result = "0x"
		case "eth_call":
			var message map[string]string
			require.NoError(t, json.Unmarshal(call.Params[0], &message))
			data := message["data"]
			if data == "" {
				data = message["input"]
			}
			switch data[:10] {
			case hexutil.Encode(crypto.Keccak256([]byte("balanceOf(address)"))[:4]):
				deposit := chain.deposit
				if deposit == nil {
					deposit = big.NewInt(0)
				}
				result = hexutil.Encode(common.LeftPadBytes(deposit.Bytes(), 32))
			case hexutil.Encode(crypto.Keccak256([]byte("limits(uint256,address)"))[:4]):
				chain.limitReads.Add(1)
				if chain.limitFails || chain.remaining == nil {
					rpcError = "limit read failed"
					break
				}
				result = hexutil.Encode(common.LeftPadBytes(chain.remaining.Bytes(), 32))
			default:
				t.Errorf("unexpected eth_call selector %s", data[:10])
			}
		case "eth_getBalance":
			chain.balanceReads.Add(1)
			result = hexutil.EncodeBig(chain.balance)
		case "eth_maxPriorityFeePerGas":
			result = "0x3b9aca00"
		case "eth_getBlockByNumber":
			result = &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(0), GasLimit: 30_000_000, BaseFee: big.NewInt(2_000_000_000), Extra: []byte{}}
		default:
			t.Errorf("unexpected RPC method %s", call.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{"jsonrpc": "2.0", "id": call.ID, "result": result}
		if rpcError != "" {
			delete(response, "result")
			response["error"] = map[string]interface{}{"code": -32000, "message": rpcError}
		}
		require.NoError(t, json.NewEncoder(writer).Encode(response))
	}))
	t.Cleanup(server.Close)
	client, err := ethclient.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return chain, client, server.URL
}

func newMaxTestProcessor(t *testing.T, client *ethclient.Client, cfg *config.SmartWalletConfig) (*ETHTransferProcessor, *avsproto.ETHTransferNode) {
	t.Helper()
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	vm.AddVar("aa_sender", maxTestWallet)
	owner := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, client, cfg, &owner)
	node := &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{Destination: maxTestDestination, Amount: "MAX", ChainId: maxTestChain}}
	taskNode := &avsproto.TaskNode{Id: "send-eth", Name: "sendEth", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER, TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: node}}
	processor.CommonProcessor.SetTaskNode(taskNode)
	vm.TaskNodes = map[string]*avsproto.TaskNode{"send-eth": taskNode}
	return processor, node
}

func selfFundedMaxConfig(rpcURL string) *config.SmartWalletConfig {
	return &config.SmartWalletConfig{ChainID: maxTestChain, EthRpcUrl: rpcURL, DisableGasSponsorship: true}
}

// stubSends replaces both session sends for one test. reserve answers the
// self-funded MAX; plain fails the test unless the case expects it.
func stubSends(t *testing.T, reserve func(*preset.NativeReserve) (*preset.NativeReserveResult, error), plain func([]byte)) {
	t.Helper()
	previousPlain, previousReserve := sendUserOpAuto, sendNativeReserve
	t.Cleanup(func() { sendUserOpAuto, sendNativeReserve = previousPlain, previousReserve })
	receipt := &types.Receipt{TxHash: common.HexToHash("0xabc"), GasUsed: 100_000, EffectiveGasPrice: big.NewInt(1_000_000_000)}
	sendNativeReserve = func(_ *config.SmartWalletConfig, _ common.Address, requested *preset.NativeReserve, _ *common.Address, _ *big.Int, _ logger.Logger) (*preset.SentUserOp, *types.Receipt, *preset.NativeReserveResult, error) {
		if reserve == nil {
			t.Fatal("a self-funded MAX must not reach the reserve send in this case")
		}
		sized, err := reserve(requested)
		if err != nil {
			return nil, nil, sized, err
		}
		return &preset.SentUserOp{}, receipt, sized, nil
	}
	sendUserOpAuto = func(_ *config.SmartWalletConfig, _ common.Address, callData []byte, _ *common.Address, _ *big.Int, _ logger.Logger) (*preset.SentUserOp, *types.Receipt, error) {
		if plain == nil {
			t.Fatal("this case must not send a fixed value")
		}
		plain(callData)
		return &preset.SentUserOp{}, receipt, nil
	}
}

func stepMaps(t *testing.T, step *avsproto.Execution_Step) (transfer, metadata map[string]interface{}) {
	t.Helper()
	data, ok := step.GetEthTransfer().GetData().AsInterface().(map[string]interface{})
	require.True(t, ok)
	transfer, ok = data["transfer"].(map[string]interface{})
	require.True(t, ok)
	metadata, ok = step.Metadata.AsInterface().(map[string]interface{})
	require.True(t, ok)
	return transfer, metadata
}

func TestETHTransferSelfFundedMaxSendsWhatTheReserveSized(t *testing.T) {
	_, client, url := newMaxTestChain(t, maxTestBalance)
	gasReserve := big.NewInt(400_000_000_000_000)
	sent := new(big.Int).Sub(maxTestBalance, gasReserve)
	var requested *preset.NativeReserve
	stubSends(t, func(reserve *preset.NativeReserve) (*preset.NativeReserveResult, error) {
		requested = reserve
		return &preset.NativeReserveResult{Value: sent, MaxGasCost: gasReserve, GasReserve: gasReserve, LimitedBy: preset.NativeReserveLimitedByBalance, Left: big.NewInt(0)}, nil
	}, nil)
	processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))

	step, err := processor.Execute("send-eth", node)
	require.NoError(t, err)
	require.True(t, step.Success, step.Error)
	require.Equal(t, common.HexToAddress(maxTestDestination), requested.Recipient)
	require.Equal(t, maxTestBalance.String(), requested.Balance.String(), "the balance read for MAX is the reserve's upper bound")
	require.Nil(t, requested.GrantedLimit, "no stored grant, so no native limit to start an install from")

	transfer, metadata := stepMaps(t, step)
	require.Equal(t, sent.String(), transfer["value"], "the output reports the amount sent, not the balance")
	require.Equal(t, gasReserve.String(), metadata["gasReserve"])
	require.NotContains(t, metadata, "limitedBy")
	require.Contains(t, step.Log, "MAX kept "+gasReserve.String()+" wei for the network fee")
}

func TestETHTransferSelfFundedMaxReportsWhatThePermissionLeft(t *testing.T) {
	_, client, url := newMaxTestChain(t, maxTestBalance)
	stubSends(t, func(*preset.NativeReserve) (*preset.NativeReserveResult, error) {
		return &preset.NativeReserveResult{
			Value: big.NewInt(9_000_000_000_000_000), MaxGasCost: big.NewInt(400_000_000_000_000),
			GasReserve: big.NewInt(400_000_000_000_000), LimitedBy: preset.NativeReserveLimitedByPermission,
			Left: big.NewInt(600_000_000_000_000),
		}, nil
	}, nil)
	processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))

	step, err := processor.Execute("send-eth", node)
	require.NoError(t, err)
	transfer, metadata := stepMaps(t, step)
	require.Equal(t, "9000000000000000", transfer["value"])
	require.Equal(t, preset.NativeReserveLimitedByPermission, metadata["limitedBy"])
	require.Equal(t, "600000000000000", metadata["leftInWallet"])
	require.Contains(t, step.Log, "left 600000000000000 wei in the wallet")
}

func TestETHTransferSelfFundedMaxRefusalKeepsItsCode(t *testing.T) {
	for _, tc := range []struct {
		code     string
		protoErr avsproto.ErrorCode
	}{
		{preset.NativeBalanceBelowFeeCode, avsproto.ErrorCode_INSUFFICIENT_BALANCE},
		{preset.NativeCapBelowFeeCode, avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, client, url := newMaxTestChain(t, maxTestBalance)
			stubSends(t, func(*preset.NativeReserve) (*preset.NativeReserveResult, error) {
				return nil, &preset.NativeReserveError{Code: tc.code, Balance: maxTestBalance, GasReserve: big.NewInt(5), Remaining: big.NewInt(3), MaxGasCost: big.NewInt(5)}
			}, nil)
			processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))

			step, err := processor.Execute("send-eth", node)
			require.Error(t, err)
			require.False(t, step.Success)
			require.True(t, strings.HasPrefix(step.Error, tc.code+":"), step.Error)
			require.Equal(t, tc.protoErr, step.ErrorCode)
			require.Equal(t, tc.code, step.ErrorDetails.AsMap()["code"])
			require.Equal(t, "3", step.ErrorDetails.AsMap()["remaining"])
		})
	}
}

func TestETHTransferSponsoredMaxStillSendsTheWholeBalance(t *testing.T) {
	_, client, url := newMaxTestChain(t, maxTestBalance)
	var sentValue *big.Int
	stubSends(t, nil, func(callData []byte) {
		_, values, _, err := aa.UnpackExecuteCalldata(callData)
		require.NoError(t, err)
		sentValue = values[0]
	})
	sponsored := &config.SmartWalletConfig{ChainID: maxTestChain, EthRpcUrl: url, AlchemyPaymasterPolicyID: "test-policy", BundlerProvider: config.BundlerProviderAlchemy}
	require.NotEmpty(t, sponsored.SponsorshipPolicyID())
	processor, node := newMaxTestProcessor(t, client, sponsored)

	step, err := processor.Execute("send-eth", node)
	require.NoError(t, err)
	require.True(t, step.Success, step.Error)
	require.Equal(t, maxTestBalance.String(), sentValue.String())
	_, metadata := stepMaps(t, step)
	require.NotContains(t, metadata, "gasReserve")
}

// A pending operation has not paid its prefund yet, so MAX must not read
// the balance until it is included.
func TestETHTransferMaxWaitsForEarlierWrites(t *testing.T) {
	chain, client, url := newMaxTestChain(t, maxTestBalance)
	stubSends(t, nil, nil)
	processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
	vm := processor.vm
	tokenNode := &avsproto.TaskNode{Id: "send-token", Name: "sendToken", TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{ChainId: maxTestChain}}}}
	vm.TaskNodes["send-token"] = tokenNode
	pending, err := structpb.NewValue(map[string]interface{}{"receiptStatus": "pending", "userOpHash": "0x01"})
	require.NoError(t, err)
	vm.ExecutionLogs = append(vm.ExecutionLogs, &avsproto.Execution_Step{
		Id: "send-token", Name: "sendToken", Success: true, Metadata: pending,
		OutputData: &avsproto.Execution_Step_EthTransfer{EthTransfer: &avsproto.ETHTransferNode_Output{}},
	})

	step, err := processor.Execute("send-eth", node)
	require.Error(t, err)
	require.False(t, step.Success)
	require.Equal(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, step.ErrorCode)
	require.Contains(t, step.Error, "sendToken")
	require.Zero(t, chain.balanceReads.Load(), "the balance is not read while an earlier operation may be pending")

	// A write on another chain does not hold this one.
	tokenNode.GetEthTransfer().Config.ChainId = 8453
	stubSends(t, func(reserve *preset.NativeReserve) (*preset.NativeReserveResult, error) {
		return &preset.NativeReserveResult{Value: big.NewInt(1), MaxGasCost: big.NewInt(1), GasReserve: big.NewInt(1), LimitedBy: preset.NativeReserveLimitedByBalance, Left: big.NewInt(0)}, nil
	}, nil)
	step, err = processor.Execute("send-eth", node)
	require.NoError(t, err)
	require.True(t, step.Success, step.Error)
}

// Test shows about what Run sends: the balance minus the steady operation's
// gas at the current max fee.
func TestETHTransferMaxSimulationShowsTheBalanceMinusTheFee(t *testing.T) {
	maxFee := big.NewInt(5_000_000_000)
	fee := new(big.Int).Mul(big.NewInt(nativePreflightGasUnitsSteady), maxFee)

	t.Run("self funded", func(t *testing.T) {
		_, client, url := newMaxTestChain(t, maxTestBalance)
		processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
		processor.vm.SetSimulation(true)
		step, err := processor.Execute("send-eth", node)
		require.NoError(t, err)
		transfer, metadata := stepMaps(t, step)
		require.Equal(t, new(big.Int).Sub(maxTestBalance, fee).String(), transfer["value"])
		require.Equal(t, fee.String(), metadata["gasReserve"])
		require.Equal(t, maxFee.String(), step.GasPrice)
		require.Equal(t, fee.String(), step.TotalGasCost)
	})
	t.Run("sponsored", func(t *testing.T) {
		_, client, url := newMaxTestChain(t, maxTestBalance)
		sponsored := &config.SmartWalletConfig{ChainID: maxTestChain, EthRpcUrl: url, AlchemyPaymasterPolicyID: "test-policy", BundlerProvider: config.BundlerProviderAlchemy}
		processor, node := newMaxTestProcessor(t, client, sponsored)
		processor.vm.SetSimulation(true)
		step, err := processor.Execute("send-eth", node)
		require.NoError(t, err)
		transfer, metadata := stepMaps(t, step)
		require.Equal(t, maxTestBalance.String(), transfer["value"])
		require.NotContains(t, metadata, "gasReserve")
	})
	t.Run("balance below the fee", func(t *testing.T) {
		_, client, url := newMaxTestChain(t, fee)
		processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
		processor.vm.SetSimulation(true)
		step, err := processor.Execute("send-eth", node)
		require.Error(t, err)
		require.True(t, strings.HasPrefix(step.Error, preset.NativeBalanceBelowFeeCode+":"), step.Error)
		require.Equal(t, avsproto.ErrorCode_INSUFFICIENT_BALANCE, step.ErrorCode)
	})
}

// A self-funded MAX preflights 0 wei: the send sizes the value to the
// remaining limit. Preflighting the whole balance refused a permission that
// covers the gas but not all the ETH, which is a partial sweep by design.
func TestETHTransferMaxPreflightLeavesTheValueToTheReserve(t *testing.T) {
	recipient := common.HexToAddress(maxTestDestination)
	policy := &model.SessionPolicy{
		ID:               "01sweep",
		NativeRecipients: []*common.Address{&recipient},
		// 0.02 ETH: above this unapplied grant's priced install operation
		// (about 0.0169 ETH at 5 gwei) but below it plus the 0.01 ETH balance.
		NativeSpendCap: &model.NativeSpendCap{Amount: "20000000000000000", GrantedCap: "20000000000000000"},
	}
	reader := stubCodeAndFee{fee: big.NewInt(5_000_000_000)}
	whole := PreflightNativePermission(policy, NativeIntent{Recipient: recipient, Amount: maxTestBalance, Kind: NativeSend}, reader)
	require.True(t, strings.HasPrefix(whole, SessionPolicyNativeCapExceededCode+":"), whole)
	require.Empty(t, PreflightNativePermission(policy, NativeIntent{Recipient: recipient, Amount: big.NewInt(0), Kind: NativeSend}, reader))
	// The recipient check still applies.
	stranger := common.HexToAddress("0x000000000000000000000000000000000000b0b0")
	require.True(t, strings.HasPrefix(PreflightNativePermission(policy, NativeIntent{Recipient: stranger, Amount: big.NewInt(0), Kind: NativeSend}, reader), SessionPolicyRecipientNotAllowedCode+":"))
	// And a permission below the step's own gas is still refused.
	tiny := *policy
	tiny.NativeSpendCap = &model.NativeSpendCap{Amount: "1", GrantedCap: "1"}
	require.True(t, strings.HasPrefix(PreflightNativePermission(&tiny, NativeIntent{Recipient: recipient, Amount: big.NewInt(0), Kind: NativeSend}, reader), SessionPolicyNativeCapExceededCode+":"))
}

func TestGrantedNativeLimit(t *testing.T) {
	limit, err := grantedNativeLimit(nil)
	require.NoError(t, err)
	require.Nil(t, limit)
	limit, err = grantedNativeLimit(&model.SessionPolicy{})
	require.NoError(t, err)
	require.Nil(t, limit)
	limit, err = grantedNativeLimit(&model.SessionPolicy{NativeSpendCap: &model.NativeSpendCap{Amount: "7", GrantedCap: "9"}})
	require.NoError(t, err)
	require.Equal(t, "9", limit.String(), "the cap the grant installs")
	_, err = grantedNativeLimit(&model.SessionPolicy{NativeSpendCap: &model.NativeSpendCap{Amount: "not a number"}})
	require.Error(t, err)
}

// A retry that lowered the signed gas can leave ETH behind even when the
// balance set the value; the output says so.
func TestETHTransferSelfFundedMaxReportsAResidualAfterARetry(t *testing.T) {
	_, client, url := newMaxTestChain(t, maxTestBalance)
	stubSends(t, func(*preset.NativeReserve) (*preset.NativeReserveResult, error) {
		return &preset.NativeReserveResult{
			Value: big.NewInt(9_000_000_000_000_000), MaxGasCost: big.NewInt(100_000_000_000_000),
			GasReserve: big.NewInt(100_000_000_000_000), LimitedBy: preset.NativeReserveLimitedByBalance,
			Left: big.NewInt(900_000_000_000_000),
		}, nil
	}, nil)
	processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))

	step, err := processor.Execute("send-eth", node)
	require.NoError(t, err)
	_, metadata := stepMaps(t, step)
	require.Equal(t, "900000000000000", metadata["leftInWallet"])
	require.NotContains(t, metadata, "limitedBy")
	require.Contains(t, step.Log, "left 900000000000000 wei in the wallet")
	require.NotContains(t, step.Log, "the most the permission allowed")
}

// storeMaxTestGrant stores an applied Skill grant for the test wallet that
// may send ETH to the destination, with a native limit of granted wei.
func storeMaxTestGrant(t *testing.T, db storage.Storage, granted string) {
	t.Helper()
	owner := testutil.TestUser1().Address
	runner := common.HexToAddress(maxTestWallet)
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	recipient := common.HexToAddress(maxTestDestination)
	require.NoError(t, StoreSessionPolicy(db, &model.SessionPolicy{
		ID: "01sweepgrant", Owner: &owner, Runner: &runner, ChainID: maxTestChain, EntityID: 3,
		SessionSigner: &signer, Status: model.SessionPolicyActive,
		Grant: &model.SessionGrantAuthorization{
			InstallCall: []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01}, CarrierNonce: big.NewInt(1),
			Deadline: uint64(time.Now().Add(time.Hour).Unix()), OwnerSignature: make([]byte, 65),
			AppliedAt: time.Now().UnixMilli(),
		},
		NativeRecipients: []*common.Address{&recipient},
		NativeSpendCap:   &model.NativeSpendCap{Amount: granted, GrantedCap: granted},
	}))
}

// Test, strict or with a grant report, estimates a self-funded MAX the way
// Run sends it: the deposit pays the fee first, and the remaining limit,
// not the balance plus the fee, bounds the value.
func TestETHTransferMaxSimulationWithAStoredGrant(t *testing.T) {
	// An applied grant prices the steady operation: 900,000 gas × 5 gwei.
	const fee = 4_500_000_000_000_000
	type expected struct {
		value, gasReserve, left string
		limitedBy               string
		strictCode              string
		reportMiss              bool
	}
	cases := []struct {
		name                        string
		balance, deposit, remaining int64
		granted                     string
		limitFails                  bool
		want                        expected
	}{
		{
			// The balance alone is below the fee, but the deposit covers it.
			name: "deposit covers the fee", balance: 1_000_000_000_000_000, deposit: 10_000_000_000_000_000,
			remaining: 100_000_000_000_000_000, granted: "100000000000000000",
			want: expected{value: "1000000000000000", gasReserve: "0"},
		},
		{
			// B + G exceeds the granted cap, which the full-balance preflight
			// refused; Run sends what the remaining limit allows.
			name: "permission sets the value", balance: 10_000_000_000_000_000,
			remaining: 8_000_000_000_000_000, granted: "10000000000000000",
			want: expected{value: "3500000000000000", gasReserve: "4500000000000000", left: "2000000000000000", limitedBy: preset.NativeReserveLimitedByPermission},
		},
		{
			name: "permission below the fee", balance: 10_000_000_000_000_000,
			remaining: 1_000_000_000_000_000, granted: "10000000000000000",
			want: expected{value: "5500000000000000", gasReserve: "4500000000000000", strictCode: preset.NativeCapBelowFeeCode, reportMiss: true},
		},
		{
			name: "limit read fails", balance: 10_000_000_000_000_000, granted: "10000000000000000", limitFails: true,
			want: expected{strictCode: "remaining native limit"},
		},
	}
	for _, tc := range cases {
		for _, report := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/strict", true: "/report"}[report], func(t *testing.T) {
				db := testutil.TestMustDB()
				defer storage.Destroy(db.(*storage.BadgerStorage))
				storeMaxTestGrant(t, db, tc.granted)
				chain, client, url := newMaxTestChain(t, big.NewInt(tc.balance))
				chain.deposit, chain.remaining, chain.limitFails = big.NewInt(tc.deposit), big.NewInt(tc.remaining), tc.limitFails
				processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
				processor.vm.WithDb(db).SetSimulation(true)
				if report {
					processor.vm.sessionGrantReport = &SessionGrantReport{}
				}

				step, err := processor.Execute("send-eth", node)
				refused := tc.want.strictCode != "" && (!report || !tc.want.reportMiss)
				if refused {
					require.Error(t, err)
					require.False(t, step.Success)
					require.Contains(t, step.Error, tc.want.strictCode)
					return
				}
				require.NoError(t, err)
				require.True(t, step.Success, step.Error)
				transfer, metadata := stepMaps(t, step)
				require.Equal(t, tc.want.value, transfer["value"])
				require.Equal(t, tc.want.gasReserve, metadata["gasReserve"])
				if tc.want.left == "" {
					require.NotContains(t, metadata, "leftInWallet")
				} else {
					require.Equal(t, tc.want.left, metadata["leftInWallet"])
				}
				if tc.want.limitedBy == "" {
					require.NotContains(t, metadata, "limitedBy")
				} else {
					require.Equal(t, tc.want.limitedBy, metadata["limitedBy"])
				}
				require.Equal(t, big.NewInt(fee).String(), step.TotalGasCost)
				if report {
					_, _, _, _, nativeMiss, nativeDetail, _, nativeSpend, _ := processor.vm.sessionGrantReport.snapshot()
					require.Equal(t, big.NewInt(tc.balance).String(), nativeSpend.String(), "Review still sizes from the whole balance")
					require.Equal(t, tc.want.reportMiss, nativeMiss)
					if tc.want.reportMiss {
						require.True(t, isNativeCapMessage(nativeDetail), nativeDetail)
					}
				}
			})
		}
	}
}

// A loop builds each iteration's output from the step (extractResultData), so
// an ETH loop, such as Sweep's, reports the reserve the way a standalone send
// does (#915).
func TestLoopIterationCarriesTheNativeReserve(t *testing.T) {
	iterationMetadata := func(t *testing.T, step *avsproto.Execution_Step) map[string]interface{} {
		t.Helper()
		output, ok := (&ExecutionQueue{}).extractResultData(step).(map[string]interface{})
		require.True(t, ok)
		require.Contains(t, output, "transfer", "the iteration keeps the transfer object")
		metadata, ok := output["metadata"].(map[string]interface{})
		require.True(t, ok)
		require.NotEmpty(t, metadata["transactionHash"])
		require.NotContains(t, metadata, "gasUsed", "gas figures stay on the step")
		return metadata
	}
	sendWith := func(t *testing.T, sized *preset.NativeReserveResult) *avsproto.Execution_Step {
		t.Helper()
		_, client, url := newMaxTestChain(t, maxTestBalance)
		stubSends(t, func(*preset.NativeReserve) (*preset.NativeReserveResult, error) { return sized, nil }, nil)
		processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
		step, err := processor.Execute("send-eth", node)
		require.NoError(t, err)
		return step
	}
	gasReserve := big.NewInt(400_000_000_000_000)

	t.Run("run, the permission set the value", func(t *testing.T) {
		metadata := iterationMetadata(t, sendWith(t, &preset.NativeReserveResult{
			Value: big.NewInt(9_000_000_000_000_000), MaxGasCost: gasReserve, GasReserve: gasReserve,
			LimitedBy: preset.NativeReserveLimitedByPermission, Left: big.NewInt(600_000_000_000_000),
		}))
		require.Equal(t, gasReserve.String(), metadata[nativeReserveGasReserveKey])
		require.Equal(t, preset.NativeReserveLimitedByPermission, metadata[nativeReserveLimitedByKey])
		require.Equal(t, "600000000000000", metadata[nativeReserveLeftInWalletKey])
	})

	t.Run("run, the balance set the value", func(t *testing.T) {
		metadata := iterationMetadata(t, sendWith(t, &preset.NativeReserveResult{
			Value: new(big.Int).Sub(maxTestBalance, gasReserve), MaxGasCost: gasReserve, GasReserve: gasReserve,
			LimitedBy: preset.NativeReserveLimitedByBalance, Left: big.NewInt(0),
		}))
		require.Equal(t, gasReserve.String(), metadata[nativeReserveGasReserveKey])
		require.NotContains(t, metadata, nativeReserveLimitedByKey)
		require.NotContains(t, metadata, nativeReserveLeftInWalletKey)
	})

	t.Run("simulation", func(t *testing.T) {
		_, client, url := newMaxTestChain(t, maxTestBalance)
		processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
		processor.vm.SetSimulation(true)
		step, err := processor.Execute("send-eth", node)
		require.NoError(t, err)
		fee := new(big.Int).Mul(big.NewInt(nativePreflightGasUnitsSteady), big.NewInt(5_000_000_000))
		require.Equal(t, fee.String(), iterationMetadata(t, step)[nativeReserveGasReserveKey])
	})

	t.Run("a sponsored MAX or a fixed amount carries only the hash", func(t *testing.T) {
		for name, setup := range map[string]func(url string) (*config.SmartWalletConfig, string){
			"sponsored MAX": func(url string) (*config.SmartWalletConfig, string) {
				return &config.SmartWalletConfig{ChainID: maxTestChain, EthRpcUrl: url, AlchemyPaymasterPolicyID: "test-policy", BundlerProvider: config.BundlerProviderAlchemy}, "MAX"
			},
			"self-funded fixed amount": func(url string) (*config.SmartWalletConfig, string) {
				return selfFundedMaxConfig(url), "1000000000000000"
			},
		} {
			t.Run(name, func(t *testing.T) {
				_, client, url := newMaxTestChain(t, maxTestBalance)
				stubSends(t, nil, func([]byte) {})
				cfg, amount := setup(url)
				processor, node := newMaxTestProcessor(t, client, cfg)
				node.Config.Amount = amount
				step, err := processor.Execute("send-eth", node)
				require.NoError(t, err)
				require.True(t, step.Success, step.Error)
				metadata := iterationMetadata(t, step)
				require.Len(t, metadata, 1, "only transactionHash: %v", metadata)
			})
		}
	})
}

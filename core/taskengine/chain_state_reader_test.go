package taskengine

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// chainStateFakeClient is a fully-typed ChainWorkerClient stub for
// workerChainStateReader tests. Each field controls one method's response;
// the test sets only the ones it exercises and leaves others zero.
type chainStateFakeClient struct {
	// SuggestGasPrice
	gasPriceResp *avsproto.WorkerSuggestGasPriceResp
	gasPriceErr  error

	// EstimateGas
	estimateGasReq  *avsproto.WorkerEstimateGasReq
	estimateGasResp *avsproto.WorkerEstimateGasResp
	estimateGasErr  error

	// GetCode
	getCodeReq  *avsproto.WorkerGetCodeReq
	getCodeResp *avsproto.WorkerGetCodeResp
	getCodeErr  error

	// GetNonceByAddress
	getNonceReq  *avsproto.WorkerGetNonceByAddressReq
	getNonceResp *avsproto.WorkerGetNonceResp
	getNonceErr  error
}

func (f *chainStateFakeClient) WorkerHealthCheck(context.Context, *avsproto.WorkerHealthCheckReq, ...grpc.CallOption) (*avsproto.WorkerHealthCheckResp, error) {
	panic("unused")
}
func (f *chainStateFakeClient) ExecuteUserOp(context.Context, *avsproto.ExecuteUserOpReq, ...grpc.CallOption) (*avsproto.ExecuteUserOpResp, error) {
	panic("unused")
}
func (f *chainStateFakeClient) GetNonce(context.Context, *avsproto.WorkerGetNonceReq, ...grpc.CallOption) (*avsproto.WorkerGetNonceResp, error) {
	panic("unused")
}
func (f *chainStateFakeClient) GetSmartWalletAddress(context.Context, *avsproto.WorkerGetSmartWalletAddressReq, ...grpc.CallOption) (*avsproto.WorkerGetSmartWalletAddressResp, error) {
	panic("unused")
}
func (f *chainStateFakeClient) GetTokenMetadata(context.Context, *avsproto.WorkerGetTokenMetadataReq, ...grpc.CallOption) (*avsproto.WorkerGetTokenMetadataResp, error) {
	panic("unused")
}

func (f *chainStateFakeClient) SuggestGasPrice(_ context.Context, _ *avsproto.WorkerSuggestGasPriceReq, _ ...grpc.CallOption) (*avsproto.WorkerSuggestGasPriceResp, error) {
	return f.gasPriceResp, f.gasPriceErr
}
func (f *chainStateFakeClient) EstimateGas(_ context.Context, req *avsproto.WorkerEstimateGasReq, _ ...grpc.CallOption) (*avsproto.WorkerEstimateGasResp, error) {
	f.estimateGasReq = req
	return f.estimateGasResp, f.estimateGasErr
}
func (f *chainStateFakeClient) GetCode(_ context.Context, req *avsproto.WorkerGetCodeReq, _ ...grpc.CallOption) (*avsproto.WorkerGetCodeResp, error) {
	f.getCodeReq = req
	return f.getCodeResp, f.getCodeErr
}
func (f *chainStateFakeClient) GetNonceByAddress(_ context.Context, req *avsproto.WorkerGetNonceByAddressReq, _ ...grpc.CallOption) (*avsproto.WorkerGetNonceResp, error) {
	f.getNonceReq = req
	return f.getNonceResp, f.getNonceErr
}

// TestWorkerChainStateReader_ChainID confirms the worker-routed reader
// returns its constructor-injected chainID without round-tripping to the
// worker — the worker doesn't expose a ChainID RPC and the gateway knows
// the chain a priori.
func TestWorkerChainStateReader_ChainID(t *testing.T) {
	r := NewWorkerChainStateReader(&chainStateFakeClient{}, 11155111, 0)
	id, err := r.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}
	if id != 11155111 {
		t.Fatalf("ChainID: got %d, want 11155111", id)
	}
}

// TestWorkerChainStateReader_ChainID_ZeroErrors: misconfiguration (no
// chainID passed to the constructor) must surface, not silently return 0.
func TestWorkerChainStateReader_ChainID_ZeroErrors(t *testing.T) {
	r := NewWorkerChainStateReader(&chainStateFakeClient{}, 0, 0)
	if _, err := r.ChainID(context.Background()); err == nil {
		t.Fatalf("expected error for missing chainID")
	}
}

// TestWorkerChainStateReader_SuggestGasPrice: happy path returns the
// big.Int parsed from the worker's string response.
func TestWorkerChainStateReader_SuggestGasPrice(t *testing.T) {
	fake := &chainStateFakeClient{
		gasPriceResp: &avsproto.WorkerSuggestGasPriceResp{GasPriceWei: "21000000000"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	got, err := r.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatalf("SuggestGasPrice: %v", err)
	}
	want, _ := new(big.Int).SetString("21000000000", 10)
	if got.Cmp(want) != 0 {
		t.Fatalf("SuggestGasPrice: got %s, want %s", got, want)
	}
}

// TestWorkerChainStateReader_SuggestGasPrice_Malformed: a malformed
// numeric string from the worker must error, not silently coerce to 0.
func TestWorkerChainStateReader_SuggestGasPrice_Malformed(t *testing.T) {
	fake := &chainStateFakeClient{
		gasPriceResp: &avsproto.WorkerSuggestGasPriceResp{GasPriceWei: "not-a-number"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.SuggestGasPrice(context.Background()); err == nil {
		t.Fatalf("expected malformed-gas-price error")
	}
}

// TestWorkerChainStateReader_SuggestGasPrice_Err: gRPC transport error
// propagates with the chain ID in the wrap for triage.
func TestWorkerChainStateReader_SuggestGasPrice_Err(t *testing.T) {
	fake := &chainStateFakeClient{gasPriceErr: errors.New("transport closed")}
	r := NewWorkerChainStateReader(fake, 42, time.Second)
	_, err := r.SuggestGasPrice(context.Background())
	if err == nil || !strings.Contains(err.Error(), "chain 42") {
		t.Fatalf("expected chain-id-wrapped error, got %v", err)
	}
}

// TestWorkerChainStateReader_EstimateGas: confirm proto request payload
// matches CallMsg field-for-field, including the optional From / Value
// fields that the worker treats as chain defaults when empty.
func TestWorkerChainStateReader_EstimateGas(t *testing.T) {
	fake := &chainStateFakeClient{
		estimateGasResp: &avsproto.WorkerEstimateGasResp{Gas: 123456},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	to := common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3")
	from := common.HexToAddress("0xDEADbeef00000000000000000000000000000000")
	msg := ethereum.CallMsg{
		From:  from,
		To:    &to,
		Value: big.NewInt(1000),
		Data:  []byte{0xde, 0xad, 0xbe, 0xef},
	}
	gas, err := r.EstimateGas(context.Background(), msg)
	if err != nil {
		t.Fatalf("EstimateGas: %v", err)
	}
	if gas != 123456 {
		t.Fatalf("EstimateGas: got %d, want 123456", gas)
	}
	if fake.estimateGasReq.To != to.Hex() {
		t.Fatalf("To not propagated: got %q want %q", fake.estimateGasReq.To, to.Hex())
	}
	if fake.estimateGasReq.From != from.Hex() {
		t.Fatalf("From not propagated: got %q want %q", fake.estimateGasReq.From, from.Hex())
	}
	if fake.estimateGasReq.Value != "1000" {
		t.Fatalf("Value not propagated as decimal string: %q", fake.estimateGasReq.Value)
	}
}

// TestWorkerChainStateReader_EstimateGas_NoFromValue: From/Value omitted
// from CallMsg must arrive as empty strings, not "0x0000...0000" or "0".
// The worker side treats empty as "chain default applies".
func TestWorkerChainStateReader_EstimateGas_NoFromValue(t *testing.T) {
	fake := &chainStateFakeClient{
		estimateGasResp: &avsproto.WorkerEstimateGasResp{Gas: 1},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	to := common.HexToAddress("0xaa")
	_, err := r.EstimateGas(context.Background(), ethereum.CallMsg{To: &to, Data: []byte{0x00}})
	if err != nil {
		t.Fatalf("EstimateGas: %v", err)
	}
	if fake.estimateGasReq.From != "" {
		t.Fatalf("From should be empty when CallMsg.From is zero address: %q", fake.estimateGasReq.From)
	}
	if fake.estimateGasReq.Value != "" {
		t.Fatalf("Value should be empty when CallMsg.Value is nil: %q", fake.estimateGasReq.Value)
	}
}

// TestWorkerChainStateReader_CodeAt: the address is hex-encoded for the
// worker; raw bytes come back unchanged.
func TestWorkerChainStateReader_CodeAt(t *testing.T) {
	fake := &chainStateFakeClient{
		getCodeResp: &avsproto.WorkerGetCodeResp{Code: []byte{0x60, 0x80}},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	code, err := r.CodeAt(context.Background(), addr)
	if err != nil {
		t.Fatalf("CodeAt: %v", err)
	}
	if len(code) != 2 || code[0] != 0x60 || code[1] != 0x80 {
		t.Fatalf("CodeAt: unexpected code %x", code)
	}
	if fake.getCodeReq.Address != addr.Hex() {
		t.Fatalf("address not propagated: got %q want %q", fake.getCodeReq.Address, addr.Hex())
	}
}

// TestWorkerChainStateReader_GetEntryPointNonce: nil key defaults to "0",
// non-nil keys serialize as base-10 decimals (matching worker's parse).
func TestWorkerChainStateReader_GetEntryPointNonce(t *testing.T) {
	fake := &chainStateFakeClient{
		getNonceResp: &avsproto.WorkerGetNonceResp{Nonce: "42"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	addr := common.HexToAddress("0xfeedface")
	nonce, err := r.GetEntryPointNonce(context.Background(), addr, nil)
	if err != nil {
		t.Fatalf("GetEntryPointNonce nil key: %v", err)
	}
	if nonce.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("nonce: got %s want 42", nonce)
	}
	if fake.getNonceReq.NonceKey != "0" {
		t.Fatalf("nil key should serialize as \"0\": got %q", fake.getNonceReq.NonceKey)
	}
	if fake.getNonceReq.WalletAddress != addr.Hex() {
		t.Fatalf("wallet not propagated: %q", fake.getNonceReq.WalletAddress)
	}

	// Non-nil key uses base-10 decimal serialization.
	_, _ = r.GetEntryPointNonce(context.Background(), addr, big.NewInt(7))
	if fake.getNonceReq.NonceKey != "7" {
		t.Fatalf("non-nil key should be base-10: got %q", fake.getNonceReq.NonceKey)
	}
}

// TestWorkerChainStateReader_GetEntryPointNonce_Malformed: a non-numeric
// nonce string from the worker is a contract violation; surface it
// rather than silently returning 0.
func TestWorkerChainStateReader_GetEntryPointNonce_Malformed(t *testing.T) {
	fake := &chainStateFakeClient{
		getNonceResp: &avsproto.WorkerGetNonceResp{Nonce: "garbage"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.GetEntryPointNonce(context.Background(), common.HexToAddress("0xab"), nil); err == nil {
		t.Fatalf("expected malformed-nonce error")
	}
}

// TestChainStateReaderRegistry: register-then-lookup happy path + the
// idempotent re-register semantics tokenServiceRegistry guarantees, so
// gateway-mode startup can call us redundantly without leaking state.
func TestChainStateReaderRegistry(t *testing.T) {
	ClearChainStateReaderRegistry()
	defer ClearChainStateReaderRegistry()

	r1 := NewWorkerChainStateReader(&chainStateFakeClient{}, 1, 0)
	RegisterChainStateReader(1, r1)
	if got := GetChainStateReaderForChain(1); got != r1 {
		t.Fatalf("registered reader not returned")
	}

	// Re-register replaces.
	r2 := NewWorkerChainStateReader(&chainStateFakeClient{}, 1, 0)
	RegisterChainStateReader(1, r2)
	if got := GetChainStateReaderForChain(1); got != r2 {
		t.Fatalf("re-register should replace, got prior reader")
	}

	// chainID 0 → nil; unregistered chain → nil; nil reader rejected.
	if GetChainStateReaderForChain(0) != nil {
		t.Fatalf("chainID 0 should return nil")
	}
	if GetChainStateReaderForChain(99) != nil {
		t.Fatalf("unregistered chain should return nil")
	}
	RegisterChainStateReader(2, nil)
	if GetChainStateReaderForChain(2) != nil {
		t.Fatalf("nil reader should be rejected")
	}
}

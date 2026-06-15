package taskengine

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
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

	// GetBalance
	getBalanceReq  *avsproto.WorkerGetBalanceReq
	getBalanceResp *avsproto.WorkerGetBalanceResp
	getBalanceErr  error

	// GetTokenBalance
	getTokenBalanceReq  *avsproto.WorkerGetTokenBalanceReq
	getTokenBalanceResp *avsproto.WorkerGetTokenBalanceResp
	getTokenBalanceErr  error

	// GetSmartWalletAddress
	getSmartWalletReq  *avsproto.WorkerGetSmartWalletAddressReq
	getSmartWalletResp *avsproto.WorkerGetSmartWalletAddressResp
	getSmartWalletErr  error

	// CallContract
	callContractReq  *avsproto.WorkerCallContractReq
	callContractResp *avsproto.WorkerCallContractResp
	callContractErr  error

	// GetBlockHeader
	getBlockHeaderReq  *avsproto.WorkerGetBlockHeaderReq
	getBlockHeaderResp *avsproto.WorkerGetBlockHeaderResp
	getBlockHeaderErr  error

	// GetBlockNumber
	getBlockNumberResp *avsproto.WorkerGetBlockNumberResp
	getBlockNumberErr  error

	// FindMatchingWalletSalt
	findSaltReq  *avsproto.WorkerFindMatchingWalletSaltReq
	findSaltResp *avsproto.WorkerFindMatchingWalletSaltResp
	findSaltErr  error

	// GetTransactionReceipt
	getReceiptReq  *avsproto.WorkerGetTransactionReceiptReq
	getReceiptResp *avsproto.WorkerGetTransactionReceiptResp
	getReceiptErr  error
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
func (f *chainStateFakeClient) GetSmartWalletAddress(_ context.Context, req *avsproto.WorkerGetSmartWalletAddressReq, _ ...grpc.CallOption) (*avsproto.WorkerGetSmartWalletAddressResp, error) {
	f.getSmartWalletReq = req
	return f.getSmartWalletResp, f.getSmartWalletErr
}
func (f *chainStateFakeClient) CallContract(_ context.Context, req *avsproto.WorkerCallContractReq, _ ...grpc.CallOption) (*avsproto.WorkerCallContractResp, error) {
	f.callContractReq = req
	return f.callContractResp, f.callContractErr
}
func (f *chainStateFakeClient) GetBlockHeader(_ context.Context, req *avsproto.WorkerGetBlockHeaderReq, _ ...grpc.CallOption) (*avsproto.WorkerGetBlockHeaderResp, error) {
	f.getBlockHeaderReq = req
	return f.getBlockHeaderResp, f.getBlockHeaderErr
}
func (f *chainStateFakeClient) GetBlockNumber(_ context.Context, _ *avsproto.WorkerGetBlockNumberReq, _ ...grpc.CallOption) (*avsproto.WorkerGetBlockNumberResp, error) {
	return f.getBlockNumberResp, f.getBlockNumberErr
}
func (f *chainStateFakeClient) FindMatchingWalletSalt(_ context.Context, req *avsproto.WorkerFindMatchingWalletSaltReq, _ ...grpc.CallOption) (*avsproto.WorkerFindMatchingWalletSaltResp, error) {
	f.findSaltReq = req
	return f.findSaltResp, f.findSaltErr
}
func (f *chainStateFakeClient) GetTransactionReceipt(_ context.Context, req *avsproto.WorkerGetTransactionReceiptReq, _ ...grpc.CallOption) (*avsproto.WorkerGetTransactionReceiptResp, error) {
	f.getReceiptReq = req
	return f.getReceiptResp, f.getReceiptErr
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
func (f *chainStateFakeClient) GetBalance(_ context.Context, req *avsproto.WorkerGetBalanceReq, _ ...grpc.CallOption) (*avsproto.WorkerGetBalanceResp, error) {
	f.getBalanceReq = req
	return f.getBalanceResp, f.getBalanceErr
}
func (f *chainStateFakeClient) GetTokenBalance(_ context.Context, req *avsproto.WorkerGetTokenBalanceReq, _ ...grpc.CallOption) (*avsproto.WorkerGetTokenBalanceResp, error) {
	f.getTokenBalanceReq = req
	return f.getTokenBalanceResp, f.getTokenBalanceErr
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

// TestWorkerChainStateReader_EstimateGas_NilToRejected: gateway-side
// callers always have a destination (factory address / contract address).
// A nil msg.To means the caller has a bug; we'd rather surface it than
// silently forward as a deployment estimate (which is what the worker
// server's CallMsg{To: nil} would imply to ethclient).
func TestWorkerChainStateReader_EstimateGas_NilToRejected(t *testing.T) {
	r := NewWorkerChainStateReader(&chainStateFakeClient{}, 1, time.Second)
	_, err := r.EstimateGas(context.Background(), ethereum.CallMsg{Data: []byte{0xaa}})
	if err == nil {
		t.Fatalf("expected error for nil msg.To")
	}
	if !strings.Contains(err.Error(), "msg.To is required") {
		t.Fatalf("error should mention msg.To: got %v", err)
	}
}

// TestDirectChainStateReader_ChainID_FromConstructor: the constructor
// chainID short-circuits the eth_chainId RPC. Important because the
// gateway always supplies a known chainID via the chain config.
func TestDirectChainStateReader_ChainID_FromConstructor(t *testing.T) {
	r := NewDirectChainStateReader(nil, 8453)
	id, err := r.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}
	if id != 8453 {
		t.Fatalf("ChainID: got %d, want 8453", id)
	}
}

// TestDirectChainStateReader_ChainID_RaceSafe spins up N concurrent
// callers on a single direct reader and confirms only one of them
// races to a successful ChainID read. The lazy detect path used to
// have an unsynchronized read+write — sync.Once now guards it.
// Tested via the same reader instance across goroutines, which is
// how the global registry exposes it.
func TestDirectChainStateReader_ChainID_RaceSafe(t *testing.T) {
	// We can't construct an ethclient.Client without a network, so
	// we just confirm the cached-chainID path (constructor != 0)
	// is safe under concurrency — that's the production hot path
	// for direct readers, since chainID=0 only ever happens in the
	// global-fallback init in task_engine.go, and even there the
	// detected value is registered as a fully-formed reader.
	r := NewDirectChainStateReader(nil, 11155111)
	var wg sync.WaitGroup
	const N = 32
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			id, err := r.ChainID(context.Background())
			if err != nil || id != 11155111 {
				t.Errorf("concurrent ChainID: got (%d, %v) want (11155111, nil)", id, err)
			}
		}()
	}
	wg.Wait()
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

// TestWorkerChainStateReader_GetBalance: the address is hex-encoded for the
// worker; the wei balance round-trips through a base-10 string.
func TestWorkerChainStateReader_GetBalance(t *testing.T) {
	fake := &chainStateFakeClient{
		getBalanceResp: &avsproto.WorkerGetBalanceResp{BalanceWei: "1000000000000000000"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	balance, err := r.GetBalance(context.Background(), addr)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	want, _ := new(big.Int).SetString("1000000000000000000", 10)
	if balance.Cmp(want) != 0 {
		t.Fatalf("balance: got %s want %s", balance, want)
	}
	if fake.getBalanceReq.Address != addr.Hex() {
		t.Fatalf("address not propagated: got %q want %q", fake.getBalanceReq.Address, addr.Hex())
	}
}

// TestWorkerChainStateReader_GetBalance_Malformed: a non-numeric balance
// string is a worker contract violation; surface it rather than return 0.
func TestWorkerChainStateReader_GetBalance_Malformed(t *testing.T) {
	fake := &chainStateFakeClient{
		getBalanceResp: &avsproto.WorkerGetBalanceResp{BalanceWei: "not-a-number"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.GetBalance(context.Background(), common.HexToAddress("0xab")); err == nil {
		t.Fatalf("expected malformed-balance error")
	}
}

// TestWorkerChainStateReader_GetTokenBalance: both token and owner addresses
// are hex-encoded for the worker; the raw balance round-trips via base-10.
func TestWorkerChainStateReader_GetTokenBalance(t *testing.T) {
	fake := &chainStateFakeClient{
		getTokenBalanceResp: &avsproto.WorkerGetTokenBalanceResp{Balance: "500000"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	token := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	owner := common.HexToAddress("0x1234567890123456789012345678901234567890")
	balance, err := r.GetTokenBalance(context.Background(), token, owner)
	if err != nil {
		t.Fatalf("GetTokenBalance: %v", err)
	}
	if balance.Cmp(big.NewInt(500000)) != 0 {
		t.Fatalf("token balance: got %s want 500000", balance)
	}
	if fake.getTokenBalanceReq.TokenAddress != token.Hex() {
		t.Fatalf("token not propagated: got %q want %q", fake.getTokenBalanceReq.TokenAddress, token.Hex())
	}
	if fake.getTokenBalanceReq.OwnerAddress != owner.Hex() {
		t.Fatalf("owner not propagated: got %q want %q", fake.getTokenBalanceReq.OwnerAddress, owner.Hex())
	}
}

// TestWorkerChainStateReader_GetTokenBalance_Malformed: a non-numeric token
// balance string is a worker contract violation; surface it.
func TestWorkerChainStateReader_GetTokenBalance_Malformed(t *testing.T) {
	fake := &chainStateFakeClient{
		getTokenBalanceResp: &avsproto.WorkerGetTokenBalanceResp{Balance: "garbage"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.GetTokenBalance(context.Background(), common.HexToAddress("0xaa"), common.HexToAddress("0xbb")); err == nil {
		t.Fatalf("expected malformed-token-balance error")
	}
}

// TestWorkerChainStateReader_GetSmartWalletAddress: owner/factory are
// hex-encoded and the salt serializes as a base-10 string; a valid hex
// address round-trips back.
func TestWorkerChainStateReader_GetSmartWalletAddress(t *testing.T) {
	want := common.HexToAddress("0xABCdEF0123456789012345678901234567890123")
	fake := &chainStateFakeClient{
		getSmartWalletResp: &avsproto.WorkerGetSmartWalletAddressResp{Address: want.Hex()},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	got, err := r.GetSmartWalletAddress(context.Background(), owner, factory, big.NewInt(7))
	if err != nil {
		t.Fatalf("GetSmartWalletAddress: %v", err)
	}
	if got != want {
		t.Fatalf("address: got %s want %s", got.Hex(), want.Hex())
	}
	if fake.getSmartWalletReq.Owner != owner.Hex() {
		t.Fatalf("owner not propagated: %q", fake.getSmartWalletReq.Owner)
	}
	if fake.getSmartWalletReq.FactoryAddress != factory.Hex() {
		t.Fatalf("factory not propagated: %q", fake.getSmartWalletReq.FactoryAddress)
	}
	if fake.getSmartWalletReq.Salt != "7" {
		t.Fatalf("salt should serialize base-10: got %q", fake.getSmartWalletReq.Salt)
	}
}

// TestWorkerChainStateReader_GetSmartWalletAddress_NilSalt: a nil salt
// serializes as "0" rather than panicking.
func TestWorkerChainStateReader_GetSmartWalletAddress_NilSalt(t *testing.T) {
	fake := &chainStateFakeClient{
		getSmartWalletResp: &avsproto.WorkerGetSmartWalletAddressResp{
			Address: "0x2222222222222222222222222222222222222222",
		},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.GetSmartWalletAddress(context.Background(), common.HexToAddress("0xaa"), common.HexToAddress("0xbb"), nil); err != nil {
		t.Fatalf("nil salt: %v", err)
	}
	if fake.getSmartWalletReq.Salt != "0" {
		t.Fatalf("nil salt should serialize as \"0\": got %q", fake.getSmartWalletReq.Salt)
	}
}

// TestWorkerChainStateReader_GetSmartWalletAddress_Malformed: a non-address
// string from the worker is a contract violation; surface it.
func TestWorkerChainStateReader_GetSmartWalletAddress_Malformed(t *testing.T) {
	fake := &chainStateFakeClient{
		getSmartWalletResp: &avsproto.WorkerGetSmartWalletAddressResp{Address: "not-an-address"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.GetSmartWalletAddress(context.Background(), common.HexToAddress("0xaa"), common.HexToAddress("0xbb"), big.NewInt(0)); err == nil {
		t.Fatalf("expected malformed-address error")
	}
}

// TestWorkerChainStateReader_CallContract: To/Data propagate; From/Value/
// block default to empty when unset; raw result bytes round-trip.
func TestWorkerChainStateReader_CallContract(t *testing.T) {
	fake := &chainStateFakeClient{
		callContractResp: &avsproto.WorkerCallContractResp{Result: []byte{0x01, 0x02, 0x03}},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	to := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	out, err := r.CallContract(context.Background(), ethereum.CallMsg{To: &to, Data: []byte{0xab}}, nil)
	if err != nil {
		t.Fatalf("CallContract: %v", err)
	}
	if len(out) != 3 || out[0] != 0x01 {
		t.Fatalf("unexpected result %x", out)
	}
	if fake.callContractReq.To != to.Hex() {
		t.Fatalf("To not propagated: %q", fake.callContractReq.To)
	}
	if len(fake.callContractReq.Data) != 1 || fake.callContractReq.Data[0] != 0xab {
		t.Fatalf("Data not propagated: %x", fake.callContractReq.Data)
	}
	if fake.callContractReq.From != "" {
		t.Fatalf("From should be empty: %q", fake.callContractReq.From)
	}
	if fake.callContractReq.Value != "" {
		t.Fatalf("Value should be empty: %q", fake.callContractReq.Value)
	}
	if fake.callContractReq.BlockNumber != "" {
		t.Fatalf("BlockNumber should be empty: %q", fake.callContractReq.BlockNumber)
	}
}

// TestWorkerChainStateReader_CallContract_FieldsPropagate: From/Value/block
// serialize when set.
func TestWorkerChainStateReader_CallContract_FieldsPropagate(t *testing.T) {
	fake := &chainStateFakeClient{callContractResp: &avsproto.WorkerCallContractResp{}}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	to := common.HexToAddress("0x00")
	from := common.HexToAddress("0xDEADBEEF00000000000000000000000000000000")
	_, err := r.CallContract(context.Background(),
		ethereum.CallMsg{To: &to, From: from, Value: big.NewInt(99), Data: []byte{0x1}}, big.NewInt(12345))
	if err != nil {
		t.Fatalf("CallContract: %v", err)
	}
	if fake.callContractReq.From != from.Hex() {
		t.Fatalf("From: %q", fake.callContractReq.From)
	}
	if fake.callContractReq.Value != "99" {
		t.Fatalf("Value: %q", fake.callContractReq.Value)
	}
	if fake.callContractReq.BlockNumber != "12345" {
		t.Fatalf("BlockNumber: %q", fake.callContractReq.BlockNumber)
	}
}

// TestWorkerChainStateReader_CallContract_NilToRejected: a nil destination
// (deployment probe) is a caller bug the contractRead path never makes.
func TestWorkerChainStateReader_CallContract_NilToRejected(t *testing.T) {
	r := NewWorkerChainStateReader(&chainStateFakeClient{}, 1, time.Second)
	if _, err := r.CallContract(context.Background(), ethereum.CallMsg{Data: []byte{0x1}}, nil); err == nil {
		t.Fatalf("expected error for nil msg.To")
	}
}

// TestWorkerChainStateReader_HeaderByNumber: a specific block number
// serializes as base-10; the worker's number/hash/time map into BlockHeader.
func TestWorkerChainStateReader_HeaderByNumber(t *testing.T) {
	hash := common.HexToHash("0xabc123")
	parent := common.HexToHash("0xdef456")
	fake := &chainStateFakeClient{
		getBlockHeaderResp: &avsproto.WorkerGetBlockHeaderResp{
			Number:     19000000,
			Hash:       hash.Hex(),
			Time:       1700000000,
			ParentHash: parent.Hex(),
			Difficulty: "12345",
			GasLimit:   30000000,
			GasUsed:    15000000,
		},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	h, err := r.HeaderByNumber(context.Background(), big.NewInt(19000000))
	if err != nil {
		t.Fatalf("HeaderByNumber: %v", err)
	}
	if h.Number != 19000000 || h.Hash != hash || h.Time != 1700000000 {
		t.Fatalf("unexpected header %+v", h)
	}
	if h.ParentHash != parent || h.Difficulty.Cmp(big.NewInt(12345)) != 0 || h.GasLimit != 30000000 || h.GasUsed != 15000000 {
		t.Fatalf("extended header fields not mapped: %+v", h)
	}
	if fake.getBlockHeaderReq.BlockNumber != "19000000" {
		t.Fatalf("block number not propagated: %q", fake.getBlockHeaderReq.BlockNumber)
	}
}

// TestRequireChainIDFromConfig: chain_id is mandatory with no engine-default
// fallback — absent, zero, or wrong-typed values are hard errors; valid
// numeric types (int64 / float64 from a structpb round-trip) parse.
func TestRequireChainIDFromConfig(t *testing.T) {
	if _, err := requireChainIDFromConfig(map[string]interface{}{}); err == nil {
		t.Fatalf("absent chain_id should error")
	}
	if _, err := requireChainIDFromConfig(map[string]interface{}{"chain_id": int64(0)}); err == nil {
		t.Fatalf("zero chain_id should error")
	}
	if _, err := requireChainIDFromConfig(map[string]interface{}{"chain_id": "11155111"}); err == nil {
		t.Fatalf("string chain_id should error (unexpected type)")
	}
	got, err := requireChainIDFromConfig(map[string]interface{}{"chain_id": int64(11155111)})
	if err != nil || got != 11155111 {
		t.Fatalf("int64 chain_id: got %d err %v", got, err)
	}
	got, err = requireChainIDFromConfig(map[string]interface{}{"chain_id": float64(8453)})
	if err != nil || got != 8453 {
		t.Fatalf("float64 chain_id: got %d err %v", got, err)
	}
}

// TestWorkerChainStateReader_HeaderByNumber_Latest: nil number sends an
// empty block_number string (worker treats empty as "latest").
func TestWorkerChainStateReader_HeaderByNumber_Latest(t *testing.T) {
	fake := &chainStateFakeClient{
		getBlockHeaderResp: &avsproto.WorkerGetBlockHeaderResp{
			Number: 1, Hash: "0x01", Time: 1, ParentHash: "0x00", Difficulty: "0",
		},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.HeaderByNumber(context.Background(), nil); err != nil {
		t.Fatalf("HeaderByNumber latest: %v", err)
	}
	if fake.getBlockHeaderReq.BlockNumber != "" {
		t.Fatalf("nil number should send empty block_number: got %q", fake.getBlockHeaderReq.BlockNumber)
	}
}

// TestWorkerChainStateReader_HeaderByNumber_IncompleteRejected: an empty
// hash (worker/gateway version skew) must hard-fail, not coerce to the
// zero hash and propagate an incorrect block-trigger output.
func TestWorkerChainStateReader_HeaderByNumber_IncompleteRejected(t *testing.T) {
	fake := &chainStateFakeClient{
		getBlockHeaderResp: &avsproto.WorkerGetBlockHeaderResp{Number: 1, Hash: "", ParentHash: "0x00"},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	if _, err := r.HeaderByNumber(context.Background(), nil); err == nil {
		t.Fatalf("expected error for empty hash")
	}
}

// TestWorkerChainStateReader_GetBlockNumber: the worker's latest block
// number is returned verbatim.
func TestWorkerChainStateReader_GetBlockNumber(t *testing.T) {
	fake := &chainStateFakeClient{
		getBlockNumberResp: &avsproto.WorkerGetBlockNumberResp{Number: 21000000},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	n, err := r.GetBlockNumber(context.Background())
	if err != nil {
		t.Fatalf("GetBlockNumber: %v", err)
	}
	if n != 21000000 {
		t.Fatalf("block number: got %d want 21000000", n)
	}
}

// TestWorkerChainStateReader_GetBlockNumber_Err: a transport error wraps
// the chain ID for triage.
func TestWorkerChainStateReader_GetBlockNumber_Err(t *testing.T) {
	fake := &chainStateFakeClient{getBlockNumberErr: errors.New("transport closed")}
	r := NewWorkerChainStateReader(fake, 99, time.Second)
	if _, err := r.GetBlockNumber(context.Background()); err == nil || !strings.Contains(err.Error(), "chain 99") {
		t.Fatalf("expected chain-id-wrapped error, got %v", err)
	}
}

// TestChainReaderForEnrichment: best-effort enrichment resolves a reader
// only for a registered, positive chain ID; an unspecified (<=0) or
// unregistered chain returns nil ("skip enrichment", never a default).
func TestChainReaderForEnrichment(t *testing.T) {
	ClearChainStateReaderRegistry()
	defer ClearChainStateReaderRegistry()

	if chainReaderForEnrichment(0) != nil {
		t.Fatalf("chainID 0 should skip (nil reader)")
	}
	if chainReaderForEnrichment(-1) != nil {
		t.Fatalf("negative chainID should skip (nil reader)")
	}
	if chainReaderForEnrichment(8453) != nil {
		t.Fatalf("unregistered chain should be nil")
	}
	RegisterChainStateReader(8453, NewWorkerChainStateReader(&chainStateFakeClient{}, 8453, 0))
	if chainReaderForEnrichment(8453) == nil {
		t.Fatalf("registered chain should resolve a reader")
	}
}

// TestWorkerChainStateReader_FindMatchingWalletSalt: owner/factory/target
// are hex-encoded, max_salts propagates, and (found, salt) round-trips.
func TestWorkerChainStateReader_FindMatchingWalletSalt(t *testing.T) {
	fake := &chainStateFakeClient{
		findSaltResp: &avsproto.WorkerFindMatchingWalletSaltResp{Found: true, Salt: 3},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	target := common.HexToAddress("0x3333333333333333333333333333333333333333")
	found, salt, err := r.FindMatchingWalletSalt(context.Background(), owner, factory, target, 2000)
	if err != nil {
		t.Fatalf("FindMatchingWalletSalt: %v", err)
	}
	if !found || salt != 3 {
		t.Fatalf("got found=%v salt=%d, want true/3", found, salt)
	}
	if fake.findSaltReq.Owner != owner.Hex() || fake.findSaltReq.FactoryAddress != factory.Hex() || fake.findSaltReq.TargetAddress != target.Hex() {
		t.Fatalf("address args not propagated: %+v", fake.findSaltReq)
	}
	if fake.findSaltReq.MaxSalts != 2000 {
		t.Fatalf("max_salts not propagated: %d", fake.findSaltReq.MaxSalts)
	}
}

// TestWorkerChainStateReader_FindMatchingWalletSalt_NotFound: a no-match
// scan returns found=false without error.
func TestWorkerChainStateReader_FindMatchingWalletSalt_NotFound(t *testing.T) {
	fake := &chainStateFakeClient{
		findSaltResp: &avsproto.WorkerFindMatchingWalletSaltResp{Found: false},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	found, _, err := r.FindMatchingWalletSalt(context.Background(), common.HexToAddress("0xaa"), common.HexToAddress("0xbb"), common.HexToAddress("0xcc"), 5)
	if err != nil || found {
		t.Fatalf("expected not-found, no error; got found=%v err=%v", found, err)
	}
}

// TestWorkerChainStateReader_GetTransactionReceipt: a found receipt maps its
// gas/status/block fields; tx hash propagates.
func TestWorkerChainStateReader_GetTransactionReceipt(t *testing.T) {
	fake := &chainStateFakeClient{
		getReceiptResp: &avsproto.WorkerGetTransactionReceiptResp{
			Found: true, Status: 1, GasUsed: 21000,
			EffectiveGasPrice: "1500000000", BlockNumber: 19000000,
			TxHash: "0xabc",
		},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	txHash := common.HexToHash("0xdeadbeef")
	receipt, err := r.GetTransactionReceipt(context.Background(), txHash)
	if err != nil {
		t.Fatalf("GetTransactionReceipt: %v", err)
	}
	if receipt == nil || receipt.Status != 1 || receipt.GasUsed != 21000 {
		t.Fatalf("unexpected receipt %+v", receipt)
	}
	if receipt.EffectiveGasPrice == nil || receipt.EffectiveGasPrice.Cmp(big.NewInt(1500000000)) != 0 {
		t.Fatalf("effective gas price not mapped: %v", receipt.EffectiveGasPrice)
	}
	if fake.getReceiptReq.TxHash != txHash.Hex() {
		t.Fatalf("tx hash not propagated: %q", fake.getReceiptReq.TxHash)
	}
}

// TestWorkerChainStateReader_GetTransactionReceipt_Pending: found=false maps
// to (nil, nil) so the caller's polling loop keeps waiting.
func TestWorkerChainStateReader_GetTransactionReceipt_Pending(t *testing.T) {
	fake := &chainStateFakeClient{
		getReceiptResp: &avsproto.WorkerGetTransactionReceiptResp{Found: false},
	}
	r := NewWorkerChainStateReader(fake, 1, time.Second)
	receipt, err := r.GetTransactionReceipt(context.Background(), common.HexToHash("0xaa"))
	if err != nil || receipt != nil {
		t.Fatalf("pending should be (nil, nil); got receipt=%v err=%v", receipt, err)
	}
}

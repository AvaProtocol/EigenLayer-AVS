package taskengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/grpc"
)

// fakeChainWorkerClient is a minimal stub implementing avsproto.ChainWorkerClient
// for tests. Only GetTokenMetadata is exercised here — all other methods panic
// to surface accidental usage.
type fakeChainWorkerClient struct {
	resp *avsproto.WorkerGetTokenMetadataResp
	err  error
	// gotAddr records the address the test asked for so assertions can verify
	// the fetcher passed it through unchanged.
	gotAddr string
}

func (f *fakeChainWorkerClient) GetTokenMetadata(
	_ context.Context,
	req *avsproto.WorkerGetTokenMetadataReq,
	_ ...grpc.CallOption,
) (*avsproto.WorkerGetTokenMetadataResp, error) {
	f.gotAddr = req.ContractAddress
	return f.resp, f.err
}

// Everything else on ChainWorkerClient is unused in this test; panic to
// catch surprises if tests accidentally use them.
func (f *fakeChainWorkerClient) WorkerHealthCheck(context.Context, *avsproto.WorkerHealthCheckReq, ...grpc.CallOption) (*avsproto.WorkerHealthCheckResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) ExecuteUserOp(context.Context, *avsproto.ExecuteUserOpReq, ...grpc.CallOption) (*avsproto.ExecuteUserOpResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) GetNonce(context.Context, *avsproto.WorkerGetNonceReq, ...grpc.CallOption) (*avsproto.WorkerGetNonceResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) GetSmartWalletAddress(context.Context, *avsproto.WorkerGetSmartWalletAddressReq, ...grpc.CallOption) (*avsproto.WorkerGetSmartWalletAddressResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) GetNonceByAddress(context.Context, *avsproto.WorkerGetNonceByAddressReq, ...grpc.CallOption) (*avsproto.WorkerGetNonceResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) SuggestGasPrice(context.Context, *avsproto.WorkerSuggestGasPriceReq, ...grpc.CallOption) (*avsproto.WorkerSuggestGasPriceResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) EstimateGas(context.Context, *avsproto.WorkerEstimateGasReq, ...grpc.CallOption) (*avsproto.WorkerEstimateGasResp, error) {
	panic("unused")
}
func (f *fakeChainWorkerClient) GetCode(context.Context, *avsproto.WorkerGetCodeReq, ...grpc.CallOption) (*avsproto.WorkerGetCodeResp, error) {
	panic("unused")
}

// TestWorkerRoutedFetcher_HappyPath confirms a successful worker response
// gets mapped to a TokenMetadata correctly — name, symbol, decimals, source
// pass through; Id is lowercased to match the rpc-fetcher contract.
func TestWorkerRoutedFetcher_HappyPath(t *testing.T) {
	fake := &fakeChainWorkerClient{
		resp: &avsproto.WorkerGetTokenMetadataResp{
			Name:     "USD Coin",
			Symbol:   "USDC",
			Decimals: 6,
			Found:    true,
			Source:   "whitelist",
		},
	}
	fetcher := NewWorkerRoutedFetcher(fake, 5*time.Second)
	md, err := fetcher("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md == nil {
		t.Fatalf("expected metadata, got nil")
	}
	if md.Name != "USD Coin" || md.Symbol != "USDC" || md.Decimals != 6 || md.Source != "whitelist" {
		t.Fatalf("unexpected metadata: %+v", md)
	}
	want := strings.ToLower("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	if md.Id != want {
		t.Fatalf("Id should be lowercased: got %q want %q", md.Id, want)
	}
	if fake.gotAddr != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" {
		t.Fatalf("fetcher should pass address through unchanged: got %q", fake.gotAddr)
	}
}

// TestWorkerRoutedFetcher_NotFound: worker says Found=false → fetcher returns
// (nil, nil) so TokenEnrichmentService can treat it as a cache miss and fall
// back to other sources (Moralis etc.) instead of surfacing a synthetic error.
func TestWorkerRoutedFetcher_NotFound(t *testing.T) {
	fake := &fakeChainWorkerClient{
		resp: &avsproto.WorkerGetTokenMetadataResp{Found: false},
	}
	fetcher := NewWorkerRoutedFetcher(fake, 5*time.Second)
	md, err := fetcher("0xdeadbeef")
	if err != nil {
		t.Fatalf("unexpected error for not-found: %v", err)
	}
	if md != nil {
		t.Fatalf("expected nil metadata for not-found, got %+v", md)
	}
}

// TestWorkerRoutedFetcher_WorkerError: gRPC error must bubble up as an error,
// not silently mask as a "not found".
func TestWorkerRoutedFetcher_WorkerError(t *testing.T) {
	fake := &fakeChainWorkerClient{err: errors.New("transport closed")}
	fetcher := NewWorkerRoutedFetcher(fake, 5*time.Second)
	_, err := fetcher("0xabc")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "transport closed") {
		t.Fatalf("error should wrap underlying cause: %v", err)
	}
}

// TestWorkerRoutedFetcher_NilClient: defensive guard so a misconfigured
// fetcher (constructed before chainRegistry connected, say) fails loudly
// instead of nil-panicking.
func TestWorkerRoutedFetcher_NilClient(t *testing.T) {
	fetcher := NewWorkerRoutedFetcher(nil, 5*time.Second)
	_, err := fetcher("0xabc")
	if err == nil || !strings.Contains(err.Error(), "nil client") {
		t.Fatalf("expected nil-client error, got: %v", err)
	}
}

// TestTokenEnrichmentService_WorkerRouted plugs a worker-routed fetcher into
// the full TokenEnrichmentService stack and verifies the cache-miss path
// invokes the fetcher (not RPC) and the result gets cached.
func TestTokenEnrichmentService_WorkerRouted(t *testing.T) {
	fake := &fakeChainWorkerClient{
		resp: &avsproto.WorkerGetTokenMetadataResp{
			Name:     "USD Coin",
			Symbol:   "USDC",
			Decimals: 6,
			Found:    true,
			Source:   "whitelist",
		},
	}
	fetcher := NewWorkerRoutedFetcher(fake, 5*time.Second)
	svc, err := NewWorkerRoutedTokenEnrichmentService(11155111, fetcher, nil)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	addr := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	md, err := svc.GetTokenMetadata(addr)
	if err != nil {
		t.Fatalf("GetTokenMetadata: %v", err)
	}
	if md == nil || md.Symbol != "USDC" {
		t.Fatalf("first call should return worker-fetched metadata, got %+v", md)
	}

	// Second call: should hit cache, NOT the fetcher.
	fake.gotAddr = ""
	md2, err := svc.GetTokenMetadata(addr)
	if err != nil {
		t.Fatalf("GetTokenMetadata cached: %v", err)
	}
	if md2.Symbol != "USDC" {
		t.Fatalf("cached result mismatch: %+v", md2)
	}
	if fake.gotAddr != "" {
		t.Fatalf("second call should be served from cache, but fetcher was invoked with %q", fake.gotAddr)
	}
}

// TestNewWorkerRoutedTokenEnrichmentService_Validates: constructor must
// reject zero chainID or nil fetcher rather than silently produce a
// whitelist-only service that returns nil on every lookup.
func TestNewWorkerRoutedTokenEnrichmentService_Validates(t *testing.T) {
	if _, err := NewWorkerRoutedTokenEnrichmentService(0, func(string) (*TokenMetadata, error) { return nil, nil }, nil); err == nil {
		t.Fatalf("expected error for zero chainID")
	}
	if _, err := NewWorkerRoutedTokenEnrichmentService(1, nil, nil); err == nil {
		t.Fatalf("expected error for nil fetcher")
	}
}

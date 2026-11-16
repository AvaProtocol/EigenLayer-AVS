package taskengine

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestExtractTxHashFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata *structpb.Value
		want     string
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			want:     "",
		},
		{
			name: "valid transactionHash in first result",
			metadata: mustNewValue(t, []any{
				map[string]any{
					"methodName": "transfer",
					"success":    true,
					"receipt": map[string]any{
						"transactionHash": "0xabc123def4560000000000000000000000000000000000000000000000000001",
						"blockNumber":     "0x1",
					},
				},
			}),
			want: "0xabc123def4560000000000000000000000000000000000000000000000000001",
		},
		{
			name: "no receipt in result",
			metadata: mustNewValue(t, []any{
				map[string]any{
					"methodName": "transfer",
					"success":    true,
				},
			}),
			want: "",
		},
		{
			name:     "empty results array",
			metadata: mustNewValue(t, []any{}),
			want:     "",
		},
		{
			name:     "null value",
			metadata: structpb.NewNullValue(),
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTxHashFromMetadata(tt.metadata)
			if got != tt.want {
				t.Errorf("extractTxHashFromMetadata() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEthIterationMetadata(t *testing.T) {
	const hash = "0xabc123def4560000000000000000000000000000000000000000000000000001"
	tests := []struct {
		name     string
		metadata *structpb.Value
		want     map[string]interface{}
	}{
		{name: "nil metadata", metadata: nil, want: map[string]interface{}{}},
		{name: "null value", metadata: structpb.NewNullValue(), want: map[string]interface{}{}},
		{name: "empty object", metadata: mustNewValue(t, map[string]any{}), want: map[string]interface{}{}},
		{
			name:     "transactionHash only",
			metadata: mustNewValue(t, map[string]any{"transactionHash": hash}),
			want:     map[string]interface{}{"transactionHash": hash},
		},
		{
			name: "gas figures stay on the step",
			metadata: mustNewValue(t, map[string]any{
				"transactionHash": hash,
				"gasUsed":         "21000",
				"gasPrice":        "1000000000",
				"totalGasCost":    "21000000000000",
			}),
			want: map[string]interface{}{"transactionHash": hash},
		},
		{
			name: "a self-funded MAX's reserve fields",
			metadata: mustNewValue(t, map[string]any{
				"transactionHash": hash,
				"gasUsed":         "100000",
				"gasReserve":      "400000000000000",
				"limitedBy":       "permission",
				"leftInWallet":    "600000000000000",
			}),
			want: map[string]interface{}{
				"transactionHash": hash,
				"gasReserve":      "400000000000000",
				"limitedBy":       "permission",
				"leftInWallet":    "600000000000000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ethIterationMetadata(tt.metadata); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ethIterationMetadata() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWrapResultWithMetadataTxHash(t *testing.T) {
	tests := []struct {
		name       string
		resultData interface{}
		txHash     string
		wantNil    bool
		wantTxHash string
		wantData   bool // expect original data preserved under "data" key
	}{
		{
			name:       "empty txHash returns original data",
			resultData: map[string]interface{}{"foo": "bar"},
			txHash:     "",
		},
		{
			name:       "nil resultData with empty txHash",
			resultData: nil,
			txHash:     "",
			wantNil:    true,
		},
		{
			name:       "map resultData with txHash merges fields",
			resultData: map[string]interface{}{"amount": "100"},
			txHash:     "0xabc",
			wantTxHash: "0xabc",
		},
		{
			name:       "non-map resultData with txHash wraps under data key",
			resultData: "some string",
			txHash:     "0xdef",
			wantTxHash: "0xdef",
			wantData:   true,
		},
		{
			name:       "nil resultData with txHash",
			resultData: nil,
			txHash:     "0x123",
			wantTxHash: "0x123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapResultWithMetadata(tt.resultData, tt.txHash, "")
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if tt.txHash == "" {
				// Should return original data unchanged
				if got == nil && tt.resultData != nil {
					t.Error("expected non-nil result")
				}
				return
			}
			m, ok := got.(map[string]interface{})
			if !ok {
				t.Fatalf("expected map result, got %T", got)
			}
			meta, ok := m["metadata"].(map[string]interface{})
			if !ok {
				t.Fatal("expected metadata map")
			}
			if meta["transactionHash"] != tt.wantTxHash {
				t.Errorf("transactionHash = %v, want %v", meta["transactionHash"], tt.wantTxHash)
			}
			if tt.wantData {
				if m["data"] != tt.resultData {
					t.Errorf("data = %v, want %v", m["data"], tt.resultData)
				}
			}
		})
	}
}

func mustNewValue(t *testing.T, v any) *structpb.Value {
	t.Helper()
	val, err := structpb.NewValue(v)
	if err != nil {
		t.Fatalf("failed to create structpb.Value: %v", err)
	}
	return val
}

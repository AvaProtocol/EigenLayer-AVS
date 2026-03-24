package taskengine

import (
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

func TestExtractTxHashFromFlatMetadata(t *testing.T) {
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
			name: "valid transactionHash",
			metadata: mustNewValue(t, map[string]any{
				"transactionHash": "0xabc123def4560000000000000000000000000000000000000000000000000001",
			}),
			want: "0xabc123def4560000000000000000000000000000000000000000000000000001",
		},
		{
			name: "with extra fields like gasUsed",
			metadata: mustNewValue(t, map[string]any{
				"transactionHash": "0xdef456abc1230000000000000000000000000000000000000000000000000002",
				"gasUsed":         "0x5208",
				"gasPrice":        "0x3b9aca00",
			}),
			want: "0xdef456abc1230000000000000000000000000000000000000000000000000002",
		},
		{
			name:     "empty object",
			metadata: mustNewValue(t, map[string]any{}),
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
			got := extractTxHashFromFlatMetadata(tt.metadata)
			if got != tt.want {
				t.Errorf("extractTxHashFromFlatMetadata() = %q, want %q", got, tt.want)
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

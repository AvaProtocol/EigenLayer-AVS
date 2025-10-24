package taskengine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Test-only helper functions for validating event trigger configurations

// extractAddressFromTopic extracts an Ethereum address from a padded topic (32-byte hash)
// Returns empty string if the topic is not an address or is invalid
func extractAddressFromTopic(topic string) string {
	if topic == "" || len(topic) < 66 {
		return ""
	}

	// Remove 0x prefix if present
	topic = strings.TrimPrefix(strings.ToLower(topic), "0x")

	// Topics are 32 bytes (64 hex chars). Addresses are last 20 bytes (40 hex chars)
	if len(topic) == 64 {
		// Check if first 24 chars are zeros (padded address format)
		paddingPart := topic[:24]
		addressPart := topic[24:]

		allZeros := true
		for _, c := range paddingPart {
			if c != '0' {
				allZeros = false
				break
			}
		}

		if allZeros {
			return "0x" + addressPart
		}
	}

	return ""
}

// validateTopicAddressesMatchQuery validates that any addresses in topics match the addresses in the query
// This is a test-only validation helper to ensure configuration correctness during development
func validateTopicAddressesMatchQuery(topics []interface{}, queryAddresses []string, logger Logger) error {
	if len(topics) == 0 || len(queryAddresses) == 0 {
		return nil // Nothing to validate
	}

	// Normalize query addresses for comparison
	normalizedQueryAddrs := make(map[string]bool)
	for _, addr := range queryAddresses {
		if addr != "" && common.IsHexAddress(addr) {
			normalizedQueryAddrs[strings.ToLower(common.HexToAddress(addr).Hex())] = true
		}
	}

	if len(normalizedQueryAddrs) == 0 {
		return nil // No valid addresses in query
	}

	// Check topics (skip topic[0] as it's typically the event signature)
	for topicIdx := 1; topicIdx < len(topics); topicIdx++ {
		if topicGroupMap, ok := topics[topicIdx].(map[string]interface{}); ok {
			if valuesInterface, exists := topicGroupMap["values"]; exists {
				if valuesArray, ok := valuesInterface.([]interface{}); ok {
					for _, valueInterface := range valuesArray {
						if valueStr, ok := valueInterface.(string); ok && valueStr != "" {
							// Try to extract an address from this topic
							extractedAddr := extractAddressFromTopic(valueStr)
							if extractedAddr != "" {
								normalizedExtracted := strings.ToLower(extractedAddr)
								if !normalizedQueryAddrs[normalizedExtracted] {
									// Found an address in topic that doesn't match query addresses
									if logger != nil {
										logger.Warn("⚠️ Topic contains address not present in query addresses",
											"topicIndex", topicIdx,
											"topicAddress", extractedAddr,
											"queryAddresses", queryAddresses)
									}
									return fmt.Errorf("topic[%d] contains address %s which is not present in the query addresses %v",
										topicIdx, extractedAddr, queryAddresses)
								}
							}
						}
					}
				}
			}
		}
	}

	return nil
}

func TestValidateTopicHexFormat(t *testing.T) {
	tests := []struct {
		name      string
		topic     string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "valid topic",
			topic:     "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
			wantError: false,
		},
		{
			name:      "empty topic (wildcard)",
			topic:     "",
			wantError: false,
		},
		{
			name:      "malformed double 0x",
			topic:     "0x0000000000000000000000000000000000000000000000x5a8a8a79ddf433756d4d97dcce33334d9e218856",
			wantError: true,
			errorMsg:  "contains multiple '0x' prefixes",
		},
		{
			name:      "missing 0x prefix",
			topic:     "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
			wantError: true,
			errorMsg:  "must start with '0x' prefix",
		},
		{
			name:      "invalid hex character",
			topic:     "0xzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			wantError: true,
			errorMsg:  "invalid hex character",
		},
		{
			name:      "padded address in topic",
			topic:     "0x0000000000000000000000005a8a8a79ddf433756d4d97dcce33334d9e218856",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTopicHexFormat(tt.topic)
			if tt.wantError {
				if err == nil {
					t.Errorf("validateTopicHexFormat() expected error but got nil")
					return
				}
				if tt.errorMsg != "" && !stringContains(err.Error(), tt.errorMsg) {
					t.Errorf("validateTopicHexFormat() error = %v, want error containing %v", err, tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateTopicHexFormat() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestExtractAddressFromTopic(t *testing.T) {
	tests := []struct {
		name        string
		topic       string
		wantAddress string
	}{
		{
			name:        "padded address",
			topic:       "0x0000000000000000000000005a8a8a79ddf433756d4d97dcce33334d9e218856",
			wantAddress: "0x5a8a8a79ddf433756d4d97dcce33334d9e218856",
		},
		{
			name:        "event signature (not an address)",
			topic:       "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
			wantAddress: "",
		},
		{
			name:        "empty topic",
			topic:       "",
			wantAddress: "",
		},
		{
			name:        "short topic",
			topic:       "0x1234",
			wantAddress: "",
		},
		{
			name:        "non-zero padded topic",
			topic:       "0x1234567890123456789012345a8a8a79ddf433756d4d97dcce33334d9e218856",
			wantAddress: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAddressFromTopic(tt.topic)
			if got != tt.wantAddress {
				t.Errorf("extractAddressFromTopic() = %v, want %v", got, tt.wantAddress)
			}
		})
	}
}

func TestValidateTopicAddressesMatchQuery(t *testing.T) {
	tests := []struct {
		name           string
		topics         []interface{}
		queryAddresses []string
		wantError      bool
		errorMsg       string
	}{
		{
			name: "matching address in topic",
			topics: []interface{}{
				map[string]interface{}{
					"values": []interface{}{
						"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					},
				},
				map[string]interface{}{
					"values": []interface{}{
						"0x0000000000000000000000005a8a8a79ddf433756d4d97dcce33334d9e218856",
					},
				},
			},
			queryAddresses: []string{"0x5a8A8a79DdF433756D4D97DCCE33334D9E218856"},
			wantError:      false,
		},
		{
			name: "non-matching address in topic",
			topics: []interface{}{
				map[string]interface{}{
					"values": []interface{}{
						"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					},
				},
				map[string]interface{}{
					"values": []interface{}{
						"0x000000000000000000000000742d35cc6634c0532925a3b8d965337c7ff18723",
					},
				},
			},
			queryAddresses: []string{"0x5a8A8a79DdF433756D4D97DCCE33334D9E218856"},
			wantError:      true,
			errorMsg:       "not present in the query addresses",
		},
		{
			name:           "empty topics",
			topics:         []interface{}{},
			queryAddresses: []string{"0x5a8A8a79DdF433756D4D97DCCE33334D9E218856"},
			wantError:      false,
		},
		{
			name: "empty query addresses",
			topics: []interface{}{
				map[string]interface{}{
					"values": []interface{}{
						"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					},
				},
			},
			queryAddresses: []string{},
			wantError:      false,
		},
		{
			name: "event signature only (no addresses)",
			topics: []interface{}{
				map[string]interface{}{
					"values": []interface{}{
						"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					},
				},
			},
			queryAddresses: []string{"0x5a8A8a79DdF433756D4D97DCCE33334D9E218856"},
			wantError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTopicAddressesMatchQuery(tt.topics, tt.queryAddresses, nil)
			if tt.wantError {
				if err == nil {
					t.Errorf("validateTopicAddressesMatchQuery() expected error but got nil")
					return
				}
				if tt.errorMsg != "" && !stringContains(err.Error(), tt.errorMsg) {
					t.Errorf("validateTopicAddressesMatchQuery() error = %v, want error containing %v", err, tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateTopicAddressesMatchQuery() unexpected error = %v", err)
				}
			}
		})
	}
}

// Helper function to check if a string contains a substring
func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && stringContainsHelper(s, substr)
}

func stringContainsHelper(s, substr string) bool {
	if s == substr {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

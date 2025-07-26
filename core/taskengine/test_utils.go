package taskengine

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"
)

// TestUtils provides shared utility functions for test files
type TestUtils struct{}

// MustConvertJSONABIToProtobufValues converts a JSON ABI string to protobuf values
// This is a shared helper function used across multiple test files
func (tu *TestUtils) MustConvertJSONABIToProtobufValues(jsonABI string) []*structpb.Value {
	var abi []interface{}
	if err := json.Unmarshal([]byte(jsonABI), &abi); err != nil {
		panic(fmt.Sprintf("Failed to unmarshal ABI: %v", err))
	}

	var result []*structpb.Value
	for _, item := range abi {
		value, err := structpb.NewValue(item)
		if err != nil {
			panic(fmt.Sprintf("Failed to convert ABI item to protobuf: %v", err))
		}
		result = append(result, value)
	}
	return result
}

// Global instance for easy access in tests
var TestUtilsInstance = &TestUtils{}

// Convenience function for direct access
func MustConvertJSONABIToProtobufValues(jsonABI string) []*structpb.Value {
	return TestUtilsInstance.MustConvertJSONABIToProtobufValues(jsonABI)
}

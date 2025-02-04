package byte4

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/crypto"
)

// GetMethodFromCalldata returns the method name and ABI method for a given 4-byte selector or full calldata
func GetMethodFromCalldata(parsedABI abi.ABI, selector []byte) (*abi.Method, error) {
	if len(selector) < 4 {
		return nil, fmt.Errorf("invalid selector length: %d", len(selector))
	}

	// Get first 4 bytes of the calldata. This is the first 8 characters of the calldata
	// Function calls in the Ethereum Virtual Machine(EVM) are specified by the first four bytes of data sent with a transaction. These 4-byte signatures are defined as the first four bytes of the Keccak hash (SHA3) of the canonical representation of the function signature.

	methodID := selector[:4]

	// Find matching method in ABI
	for name, method := range parsedABI.Methods {
		// Build the signature string from inputs
		var types []string
		for _, input := range method.Inputs {
			types = append(types, input.Type.String())
		}

		// Create method signature: name(type1,type2,...)
		sig := fmt.Sprintf("%v(%v)", name, strings.Join(types, ","))
		hash := crypto.Keccak256([]byte(sig))[:4]

		if bytes.Equal(hash, methodID) {
			return &method, nil
		}
	}

	return nil, fmt.Errorf("no matching method found for selector: 0x%x", methodID)
}
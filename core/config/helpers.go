package config

import (
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func convertToAddressSlice(addresses []string) []common.Address {
	result := make([]common.Address, len(addresses))
	for i, addr := range addresses {
		result[i] = common.HexToAddress(addr)
	}
	return result
}

// defaultApprovedOperators is the allowlist used when yaml omits
// approved_operators. Same three addresses CanStreamCheck has always
// fallen back to — production configs set the list explicitly.
var defaultApprovedOperators = []string{
	"0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d",
	"0xc6b87cc9e85b07365b6abefff061f237f7cf7dc3",
	"0xa026265a0f01a6e1a19b04655519429df0a57c4e",
}

// IsApprovedOperator reports whether address is allowed to run Node RPCs
// and receive task streams. Empty ApprovedOperators falls back to the
// hardcoded defaults; a configured list is exclusive of those defaults.
func (c *Config) IsApprovedOperator(address string) bool {
	if c == nil {
		return false
	}
	if len(c.ApprovedOperators) == 0 {
		for _, approved := range defaultApprovedOperators {
			if strings.EqualFold(address, approved) {
				return true
			}
		}
		return false
	}
	for _, approvedAddr := range c.ApprovedOperators {
		if strings.EqualFold(address, approvedAddr.Hex()) {
			return true
		}
	}
	return false
}

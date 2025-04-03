package taskengine

import (
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func isWhitelistedAddress(address common.Address, whitelist []common.Address) bool {
	if whitelist == nil {
		return false
	}
	
	addressStr := strings.ToLower(address.Hex())
	for _, whitelistAddr := range whitelist {
		if strings.ToLower(whitelistAddr.Hex()) == addressStr {
			return true
		}
	}
	return false
}

package config

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

func parseBudgetRetiredSessionSigners(raw map[int64][]string) (map[int64][]common.Address, error) {
	out := make(map[int64][]common.Address, len(raw))
	for chain, addresses := range raw {
		if chain <= 0 {
			return nil, fmt.Errorf("wallet_budget_retired_session_signers: chain must be positive")
		}
		seen := map[common.Address]bool{}
		for _, value := range addresses {
			if !common.IsHexAddress(value) || common.HexToAddress(value) == (common.Address{}) {
				return nil, fmt.Errorf("wallet_budget_retired_session_signers: invalid signer for chain %d", chain)
			}
			address := common.HexToAddress(value)
			if !seen[address] {
				out[chain] = append(out[chain], address)
				seen[address] = true
			}
		}
	}
	return out, nil
}

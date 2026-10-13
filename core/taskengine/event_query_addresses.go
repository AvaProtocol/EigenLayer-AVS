package taskengine

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// IsEthTransferLogAddress reports whether addr is the EIP-7708 system address.
// That address emits an ERC-20-shaped Transfer for a plain ETH move.
func IsEthTransferLogAddress(addr common.Address) bool {
	return addr == ethTransferLogAddress
}

// UsableEventQueryAddresses returns the contract addresses a watch can subscribe
// to. Zero, non-hex, and the ETH-transfer system address are dropped. The
// result is checksummed and de-duplicated, in input order.
func UsableEventQueryAddresses(addrs []string) []string {
	seen := make(map[common.Address]struct{}, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, raw := range addrs {
		if !common.IsHexAddress(raw) {
			continue
		}
		addr := common.HexToAddress(raw)
		if addr == (common.Address{}) || addr == ethTransferLogAddress {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr.Hex())
	}
	return out
}

// ValidateEventTriggerAddresses rejects a new event task whose query has no
// contract to watch. It is not part of Workflow.ValidateWithError: that sweep
// marks already-stored tasks Failed on boot.
func ValidateEventTriggerAddresses(trigger *avsproto.TaskTrigger) error {
	if trigger == nil || trigger.GetEvent() == nil || trigger.GetEvent().GetConfig() == nil {
		return nil
	}
	for i, query := range trigger.GetEvent().GetConfig().GetQueries() {
		var raw []string
		if query != nil {
			raw = query.GetAddresses()
		}
		if len(raw) == 0 {
			return fmt.Errorf("queries[].addresses must be a non-empty array")
		}
		if len(UsableEventQueryAddresses(raw)) > 0 {
			continue
		}
		for _, addr := range raw {
			if common.IsHexAddress(addr) && common.HexToAddress(addr) == ethTransferLogAddress {
				return fmt.Errorf("query[%d].addresses: the ETH transfer system address cannot be watched", i)
			}
		}
		return fmt.Errorf("queries[].addresses must be a non-empty array")
	}
	return nil
}

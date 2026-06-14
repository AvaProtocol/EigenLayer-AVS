package taskengine

import (
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// getEntryPointNonceDirect bridges directChainStateReader.GetEntryPointNonce
// to aa.GetNonce. Lives in its own file to keep chain_state_reader.go
// free of the aa package dependency — that lets us mock-out the aa
// path more cleanly in tests (link a shim in a test-only file when
// needed) and keeps the interface-level reader logic readable.
func getEntryPointNonceDirect(client *ethclient.Client, walletAddr common.Address, key *big.Int) (*big.Int, error) {
	return aa.GetNonce(client, walletAddr, key)
}

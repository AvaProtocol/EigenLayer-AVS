package taskengine

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
)

// resolveFactoryOverride is the one place the "explicit factory wins" rule
// lives for GetWallet/SetWallet.

func TestResolveFactoryOverride_OverrideWins(t *testing.T) {
	override := "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"
	require.Equal(t, common.HexToAddress(override), resolveFactoryOverride(override),
		"a supplied override reaches a stored wallet under its own factory")
}

func TestResolveFactoryOverride_DefaultsToMAv2(t *testing.T) {
	require.Equal(t, aa.MAv2FactoryAddress(), resolveFactoryOverride(""))
}

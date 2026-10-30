package aa

import (
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestOwnerModuleCleanupUsesStoredInstallPayloads(t *testing.T) {
	install, _, allowlistData, timeData := testGrantInstall(t, testEntity)
	calls, err := ModuleCleanupFromInstall(testEntity, install)
	require.NoError(t, err)
	require.Len(t, calls, 3, "stateless allowlist execution hook adds no call")
	require.Equal(t, []common.Address{AllowlistModuleAddress(), TimeRangeModuleAddress(), SingleSignerValidationModuleAddress()}, []common.Address{calls[0].Target, calls[1].Target, calls[2].Target})
	bytesType, err := abi.NewType("bytes", "", nil)
	require.NoError(t, err)
	wantSigner, err := PackSingleSignerUninstallData(testEntity)
	require.NoError(t, err)
	for i, want := range [][]byte{allowlistData, timeData, wantSigner} {
		decoded, err := (abi.Arguments{{Type: bytesType}}).Unpack(calls[i].Data[4:])
		require.NoError(t, err)
		require.Equal(t, want, decoded[0])
		require.Zero(t, calls[i].Value.Sign())
	}
}

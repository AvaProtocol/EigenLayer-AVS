package aa

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

type residueCaller func(ethereum.CallMsg) ([]byte, error)

func (f residueCaller) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return f(call)
}

func residueInstall(t *testing.T) []byte {
	t.Helper()
	allowlist, err := AllowlistValidationHook(testEntity, []AllowlistInput{{Target: common.HexToAddress("0x1234"), HasSelectorAllowlist: true, Selectors: [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}}, HasERC20SpendLimit: true, ERC20SpendLimit: big.NewInt(500)}})
	require.NoError(t, err)
	native, err := NativeTokenLimitValidationHook(testEntity, big.NewInt(1000))
	require.NoError(t, err)
	timeRange, err := TimeRangeValidationHook(testEntity, 2000000000, 0)
	require.NoError(t, err)
	install, err := PackSessionSignerInstall(SessionGrant{EntityID: testEntity, Signer: common.HexToAddress("0x5678"), Global: true, Hooks: [][]byte{allowlist, AllowlistExecHook(testEntity), native, NativeTokenLimitExecHook(testEntity), timeRange}})
	require.NoError(t, err)
	return install
}

func TestEntityModuleStorageChecksEveryResidueBranch(t *testing.T) {
	install := residueInstall(t)
	getters := []struct {
		signature string
		words     int
	}{
		{"signers(uint32,address)", 1},
		{"addressAllowlist(uint32,address,address)", 3},
		{"erc20SpendLimits(uint32,address,address)", 1},
		{"selectorAllowlist(uint32,bytes4,address,address)", 1},
		{"timeRanges(uint32,address)", 2},
		{"limits(uint256,address)", 1},
	}
	for _, getter := range getters {
		for _, failure := range []bool{false, true} {
			t.Run(getter.signature+"/"+map[bool]string{false: "residual", true: "read failed"}[failure], func(t *testing.T) {
				visited := false
				caller := residueCaller(func(call ethereum.CallMsg) ([]byte, error) {
					for _, candidate := range getters {
						if string(call.Data[:4]) != string(crypto.Keccak256([]byte(candidate.signature))[:4]) {
							continue
						}
						out := make([]byte, candidate.words*32)
						if candidate.signature == getter.signature {
							visited = true
							if failure {
								return nil, errors.New("RPC unavailable")
							}
							out[len(out)-1] = 1
						}
						return out, nil
					}
					t.Fatalf("unexpected getter %x", call.Data[:4])
					return nil, nil
				})
				clear, err := EntityModuleStorageCleared(context.Background(), caller, common.HexToAddress("0x9876"), testEntity, install)
				require.True(t, visited)
				require.False(t, clear)
				if failure {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

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

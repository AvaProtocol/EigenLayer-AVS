package aa

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestHistoricalSessionSignerInstallUsesExactIdentityAndSelfCalls(t *testing.T) {
	wallet := common.HexToAddress("0x1111")
	signer := common.HexToAddress("0xbaba")
	install, err := PackSessionSignerInstall(SessionGrant{EntityID: 99, Signer: signer, Selectors: [][4]byte{{1, 2, 3, 4}}})
	require.NoError(t, err)
	for _, mode := range []string{"direct", "self batch", "foreign wallet", "wrong signer", "wrong entity", "owner entity", "duplicate", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			call, want, entity, signingKey := install, install, uint32(99), signer
			switch mode {
			case "self batch", "foreign wallet", "duplicate":
				target := wallet
				if mode == "foreign wallet" {
					target, want = common.HexToAddress("0x2222"), nil
				}
				calls := []Call{{Target: target, Data: install}}
				if mode == "duplicate" {
					calls = append(calls, calls[0])
				}
				call, err = PackExecuteBatchMAv2(calls)
				require.NoError(t, err)
			case "wrong signer":
				signingKey, want = common.HexToAddress("0xcafe"), nil
			case "wrong entity":
				entity, want = 100, nil
			case "owner entity":
				entity = 0
			case "malformed":
				call = install[:10]
			}
			got, err := HistoricalSessionSignerInstall(wallet, entity, signingKey, call)
			if mode == "duplicate" || mode == "owner entity" || mode == "malformed" {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, want, got)
			}
		})
	}
}

func TestHistoricalSessionSignerInstallRefusesForeignHookCleanup(t *testing.T) {
	wallet, signer := common.HexToAddress("0x1111"), common.HexToAddress("0xbaba")
	for _, mode := range []string{"unknown module", "foreign config entity", "foreign payload entity"} {
		t.Run(mode, func(t *testing.T) {
			hook, err := TimeRangeValidationHook(99, 1800000000, 0)
			require.NoError(t, err)
			switch mode {
			case "unknown module":
				module := common.HexToAddress("0xdead")
				copy(hook[:20], module[:])
			case "foreign config entity":
				hook[23] = 100
			case "foreign payload entity":
				hook[25+31] = 100
			}
			install, err := PackSessionSignerInstall(SessionGrant{EntityID: 99, Signer: signer, Selectors: [][4]byte{{1, 2, 3, 4}}, Hooks: [][]byte{hook}})
			require.NoError(t, err)
			got, err := HistoricalSessionSignerInstall(wallet, 99, signer, install)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

package aa

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestEntityRegisteredReadsAccountNotSignerModule(t *testing.T) {
	account := common.HexToAddress("0x1234")
	for _, tc := range []struct {
		name       string
		view       validationView
		registered bool
	}{
		{"cleared", validationView{}, false},
		{"flags", validationView{ValidationFlags: 5}, true},
		{"validation hooks", validationView{ValidationHooks: [][25]byte{{1}}}, true},
		{"execution hooks", validationView{ExecutionHooks: [][25]byte{{1}}}, true},
		{"selectors", validationView{Selectors: [][4]byte{{1}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := validationViewABI.Methods["getValidationData"].Outputs.Pack(tc.view)
			require.NoError(t, err)
			caller := &recordingCaller{result: encoded}
			registered, err := EntityRegisteredOnChain(context.Background(), caller, account, 7)
			require.NoError(t, err)
			require.Equal(t, tc.registered, registered)
			require.Equal(t, account, caller.to)
			moduleEntity := PackModuleEntity(SingleSignerValidationModuleAddress(), 7)
			require.Equal(t, moduleEntity[:], caller.data[4:28])
		})
	}
	for _, caller := range []*recordingCaller{{}, {result: make([]byte, 32)}, {err: errors.New("RPC unavailable")}} {
		registered, err := EntityRegisteredOnChain(context.Background(), caller, account, 7)
		require.Error(t, err)
		require.False(t, registered)
	}
}

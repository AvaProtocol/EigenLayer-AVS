package preset

import (
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil/fixtures"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// Execute the exact pinned runtime, including EIP-7702 code resolution, rather
// than relying on an RPC fixture to assert which signature the account accepts.
// No network, real EOA delegation, controller or submitted transaction is used.
func TestOwnerEOAPinnedRuntimeValidatesPersonalSign(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(key.PublicKey)
	delegate := config.SMA7702Delegate()
	code := fixtures.SMA7702Runtime()
	require.Equal(t, config.SMA7702ImplHash(), crypto.Keccak256Hash(code))
	contractABI, err := abi.JSON(strings.NewReader(`[
 {"type":"function","name":"validateUserOp","inputs":[{"name":"userOp","type":"tuple","components":[
  {"name":"sender","type":"address"},{"name":"nonce","type":"uint256"},{"name":"initCode","type":"bytes"},
  {"name":"callData","type":"bytes"},{"name":"accountGasLimits","type":"bytes32"},{"name":"preVerificationGas","type":"uint256"},
  {"name":"gasFees","type":"bytes32"},{"name":"paymasterAndData","type":"bytes"},{"name":"signature","type":"bytes"}]},
  {"name":"userOpHash","type":"bytes32"},{"name":"missingAccountFunds","type":"uint256"}],"outputs":[{"type":"uint256"}]},
 {"type":"function","name":"getFallbackSignerData","inputs":[],"outputs":[{"type":"address"},{"type":"bool"}]},
 {"type":"function","name":"entryPoint","inputs":[],"outputs":[{"type":"address"}]}
 ]`))
	require.NoError(t, err)
	for _, scenario := range []string{"personal_sign", "bare hash", "hex text", "double prefix", "wrong nonce entity", "disabled fallback", "changed fallback"} {
		t.Run(scenario, func(t *testing.T) {
			st, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			st.SetCode(delegate, code, tracing.CodeChangeUnspecified)
			st.SetCode(owner, types.AddressToDelegation(delegate), tracing.CodeChangeUnspecified)
			chain := *params.AllDevChainProtocolChanges
			chain.ChainID = big.NewInt(config.SMA7702ChainSepolia)
			cfg := &runtime.Config{ChainConfig: &chain, Origin: EntryPointV07(), GasLimit: 2000000, State: st}
			fallbackData, err := contractABI.Pack("getFallbackSignerData")
			require.NoError(t, err)
			out, _, err := runtime.Call(owner, fallbackData, cfg)
			require.NoError(t, err)
			fallback, err := contractABI.Unpack("getFallbackSignerData", out)
			require.NoError(t, err)
			require.Equal(t, []interface{}{owner, false}, fallback)
			entryData, err := contractABI.Pack("entryPoint")
			require.NoError(t, err)
			out, _, err = runtime.Call(owner, entryData, cfg)
			require.NoError(t, err)
			require.Equal(t, EntryPointV07(), common.BytesToAddress(out))
			op := testOp()
			op.Sender = owner
			op.Factory, op.FactoryData = nil, nil
			op.Nonce, err = userop.EncodeNonceMAv2(0, userop.ValidationOptionGlobal, 0)
			require.NoError(t, err)
			if scenario == "wrong nonce entity" {
				op.Nonce, err = userop.EncodeNonceMAv2(1, userop.ValidationOptionGlobal, 0)
				require.NoError(t, err)
			}
			hash, err := op.GetUserOpHash(EntryPointV07(), chain.ChainID)
			require.NoError(t, err)
			digest := accounts.TextHash(hash.Bytes())
			switch scenario {
			case "bare hash":
				digest = hash.Bytes()
			case "hex text":
				digest = accounts.TextHash([]byte(hash.Hex()))
			case "double prefix":
				digest = accounts.TextHash(digest)
			}
			sig, err := crypto.Sign(digest, key)
			require.NoError(t, err)
			sig[64] += 27
			op.Signature, err = userop.WrapSignatureMAv2(sig)
			require.NoError(t, err)
			limits, err := op.AccountGasLimits()
			require.NoError(t, err)
			fees, err := op.GasFees()
			require.NoError(t, err)
			packed := struct {
				Sender                      common.Address
				Nonce                       *big.Int
				InitCode, CallData          []byte
				AccountGasLimits            [32]byte
				PreVerificationGas          *big.Int
				GasFees                     [32]byte
				PaymasterAndData, Signature []byte
			}{owner, op.Nonce, []byte{}, op.CallData, limits, op.PreVerificationGas, fees, []byte{}, op.Signature}
			data, err := contractABI.Pack("validateUserOp", packed, hash, big.NewInt(0))
			require.NoError(t, err)
			storageSlot := common.HexToHash("0x5b9dc9aa943f8fa2653ceceda5e3798f0686455280432166ba472eca0bc17a32")
			if scenario == "disabled fallback" {
				st.SetState(owner, storageSlot, common.BigToHash(new(big.Int).Lsh(big.NewInt(1), 160)))
			} else if scenario == "changed fallback" {
				st.SetState(owner, storageSlot, common.HexToHash("0x1234"))
			}
			out, _, err = runtime.Call(owner, data, cfg)
			if scenario == "wrong nonce entity" || scenario == "disabled fallback" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, out, 32)
			if scenario == "personal_sign" {
				require.Zero(t, new(big.Int).SetBytes(out).Sign(), "pinned SMA-7702 must accept the advertised owner signature")
			} else {
				require.Equal(t, big.NewInt(1), new(big.Int).SetBytes(out), "wrong signing recipe or fallback owner must fail validation")
			}
		})
	}
}

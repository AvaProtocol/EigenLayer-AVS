//go:build integration
// +build integration

package taskengine

// Live Sepolia proof that Alchemy Gas Manager sponsors a 7702 EOA through
// production SendUserOpMAv2, and that K7 charges value-only under
// sponsorship vs value+gas when self-funded.
//
// Does NOT flip gateway eoa_7702_execute. The flag is set on an in-process
// SmartWalletConfig only — the same pattern as the 7702 send-guard tests.
// Does NOT load config/test.yaml (that file sets disable_gas_sponsorship,
// which is why no other suite path touches Gas Manager).
//
//	ALCHEMY_PAYMASTER_POLICY_ID  required; must be a Sepolia policy WITHOUT
//	                             a production custom-rules webhook
//	ALCHEMY_API_KEY              Alchemy Sepolia RPC + bundler
//	SPIKE_RPC_URL                optional paid RPC; else Alchemy
//	SPIKE_7702_KEY or scripts/spike/mav2_7702_eoa/.eoa.key
//	SPIKE_CONTROLLER_KEY or CONTROLLER_PRIVATE_KEY (must differ from EOA)
//	GAS_MANAGER_WEBHOOK_SECRET   optional; set only if the policy has custom rules
//
//	CGO_ENABLED=0 go test -tags=integration ./core/taskengine \
//	  -run TestEOA7702SponsorshipK7_Sepolia -v -count=1
//
// Hard-fail on missing env. Not per-PR CI.
//
// A dummy-sig simulation of this path is AA23 (missing grant →
// ValidationFunctionMissing) for both a 7702 EOA and a deployed MA v2
// runner. That is not this test. Do not cite an AA23 simulation as
// "sponsorship was tested."

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

const (
	eoa7702SponsorCap  = "50000000000000000" // 0.05 ETH — covers self-funded gas prefund
	eoa7702SponsorWei  = int64(1)
	eoa7702ControlSalt = int64(900001)
	eoa7702AliceHex    = "0x000000000000000000000000000000000000a11c"
)

func TestEOA7702SponsorshipK7_Sepolia(t *testing.T) {
	require.NoError(t, testutil.LoadDotEnv())

	policy := strings.TrimSpace(os.Getenv("ALCHEMY_PAYMASTER_POLICY_ID"))
	require.NotEmpty(t, policy, "ALCHEMY_PAYMASTER_POLICY_ID must be set; config/test.yaml disables sponsorship and cannot prove Gas Manager")

	rpcURL := paidSepoliaRPC(t)
	bundlerURL := alchemySepoliaBundler(t)
	ownerKey, eoa := throwaway7702Key(t)
	ctrlKey, ctrl := sponsorshipController(t, eoa)
	require.NotEqual(t, eoa, ctrl, "session signer must differ from the EOA")

	ctx := context.Background()
	chain, err := ethclient.Dial(rpcURL)
	require.NoError(t, err, "cannot reach Sepolia RPC")
	t.Cleanup(func() { chain.Close() })
	chainID, err := chain.ChainID(ctx)
	require.NoError(t, err)
	require.Equal(t, config.SMA7702ChainSepolia, chainID.Int64())

	impl, err := chain.CodeAt(ctx, config.SMA7702Delegate(), nil)
	require.NoError(t, err)
	eoaCode, err := chain.CodeAt(ctx, eoa, nil)
	require.NoError(t, err)
	pin := &config.SmartWalletConfig{
		ChainID:         config.SMA7702ChainSepolia,
		SMA7702Delegate: config.SMA7702Delegate(),
		SMA7702ImplHash: config.SMA7702ImplHash(),
	}
	require.NoError(t, pin.AssertSMA7702Designation(eoaCode, impl), "throwaway EOA must already be K13-delegated (run the B0 spike first)")

	bal, err := chain.BalanceAt(ctx, eoa, nil)
	require.NoError(t, err)
	require.Greater(t, bal.Cmp(big.NewInt(20_000_000_000_000_000)), 0,
		"throwaway EOA %s needs ~0.02 ETH for the self-funded K7 branch", eoa.Hex())

	alchemyKey := strings.TrimSpace(os.Getenv("ALCHEMY_API_KEY"))
	sponsoredCfg := &config.SmartWalletConfig{
		EthRpcUrl:                rpcURL,
		BundlerURL:               bundlerURL,
		BundlerProvider:          config.BundlerProviderAlchemy,
		AlchemyAPIKey:            alchemyKey,
		AccountProvider:          config.AccountProviderModularAccountV2,
		ChainID:                  config.SMA7702ChainSepolia,
		ControllerPrivateKey:     ctrlKey,
		FactoryAddress:           common.HexToAddress(config.DefaultFactoryProxyAddressHex),
		EOA7702Execute:           true,
		SMA7702Delegate:          config.SMA7702Delegate(),
		SMA7702ImplHash:          config.SMA7702ImplHash(),
		AlchemyPaymasterPolicyID: policy,
		DisableGasSponsorship:    false,
		GasManagerWebhookSecret:  strings.TrimSpace(os.Getenv("GAS_MANAGER_WEBHOOK_SECRET")),
	}
	require.NotEmpty(t, sponsoredCfg.SponsorshipPolicyID(), "in-process config must actually request Gas Manager (Alchemy bundler, policy set, disable_gas_sponsorship unset)")

	preset.SetEOA7702AccountLookup(func(id int64, owner, sender common.Address) (bool, error) {
		return id == config.SMA7702ChainSepolia && owner == eoa && sender == eoa, nil
	})
	t.Cleanup(func() { preset.SetEOA7702AccountLookup(nil) })

	bundler, err := rpc.DialContext(ctx, bundlerURL)
	require.NoError(t, err)
	t.Cleanup(bundler.Close)
	entry := preset.EntryPointV07()
	alice := common.HexToAddress(eoa7702AliceHex)
	one := big.NewInt(eoa7702SponsorWei)

	undeployed, undeployedSalt := unusedMAv2Salt(t, ctx, chain, eoa)
	uop := dummyNativeOp(*undeployed, alice, one)
	uerr := preset.RequestSponsorshipV07(ctx, bundler, uop, entry, preset.SponsorshipRequestV07{
		PolicyID:    policy,
		WebhookData: sponsoredCfg.GasManagerWebhookSecret,
	})
	require.Error(t, uerr)
	require.Contains(t, strings.ToLower(uerr.Error()), "aa20",
		"undeployed CREATE2 must still be AA20, not a sender-type denial: %v", uerr)
	t.Logf("UNDEPLOYED_CREATE2 AA20 as expected sender=%s salt=%s", undeployed.Hex(), undeployedSalt.String())

	// Deployed MA v2 control: unused salt, first op with a native grant, sponsored.
	derived, controlSalt := unusedMAv2Salt(t, ctx, chain, eoa)
	require.NoError(t, sendETH(ctx, chain, chainID, ownerKey, eoa, *derived, one))
	dEntity := randomSessionEntity()
	dAuth := deferredNativeGrant(t, chainID, *derived, ownerKey, ctrlKey, ctrl, dEntity, alice, eoa7702SponsorCap)
	dCall, err := aa.PackExecute(alice, one, nil)
	require.NoError(t, err)
	dOp, _, err := preset.SendUserOpMAv2(sponsoredCfg, eoa, dCall, derived, controlSalt, dAuth, logger.NewNoOpLogger())
	requireNotWebhookDenied(t, err)
	require.NoError(t, err, "deployed MA v2 with a grant must be sponsored through SendUserOpMAv2")
	requirePaymaster(t, dOp, "deployed MA v2 control")
	t.Logf("DEPLOYED_MA_V2 SPONSORED paymaster=%s sender=%s salt=%s", dOp.Paymaster.Hex(), derived.Hex(), controlSalt.String())

	// EOA 7702: deferred native grant riding the first sponsored send.
	entity := randomSessionEntity()
	eAuth := deferredNativeGrant(t, chainID, eoa, ownerKey, ctrlKey, ctrl, entity, alice, eoa7702SponsorCap)
	eCall, err := aa.PackExecute(alice, one, nil)
	require.NoError(t, err)
	before, err := chain.BalanceAt(ctx, eoa, nil)
	require.NoError(t, err)
	limitBefore := readNTLimits(t, ctx, chain, entity, eoa)
	require.Equal(t, "0", limitBefore.String(), "NT limits must start at 0 (fresh entity)")

	eOp, _, err := preset.SendUserOpMAv2(sponsoredCfg, eoa, eCall, &eoa, nil, eAuth, logger.NewNoOpLogger())
	requireNotWebhookDenied(t, err)
	require.NoError(t, err, "7702 EOA sponsored send through SendUserOpMAv2")
	requirePaymaster(t, eOp, "7702 EOA")
	t.Logf("EOA_7702 SPONSORED paymaster=%s", eOp.Paymaster.Hex())

	uor := waitUserOpReceipt(t, ctx, bundler, eOp, entry, chainID)
	require.True(t, uor.Success, "UserOp success=true")
	require.NotNil(t, uor.ActualGasCost)
	require.Greater(t, uor.ActualGasCost.Sign(), 0, "actualGasCost must be > 0 (gas was spent by someone)")
	after, err := chain.BalanceAt(ctx, eoa, nil)
	require.NoError(t, err)
	delta := new(big.Int).Sub(before, after)
	require.Equal(t, one.String(), delta.String(),
		"sponsored EOA ETH delta must be exactly 1 wei, not value+gas (delta=%s actualGasCost=%s)", delta, uor.ActualGasCost)
	limitAfter := readNTLimits(t, ctx, chain, entity, eoa)
	capAmt, ok := new(big.Int).SetString(eoa7702SponsorCap, 10)
	require.True(t, ok)
	wantLimit := new(big.Int).Sub(capAmt, one)
	require.Equal(t, wantLimit.String(), limitAfter.String(),
		"sponsored NT limits must drop by value only (got %s want %s); if this is value+gas, A3's sponsored branch is wrong for Track B",
		limitAfter, wantLimit)
	t.Logf("K7_SPONSORED paymaster=%s eth_delta=%s nt_remaining=%s actualGasCost=%s",
		eOp.Paymaster.Hex(), delta, limitAfter, uor.ActualGasCost)

	// Self-funded follow-up: same entity, no deferred, sponsorship off.
	selfCfg := *sponsoredCfg
	selfCfg.DisableGasSponsorship = true
	selfCfg.AlchemyPaymasterPolicyID = ""
	require.Empty(t, selfCfg.SponsorshipPolicyID())
	follow := &preset.SessionAuthorization{EntityID: entity, SignerKey: ctrlKey, WrapExecuteUserOp: true}
	beforeSF, err := chain.BalanceAt(ctx, eoa, nil)
	require.NoError(t, err)
	limitSF0 := readNTLimits(t, ctx, chain, entity, eoa)
	sfOp, _, err := preset.SendUserOpMAv2(&selfCfg, eoa, eCall, &eoa, nil, follow, logger.NewNoOpLogger())
	require.NoError(t, err, "self-funded follow-up")
	require.True(t, sfOp.Paymaster == nil || *sfOp.Paymaster == (common.Address{}),
		"self-funded op must not carry a paymaster")
	sfRec := waitUserOpReceipt(t, ctx, bundler, sfOp, entry, chainID)
	require.True(t, sfRec.Success)
	require.NotNil(t, sfRec.ActualGasCost)
	require.Greater(t, sfRec.ActualGasCost.Sign(), 0)
	afterSF, err := chain.BalanceAt(ctx, eoa, nil)
	require.NoError(t, err)
	deltaSF := new(big.Int).Sub(beforeSF, afterSF)
	wantETH := new(big.Int).Add(one, sfRec.ActualGasCost)
	require.Equal(t, wantETH.String(), deltaSF.String(),
		"self-funded EOA ETH delta must be 1 wei + actualGasCost (delta=%s want=%s)", deltaSF, wantETH)
	limitSF1 := readNTLimits(t, ctx, chain, entity, eoa)
	ntDrop := new(big.Int).Sub(limitSF0, limitSF1)
	wantNT := new(big.Int).Add(one, signedOpGasPrefund(sfOp))
	require.Equal(t, wantNT.String(), ntDrop.String(),
		"self-funded NT limits must drop by value+gas prefund (got drop=%s want %s); K7 gas branch", ntDrop, wantNT)
	t.Logf("K7_SELF_FUNDED eth_delta=%s nt_drop=%s actualGasCost=%s gas_prefund=%s",
		deltaSF, ntDrop, sfRec.ActualGasCost, signedOpGasPrefund(sfOp))
}

func requireNotWebhookDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "denied by webhook") {
		t.Fatalf("Gas Manager custom-rules webhook denied this wallet (production FeeLedger does not know the throwaway). Need a Sepolia policy without a webhook — this is not a sender-type ban; the AA20 undeployed control already passed. err=%v", err)
	}
}

func requirePaymaster(t *testing.T, op *userop.UserOperationV07, label string) {
	t.Helper()
	require.NotNil(t, op, "%s: signed op is nil", label)
	require.NotNil(t, op.Paymaster, "%s: Paymaster must be non-zero (the thing we have never seen)", label)
	require.NotEqual(t, (common.Address{}), *op.Paymaster, "%s: Paymaster must be non-zero", label)
}

func paidSepoliaRPC(t *testing.T) string {
	t.Helper()
	if u := strings.TrimSpace(os.Getenv("SPIKE_RPC_URL")); u != "" {
		require.NotContains(t, strings.ToLower(u), "publicnode")
		require.NotContains(t, strings.ToLower(u), "public-rpc")
		return u
	}
	k := strings.TrimSpace(os.Getenv("ALCHEMY_API_KEY"))
	require.NotEmpty(t, k, "SPIKE_RPC_URL or ALCHEMY_API_KEY must be set (paid endpoint)")
	return "https://eth-sepolia.g.alchemy.com/v2/" + k
}

func alchemySepoliaBundler(t *testing.T) string {
	t.Helper()
	if u := strings.TrimSpace(os.Getenv("SPIKE_BUNDLER_URL")); u != "" {
		require.NotContains(t, strings.ToLower(u), "avaprotocol.org")
		require.NotContains(t, strings.ToLower(u), "publicnode")
		return u
	}
	k := strings.TrimSpace(os.Getenv("ALCHEMY_API_KEY"))
	require.NotEmpty(t, k, "SPIKE_BUNDLER_URL or ALCHEMY_API_KEY must be set")
	return "https://eth-sepolia.g.alchemy.com/v2/" + k
}

func throwaway7702Key(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	if os.Getenv("SPIKE_7702_KEY") != "" {
		return requireKey(t, "SPIKE_7702_KEY")
	}
	dir, err := os.Getwd()
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			p := filepath.Join(dir, "scripts/spike/mav2_7702_eoa/.eoa.key")
			raw, err := os.ReadFile(p)
			require.NoError(t, err, "SPIKE_7702_KEY or %s must be set", p)
			key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"))
			require.NoError(t, err)
			return key, crypto.PubkeyToAddress(key.PublicKey)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("SPIKE_7702_KEY or scripts/spike/mav2_7702_eoa/.eoa.key must be set")
	return nil, common.Address{}
}

func sponsorshipController(t *testing.T, eoa common.Address) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	for _, name := range []string{"SPIKE_CONTROLLER_KEY", "CONTROLLER_PRIVATE_KEY"} {
		if os.Getenv(name) == "" {
			continue
		}
		key, addr := requireKey(t, name)
		require.NotEqual(t, eoa, addr, "%s is the delegated EOA", name)
		return key, addr
	}
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	return key, crypto.PubkeyToAddress(key.PublicKey)
}

func unusedMAv2Salt(t *testing.T, ctx context.Context, chain *ethclient.Client, eoa common.Address) (*common.Address, *big.Int) {
	t.Helper()
	for i := int64(0); i < 32; i++ {
		salt := big.NewInt(eoa7702ControlSalt + i)
		addr, err := aa.GetSenderAddressMAv2(chain, eoa, salt)
		require.NoError(t, err)
		code, err := chain.CodeAt(ctx, *addr, nil)
		require.NoError(t, err)
		if len(code) == 0 {
			return addr, salt
		}
	}
	t.Fatal("could not find an unused MA v2 salt")
	return nil, nil
}

func deferredNativeGrant(t *testing.T, chainID *big.Int, account common.Address, ownerKey, ctrlKey *ecdsa.PrivateKey, ctrl common.Address, entity uint32, recipient common.Address, cap string) *preset.SessionAuthorization {
	t.Helper()
	r := recipient
	perms := SessionPermissions{
		NativeRecipients: []*common.Address{&r},
		NativeSpendCap:   &model.NativeSpendCap{Amount: cap},
		ValidUntilMs:     time.Now().Add(24 * time.Hour).UnixMilli(),
		CodeAt:           func(common.Address) ([]byte, error) { return nil, nil },
	}
	require.NoError(t, perms.Validate())
	hooks, err := perms.HooksFor(entity)
	require.NoError(t, err)
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity, Signer: ctrl, Global: true, Hooks: hooks,
	})
	require.NoError(t, err)
	return signDeferred(t, chainID, account, ownerKey, ctrlKey, entity, installCall, true, 1)
}

func signDeferred(t *testing.T, chainID *big.Int, account common.Address, ownerKey, ctrlKey *ecdsa.PrivateKey, entity uint32, installCall []byte, wrap bool, rows int) *preset.SessionAuthorization {
	t.Helper()
	deadline := uint64(time.Now().Add(2 * time.Hour).Unix())
	carrier, err := userop.EncodeNonceMAv2(entity, userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction, 0)
	require.NoError(t, err)
	digest, err := userop.DeferredActionDigest(chainID, account, carrier, deadline, installCall)
	require.NoError(t, err)
	ownerSig, err := crypto.Sign(digest.Bytes(), ownerKey)
	require.NoError(t, err)
	ownerSig[64] += 27
	encoded, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, installCall)
	require.NoError(t, err)
	return &preset.SessionAuthorization{
		EntityID: entity, SignerKey: ctrlKey,
		DeferredData: encoded, OwnerSignature: ownerSig, CarrierNonce: carrier,
		WrapExecuteUserOp: wrap, AllowlistRows: rows,
	}
}

func dummyNativeOp(sender, to common.Address, value *big.Int) *userop.UserOperationV07 {
	exec, _ := aa.PackExecute(to, value, nil)
	call, _ := aa.WrapExecuteUserOp(exec)
	return &userop.UserOperationV07{
		Sender: sender, Nonce: big.NewInt(0), CallData: call,
		CallGasLimit: big.NewInt(200_000), VerificationGasLimit: big.NewInt(700_000),
		PreVerificationGas: big.NewInt(80_000), MaxFeePerGas: big.NewInt(1), MaxPriorityFeePerGas: big.NewInt(1),
	}
}

func randomSessionEntity() uint32 {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return 100_000 + binary.BigEndian.Uint32(b)%1_000_000
}

func readNTLimits(t *testing.T, ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) *big.Int {
	t.Helper()
	sel := crypto.Keccak256([]byte("limits(uint256,address)"))[:4]
	data := append(sel, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	mod := aa.NativeTokenLimitModuleAddress()
	out, err := chain.CallContract(ctx, ethereum.CallMsg{To: &mod, Data: data}, nil)
	require.NoError(t, err)
	return new(big.Int).SetBytes(out)
}

func signedOpGasPrefund(op *userop.UserOperationV07) *big.Int {
	units := new(big.Int).Set(op.PreVerificationGas)
	units.Add(units, op.VerificationGasLimit)
	units.Add(units, op.CallGasLimit)
	if op.Paymaster != nil && *op.Paymaster != (common.Address{}) {
		if op.PaymasterVerificationGasLimit != nil {
			units.Add(units, op.PaymasterVerificationGasLimit)
		}
		if op.PaymasterPostOpGasLimit != nil {
			units.Add(units, op.PaymasterPostOpGasLimit)
		}
	}
	return new(big.Int).Mul(units, op.MaxFeePerGas)
}

type userOpReceiptJSON struct {
	Success       bool
	ActualGasCost *big.Int
}

func waitUserOpReceipt(t *testing.T, ctx context.Context, bundler *rpc.Client, op *userop.UserOperationV07, entry common.Address, chainID *big.Int) userOpReceiptJSON {
	t.Helper()
	hash, err := op.GetUserOpHash(entry, chainID)
	require.NoError(t, err)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var raw json.RawMessage
		if err := bundler.CallContext(ctx, &raw, "eth_getUserOperationReceipt", hash.Hex()); err == nil &&
			len(raw) > 0 && string(raw) != "null" {
			var parsed struct {
				Success       bool   `json:"success"`
				ActualGasCost string `json:"actualGasCost"`
			}
			require.NoError(t, json.Unmarshal(raw, &parsed))
			cost := parseUint(parsed.ActualGasCost)
			require.NotNil(t, cost, "actualGasCost %q", parsed.ActualGasCost)
			return userOpReceiptJSON{Success: parsed.Success, ActualGasCost: cost}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("no UserOp receipt for %s", hash.Hex())
	return userOpReceiptJSON{}
}

func parseUint(s string) *big.Int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	n := new(big.Int)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if _, ok := n.SetString(s[2:], 16); !ok {
			return nil
		}
		return n
	}
	if _, ok := n.SetString(s, 10); !ok {
		return nil
	}
	return n
}

func sendETH(ctx context.Context, chain *ethclient.Client, chainID *big.Int, key *ecdsa.PrivateKey, from, to common.Address, amount *big.Int) error {
	nonce, err := chain.PendingNonceAt(ctx, from)
	if err != nil {
		return err
	}
	tip, err := chain.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	head, err := chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	fee := eip1559.MaxFeeFromTipAndBase(tip, head.BaseFee)
	gas, err := chain.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Value: amount})
	if err != nil {
		return err
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, GasTipCap: tip, GasFeeCap: fee,
		Gas: gas + 10_000, To: &to, Value: amount,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return err
	}
	for i := 0; i < 40; i++ {
		r, err := chain.TransactionReceipt(ctx, signed.Hash())
		if err == nil {
			if r.Status != types.ReceiptStatusSuccessful {
				return fmt.Errorf("funding tx %s failed", signed.Hash())
			}
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("funding tx %s not mined", signed.Hash())
}

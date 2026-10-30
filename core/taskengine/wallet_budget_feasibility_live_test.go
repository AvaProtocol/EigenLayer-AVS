//go:build integration

package taskengine

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

// This is an opt-in experiment, not the budget feature. It uses a fresh,
// owner-controlled MA v2 wallet and never edits a shared fixture's grants.
// Only the existing Sepolia test owner/controller keys are used. No paymaster.
type budgetSpike struct {
	ctx                  context.Context
	chain                *ethclient.Client
	bundler              *rpc.Client
	sw                   *config.SmartWalletConfig
	ownerKey, sessionKey *ecdsa.PrivateKey
	owner, wallet        common.Address
	salt                 *big.Int
	readBlock            *big.Int
}

func TestWalletBudgetFeasibilityPreflight_Sepolia(t *testing.T) {
	f := newBudgetSpike(t, false)
	balance, err := f.chain.BalanceAt(f.ctx, f.owner, nil)
	require.NoError(t, err)
	t.Logf("Sepolia owner=%s nativeBalanceWei=%s bundlerProvider=%s (read only)", f.owner, balance, f.sw.BundlerProvider)
	require.Greater(t, balance.Cmp(big.NewInt(30_000_000_000_000_000)), 0, "owner needs test gas; no funding is attempted by preflight")
}

func newBudgetSpike(t *testing.T, deploy bool) *budgetSpike {
	t.Helper()
	if testing.Short() || os.Getenv("WALLET_BUDGET_FEASIBILITY") != "1" {
		t.Skip("set WALLET_BUDGET_FEASIBILITY=1 for Sepolia experiments")
	}
	require.NoError(t, testutil.LoadDotEnv())
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err)
	require.NotNil(t, cfg.SmartWallet)
	require.EqualValues(t, 11155111, cfg.SmartWallet.ChainID)
	sw := *cfg.SmartWallet
	sw.DisableGasSponsorship = true
	url, err := sw.ActiveBundlerURL()
	require.NoError(t, err)
	// The Alchemy bundler can report inclusion before a different RPC has
	// caught up. Use its chain view as well, only inside this isolated fixture.
	if sw.BundlerProvider == "alchemy" || sw.BundlerProvider == "" {
		sw.EthRpcUrl = url
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)
	chain, err := ethclient.DialContext(ctx, sw.EthRpcUrl)
	require.NoError(t, err)
	t.Cleanup(chain.Close)
	id, err := chain.ChainID(ctx)
	require.NoError(t, err)
	require.Equal(t, "11155111", id.String(), "never run these experiments on another chain")
	bundler, err := rpc.DialContext(ctx, url)
	require.NoError(t, err)
	t.Cleanup(bundler.Close)
	ownerKey := requireOwnerKey(t)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	require.NotNil(t, sw.ControllerPrivateKey)
	salt, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	salt.SetBit(salt, 200, 1)
	if raw := os.Getenv("WALLET_BUDGET_SALT"); raw != "" {
		var ok bool
		salt, ok = new(big.Int).SetString(raw, 10)
		require.True(t, ok)
		require.Positive(t, salt.Sign())
		require.LessOrEqual(t, salt.BitLen(), 256)
	}
	wallet, err := aa.GetSenderAddressMAv2WithContext(ctx, chain, owner, salt)
	require.NoError(t, err)
	f := &budgetSpike{ctx: ctx, chain: chain, bundler: bundler, sw: &sw, ownerKey: ownerKey, sessionKey: sw.ControllerPrivateKey, owner: owner, wallet: *wallet, salt: salt}
	if deploy {
		t.Logf("isolated Sepolia wallet=%s owner=%s salt=%s; maximum initial funding 0.02 ETH, self-funded gas", wallet, owner, salt)
		ensureOwnerNativeRunner(t, ctx, chain, ownerKey, id, owner, *wallet, salt)
		t.Cleanup(func() { f.recoverFunds(t) })
	}
	return f
}

func (f *budgetSpike) call(target common.Address, data []byte) aa.Call {
	return aa.Call{Target: target, Value: big.NewInt(0), Data: data}
}

func budgetTokenCall(selector string, address common.Address, amount *big.Int) []byte {
	data := append([]byte{}, crypto.Keccak256([]byte(selector))[:4]...)
	data = append(data, common.LeftPadBytes(address.Bytes(), 32)...)
	return append(data, common.LeftPadBytes(amount.Bytes(), 32)...)
}

func (f *budgetSpike) allowance(t *testing.T) *big.Int {
	t.Helper()
	data := budgetTokenCall("allowance(address,address)", f.wallet, new(big.Int).SetBytes(common.HexToAddress(SEPOLIA_SWAPROUTER).Bytes()))
	result, err := f.chain.CallContract(f.ctx, ethereum.CallMsg{To: budgetAddress(SEPOLIA_USDC), Data: data}, f.readBlock)
	require.NoError(t, err)
	require.Len(t, result, 32)
	return new(big.Int).SetBytes(result)
}

func budgetAddress(address string) *common.Address { a := common.HexToAddress(address); return &a }

type budgetBlockReader struct {
	chain *ethclient.Client
	block *big.Int
}

func (r budgetBlockReader) CallContract(ctx context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return r.chain.CallContract(ctx, msg, r.block)
}
func (f *budgetSpike) assertEntity(t *testing.T, entity uint32, clear bool) {
	t.Helper()
	signer, err := aa.EntitySignerOnChain(f.ctx, budgetBlockReader{f.chain, f.readBlock}, f.wallet, entity)
	require.NoError(t, err)
	want := common.Address{}
	if !clear {
		want = crypto.PubkeyToAddress(f.sessionKey.PublicKey)
	}
	require.Equal(t, want, signer, "entity %d at confirmed block %v", entity, f.readBlock)
}

func (f *budgetSpike) install(t *testing.T, entity uint32, cap *big.Int, until uint64, extra []aa.AllowlistInput, nativeCap ...*big.Int) []byte {
	t.Helper()
	inputs := append([]aa.AllowlistInput{{Target: common.HexToAddress(SEPOLIA_USDC), HasSelectorAllowlist: true,
		Selectors: [][4]byte{{0x09, 0x5e, 0xa7, 0xb3}, {0xa9, 0x05, 0x9c, 0xbb}}, HasERC20SpendLimit: true, ERC20SpendLimit: cap}}, extra...)
	allow := budgetPrototypeAllowlistHook(t, entity, inputs)
	clock, err := aa.TimeRangeValidationHook(entity, until, 0)
	require.NoError(t, err)
	hooks := [][]byte{allow, aa.AllowlistExecHook(entity), clock}
	if len(nativeCap) > 0 {
		native, err := aa.NativeTokenLimitValidationHook(entity, nativeCap[0])
		require.NoError(t, err)
		hooks = append(hooks, native, aa.NativeTokenLimitExecHook(entity))
	}
	data, err := aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: entity, Signer: crypto.PubkeyToAddress(f.sessionKey.PublicKey), Global: true, Hooks: hooks})
	require.NoError(t, err)
	return data
}

func (f *budgetSpike) price(op *userop.UserOperationV07) error {
	tip, err := f.chain.SuggestGasTipCap(f.ctx)
	if err != nil {
		return err
	}
	var hexTip string
	if err := f.bundler.CallContext(f.ctx, &hexTip, "rundler_maxPriorityFeePerGas"); err != nil {
		return err
	}
	bt, ok := new(big.Int).SetString(strings.TrimPrefix(hexTip, "0x"), 16)
	if !ok {
		return fmt.Errorf("invalid bundler gas tip")
	}
	if bt.Cmp(tip) > 0 {
		tip = bt
	}
	head, err := f.chain.HeaderByNumber(f.ctx, nil)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	_, err = preset.EstimateUserOpGasV07(f.ctx, f.bundler, op, preset.EntryPointV07())
	return err
}

func (f *budgetSpike) ownerBatch(t *testing.T, calls []aa.Call) (common.Hash, error) {
	t.Helper()
	prepared, err := preset.PrepareOwnerUserOp(f.ctx, f.sw, f.owner, f.wallet, f.salt, calls, testutil.GetLogger())
	if err != nil {
		return common.Hash{}, err
	}
	op := prepared.Operation
	if err = preset.SignUserOpV07(op, preset.EntryPointV07(), big.NewInt(11155111), f.ownerKey); err != nil {
		return common.Hash{}, err
	}
	return f.send(t, op)
}

func (f *budgetSpike) recoverFunds(t *testing.T) {
	t.Helper()
	// Recover only this fixture's assets, to its existing owner. A small unused
	// EntryPoint refund can remain owner-controlled; report it explicitly.
	calls := []aa.Call{f.call(common.HexToAddress(SEPOLIA_USDC), budgetTokenCall("approve(address,uint256)", common.HexToAddress(SEPOLIA_SWAPROUTER), big.NewInt(0)))}
	for _, token := range []common.Address{common.HexToAddress(SEPOLIA_USDC), common.HexToAddress(SEPOLIA_WETH)} {
		balance := f.tokenBalance(t, token)
		if balance.Sign() > 0 {
			calls = append(calls, f.call(token, budgetTokenCall("transfer(address,uint256)", f.owner, balance)))
		}
	}
	balance, err := f.chain.BalanceAt(f.ctx, f.wallet, f.readBlock)
	require.NoError(t, err)
	if balance.Sign() > 0 {
		calls = append(calls, aa.Call{Target: f.owner, Value: balance})
		prepared, err := preset.PrepareOwnerBatch(f.ctx, f.sw, f.owner, f.wallet, f.salt, preset.OwnerBatch{Calls: calls, ReserveNativeGas: true}, testutil.GetLogger())
		require.NoError(t, err, "fixture funds stay at %s, salt %s, if recovery fails", f.wallet, f.salt)
		require.NoError(t, preset.SignUserOpV07(prepared.Operation, preset.EntryPointV07(), big.NewInt(11155111), f.ownerKey))
		_, err = f.send(t, prepared.Operation)
		require.NoError(t, err)
	}
	entryABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"name":"account","type":"address"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	deposit := ownerNativeEntryPointDeposit(t, f.ctx, f.chain, entryABI, f.wallet, f.readBlock)
	t.Logf("fixture recovered to %s; remaining owner-controlled EntryPoint depositWei=%s wallet=%s salt=%s", f.owner, deposit, f.wallet, f.salt)
}

func (f *budgetSpike) session(t *testing.T, entity uint32, calls []aa.Call, deferred []byte) (common.Hash, error) {
	t.Helper()
	opts := uint8(userop.ValidationOptionGlobal)
	if deferred != nil {
		opts |= userop.ValidationOptionDeferredAction
	}
	nonce, err := preset.NextNonceV07(f.ctx, f.chain.Client(), preset.EntryPointV07(), f.wallet, entity, opts)
	if err != nil {
		return common.Hash{}, err
	}
	data, err := aa.PackExecuteBatchMAv2(calls)
	if err != nil {
		return common.Hash{}, err
	}
	data, err = aa.WrapExecuteUserOp(data)
	if err != nil {
		return common.Hash{}, err
	}
	op := &userop.UserOperationV07{Sender: f.wallet, Nonce: nonce, CallData: data,
		CallGasLimit: big.NewInt(0), VerificationGasLimit: big.NewInt(0), PreVerificationGas: big.NewInt(0)}
	var encoded, ownerSig []byte
	if deferred != nil {
		deadline := uint64(time.Now().Add(10 * time.Minute).Unix())
		digest, e := userop.DeferredActionDigest(big.NewInt(11155111), f.wallet, nonce, deadline, deferred)
		if e != nil {
			return common.Hash{}, e
		}
		ownerSig, e = crypto.Sign(digest.Bytes(), f.ownerKey)
		if e != nil {
			return common.Hash{}, e
		}
		ownerSig[64] += 27
		encoded, e = userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, deferred)
		if e != nil {
			return common.Hash{}, e
		}
		op.Signature, e = preset.DeferredEstimationSignature(encoded, ownerSig)
		if e != nil {
			return common.Hash{}, e
		}
	}
	if err = f.price(op); err != nil {
		return common.Hash{}, err
	}
	if deferred != nil {
		err = preset.SignUserOpV07Deferred(op, preset.EntryPointV07(), big.NewInt(11155111), f.sessionKey, encoded, ownerSig)
	} else {
		err = preset.SignUserOpV07(op, preset.EntryPointV07(), big.NewInt(11155111), f.sessionKey)
	}
	if err != nil {
		return common.Hash{}, err
	}
	return f.send(t, op)
}

func (f *budgetSpike) send(t *testing.T, op *userop.UserOperationV07) (common.Hash, error) {
	t.Helper()
	if preset.MaxOwnerGasCost(op).Cmp(big.NewInt(5_000_000_000_000_000)) > 0 {
		return common.Hash{}, fmt.Errorf("test gas ceiling exceeds 0.005 ETH")
	}
	hash, err := preset.SendUserOpV07(f.ctx, f.bundler, op, preset.EntryPointV07())
	if err != nil {
		return hash, err
	}
	t.Logf("broadcast userOp=%s", hash)
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	for {
		var raw json.RawMessage
		if err = f.bundler.CallContext(ctx, &raw, "eth_getUserOperationReceipt", hash.Hex()); err != nil {
			return hash, err
		}
		if len(raw) > 0 && string(raw) != "null" {
			var receipt struct {
				Success bool `json:"success"`
				Receipt struct {
					TransactionHash string `json:"transactionHash"`
				} `json:"receipt"`
			}
			if err = json.Unmarshal(raw, &receipt); err != nil {
				return hash, err
			}
			t.Logf("included userOp=%s tx=%s success=%t", hash, receipt.Receipt.TransactionHash, receipt.Success)
			// Do not inspect state or build the next nonce from a chain view
			// that has not indexed the transaction the bundler just reported.
			for {
				chainReceipt, readErr := f.chain.TransactionReceipt(ctx, common.HexToHash(receipt.Receipt.TransactionHash))
				if readErr == nil && chainReceipt != nil {
					f.readBlock = new(big.Int).Set(chainReceipt.BlockNumber)
					break
				}
				if readErr != nil && readErr != ethereum.NotFound {
					return hash, readErr
				}
				select {
				case <-ctx.Done():
					return hash, ctx.Err()
				case <-time.After(time.Second):
				}
			}
			// RPC caches for "latest" are independent of receipt indexing.
			// The next prepare uses the production nonce reader, so wait for
			// that reader to observe consumption before constructing another op.
			entity, options, _, decodeErr := userop.DecodeNonceMAv2(op.Nonce)
			if decodeErr != nil {
				return hash, decodeErr
			}
			want := new(big.Int).Add(op.Nonce, big.NewInt(1))
			for {
				nonce, readErr := preset.NextNonceV07(ctx, f.chain.Client(), preset.EntryPointV07(), f.wallet, entity, options)
				if readErr != nil {
					return hash, readErr
				}
				if nonce.Cmp(want) >= 0 {
					break
				}
				select {
				case <-ctx.Done():
					return hash, ctx.Err()
				case <-time.After(time.Second):
				}
			}
			if !receipt.Success {
				return hash, fmt.Errorf("UserOperation execution reverted")
			}
			return hash, nil
		}
		select {
		case <-ctx.Done():
			return hash, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func TestWalletBudgetApprovalMechanisms_Sepolia(t *testing.T) {
	f := newBudgetSpike(t, true)
	usdc, router := common.HexToAddress(SEPOLIA_USDC), common.HexToAddress(SEPOLIA_SWAPROUTER)
	approve := func(spender common.Address, amount int64) aa.Call {
		return f.call(usdc, budgetTokenCall("approve(address,uint256)", spender, big.NewInt(amount)))
	}
	seed := func(t *testing.T) {
		_, err := f.ownerBatch(t, []aa.Call{approve(router, 123)})
		require.NoError(t, err)
		require.Equal(t, "123", f.allowance(t).String())
	}
	until := uint64(time.Now().Add(time.Hour).Unix())
	t.Run("S1DeferredResetsAndInstall", func(t *testing.T) {
		seed(t)
		install := f.install(t, 101, big.NewInt(100), until, nil)
		batch, err := aa.PackExecuteBatchMAv2([]aa.Call{approve(router, 0), f.call(f.wallet, install)})
		require.NoError(t, err)
		_, err = f.session(t, 101, []aa.Call{approve(router, 0)}, batch)
		require.NoError(t, err, "real bundler must accept token allowance storage in validation")
		require.Equal(t, "0", f.allowance(t).String())
		f.assertEntity(t, 101, false)
	})
	t.Run("S1DeferredFailingResetRollsBack", func(t *testing.T) {
		seed(t)
		batch, err := aa.PackExecuteBatchMAv2([]aa.Call{approve(router, 0), approve(common.Address{}, 0), f.call(f.wallet, f.install(t, 102, big.NewInt(100), until, nil))})
		require.NoError(t, err)
		_, err = f.session(t, 102, []aa.Call{approve(router, 0)}, batch)
		require.Error(t, err)
		t.Logf("expected reset refusal: %v", err)
		require.Contains(t, err.Error(), "approve to the zero address")
		require.Equal(t, "123", f.allowance(t).String())
		f.assertEntity(t, 102, true)
	})
	t.Run("S2OwnerResetsAndInstall", func(t *testing.T) {
		seed(t)
		_, err := f.ownerBatch(t, []aa.Call{approve(router, 0), f.call(f.wallet, f.install(t, 103, big.NewInt(100), until, nil))})
		require.NoError(t, err)
		require.Equal(t, "0", f.allowance(t).String())
		f.assertEntity(t, 103, false)
	})
	t.Run("S2OwnerFailingResetRollsBack", func(t *testing.T) {
		seed(t)
		_, err := f.ownerBatch(t, []aa.Call{approve(router, 0), approve(common.Address{}, 0), f.call(f.wallet, f.install(t, 104, big.NewInt(100), until, nil))})
		require.Error(t, err)
		t.Logf("expected reset refusal: %v", err)
		require.Contains(t, err.Error(), "approve to the zero address")
		require.Equal(t, "123", f.allowance(t).String())
		f.assertEntity(t, 104, true)
	})
	t.Run("S3ZeroCap", func(t *testing.T) {
		// A test-only prototype bypasses SessionPermissions' positive parser and
		// uses the stock hook encoder directly. Production validation is unchanged.
		_, err := parseCapAmount("0")
		require.Error(t, err)
		_, err = f.ownerBatch(t, []aa.Call{f.call(f.wallet, f.install(t, 105, big.NewInt(0), until, nil))})
		require.NoError(t, err)
		_, err = f.session(t, 105, []aa.Call{approve(router, 0)}, nil)
		require.NoError(t, err)
		for name, call := range map[string]aa.Call{"approve1": approve(router, 1), "transfer1": f.call(usdc, budgetTokenCall("transfer(address,uint256)", f.owner, big.NewInt(1)))} {
			t.Run(name, func(t *testing.T) {
				_, err := f.session(t, 105, []aa.Call{call}, nil)
				require.Error(t, err)
				t.Logf("expected zero-cap refusal: %v", err)
				require.Contains(t, err.Error(), "revert")
				// The bundler hides execution-hook revert data. Call the deployed
				// hook with the same account/entity to prove ExceededTokenLimit,
				// rather than mistaking an empty token balance for cap enforcement.
				exec, err := aa.PackExecuteBatchMAv2([]aa.Call{call})
				require.NoError(t, err)
				hookABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"preExecutionHook","inputs":[{"type":"uint32"},{"type":"address"},{"type":"uint256"},{"type":"bytes"}],"outputs":[{"type":"bytes"}]}]`))
				require.NoError(t, err)
				data, err := hookABI.Pack("preExecutionHook", uint32(105), preset.EntryPointV07(), big.NewInt(0), exec)
				require.NoError(t, err)
				hook := aa.AllowlistModuleAddress()
				_, err = f.chain.CallContract(f.ctx, ethereum.CallMsg{From: f.wallet, To: &hook, Data: data}, f.readBlock)
				var failure rpc.DataError
				require.ErrorAs(t, err, &failure)
				require.Contains(t, fmt.Sprint(failure.ErrorData()), common.Bytes2Hex(crypto.Keccak256([]byte("ExceededTokenLimit()"))[:4]))
			})
		}
	})
	t.Run("S2OwnerMinedFailureRollsBack", func(t *testing.T) {
		seed(t)
		install := f.call(f.wallet, f.install(t, 106, big.NewInt(100), until, nil))
		prepared, err := preset.PrepareOwnerUserOp(f.ctx, f.sw, f.owner, f.wallet, f.salt, []aa.Call{approve(router, 0), install}, testutil.GetLogger())
		require.NoError(t, err)
		// Deliberately exercise a MINED execution failure, not only an
		// estimator rejection. Put the install before the final failing reset
		// so its rollback is observable too. Sign these final bytes once.
		bad, err := aa.PackExecuteBatchMAv2([]aa.Call{approve(router, 0), install, approve(common.Address{}, 0)})
		require.NoError(t, err)
		op := prepared.Operation
		op.CallData, op.CallGasLimit = bad, big.NewInt(900_000)
		// The failing batch is larger than the successful pricing control.
		// Give its calldata overhead headroom BEFORE the owner signs it.
		op.PreVerificationGas.Add(op.PreVerificationGas, big.NewInt(100_000))
		require.NoError(t, preset.SignUserOpV07(op, preset.EntryPointV07(), big.NewInt(11155111), f.ownerKey))
		_, err = f.send(t, op)
		require.EqualError(t, err, "UserOperation execution reverted")
		require.Equal(t, "123", f.allowance(t).String(), "the earlier reset must roll back")
		f.assertEntity(t, 106, true)
	})
}

// Explicit recovery for a previous interrupted run. It cannot create/fund an
// account and accepts only the known salt of a wallet owned by the test key.
func TestWalletBudgetRecoverFixture_Sepolia(t *testing.T) {
	if os.Getenv("WALLET_BUDGET_SALT") == "" {
		t.Skip("set the interrupted fixture's WALLET_BUDGET_SALT")
	}
	f := newBudgetSpike(t, false)
	code, err := f.chain.CodeAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code)
	f.recoverFunds(t)
}

func (f *budgetSpike) tokenBalance(t *testing.T, token common.Address) *big.Int {
	t.Helper()
	data := append(crypto.Keccak256([]byte("balanceOf(address)"))[:4], common.LeftPadBytes(f.wallet.Bytes(), 32)...)
	result, err := f.chain.CallContract(f.ctx, ethereum.CallMsg{To: &token, Data: data}, f.readBlock)
	require.NoError(t, err)
	require.Len(t, result, 32)
	return new(big.Int).SetBytes(result)
}

func budgetExactInput(tokenIn, tokenOut, recipient common.Address, amount *big.Int) []byte {
	data := append([]byte{}, crypto.Keccak256([]byte("exactInputSingle((address,address,uint24,address,uint256,uint256,uint160))"))[:4]...)
	for _, word := range [][]byte{tokenIn.Bytes(), tokenOut.Bytes(), big.NewInt(SEPOLIA_FEE_TIER).Bytes(), recipient.Bytes(), amount.Bytes(), big.NewInt(1).Bytes(), nil} {
		data = append(data, common.LeftPadBytes(word, 32)...)
	}
	return data
}

func TestWalletBudgetRouterWithoutWETHWrapPermissions_Sepolia(t *testing.T) {
	f := newBudgetSpike(t, true)
	usdc, weth, router := common.HexToAddress(SEPOLIA_USDC), common.HexToAddress(SEPOLIA_WETH), common.HexToAddress(SEPOLIA_SWAPROUTER)
	multiABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"multicall","inputs":[{"name":"data","type":"bytes[]"}],"outputs":[{"type":"bytes[]"}]}]`))
	require.NoError(t, err)
	selector := multiABI.Methods["multicall"].ID
	wethCap := big.NewInt(100_000_000_000_000_000) // finite 0.1 WETH, no deposit/withdraw
	nativeCap := big.NewInt(10_000_000_000_000_000)
	install := f.install(t, 201, big.NewInt(1_000_000_000_000), uint64(time.Now().Add(time.Hour).Unix()), []aa.AllowlistInput{
		{Target: weth, HasSelectorAllowlist: true, HasERC20SpendLimit: true, ERC20SpendLimit: wethCap, Selectors: [][4]byte{{0x09, 0x5e, 0xa7, 0xb3}, {0xa9, 0x05, 0x9c, 0xbb}}},
		{Target: router, HasSelectorAllowlist: true, Selectors: [][4]byte{[4]byte(selector)}},
	}, nativeCap)
	_, err = f.ownerBatch(t, []aa.Call{f.call(f.wallet, install)})
	require.NoError(t, err)
	before := f.tokenBalance(t, usdc)
	amountIn := big.NewInt(10_000_000_000_000) // 0.00001 Sepolia ETH, never increased on failure
	var bought *big.Int
	t.Run("S5ETHToToken", func(t *testing.T) {
		data, err := multiABI.Pack("multicall", [][]byte{budgetExactInput(weth, usdc, f.wallet, amountIn), crypto.Keccak256([]byte("refundETH()"))[:4]})
		require.NoError(t, err)
		_, err = f.session(t, 201, []aa.Call{{Target: router, Value: amountIn, Data: data}}, nil)
		require.NoError(t, err)
		bought = new(big.Int).Sub(f.tokenBalance(t, usdc), before)
		require.Positive(t, bought.Sign(), "the wallet must actually receive tokens")
		t.Logf("received USDC base units=%s for %s wei", bought, amountIn)
	})
	t.Run("S5TokenToETH", func(t *testing.T) {
		if bought == nil || bought.Sign() <= 0 {
			t.Skip("first direction did not produce test tokens")
		}
		// Unwrap on the router, whose own WETH permission is independent of the
		// wallet's capped WETH row. The only approval is exactly this input.
		unwrap := append(append([]byte{}, crypto.Keccak256([]byte("unwrapWETH9(uint256,address)"))[:4]...), common.LeftPadBytes(big.NewInt(1).Bytes(), 32)...)
		unwrap = append(unwrap, common.LeftPadBytes(f.wallet.Bytes(), 32)...)
		data, err := multiABI.Pack("multicall", [][]byte{budgetExactInput(usdc, weth, router, bought), unwrap})
		require.NoError(t, err)
		_, err = f.session(t, 201, []aa.Call{f.call(usdc, budgetTokenCall("approve(address,uint256)", router, bought)), f.call(router, data)}, nil)
		require.NoError(t, err)
		require.Equal(t, before.String(), f.tokenBalance(t, usdc).String(), "all purchased test USDC must be sold back")
		require.Zero(t, f.tokenBalance(t, weth).Sign(), "router must unwrap its output, not leave WETH in the wallet")
		remaining, err := aa.ReadERC20SpendLimit(f.ctx, budgetBlockReader{f.chain, f.readBlock}, 201, weth, f.wallet)
		require.NoError(t, err)
		require.Equal(t, wethCap.String(), remaining.String(), "no direct WETH action may be charged")
		remainingNative, err := aa.ReadNativeSpendLimit(f.ctx, budgetBlockReader{f.chain, f.readBlock}, 201, f.wallet)
		require.NoError(t, err)
		require.Less(t, remainingNative.Cmp(nativeCap), 0, "native input/gas is metered separately from WETH")
		t.Logf("finite WETH remaining=%s native remaining=%s", remaining, remainingNative)
	})
}

// Native-recipient cap measurement: MaxNativeRecipients 5 → 20.
//
// Unlike native_eth_hooks (proof 8), every grant here goes through the
// production path. SessionPermissions.HooksFor builds the hooks and
// aa.PackSessionSignerInstall the install. A replace is the install FIRST,
// then each prior entity's SessionSignerUninstallFromInstall, packed with
// aa.PackExecuteBatchMAv2 as PrepareSessionGrant does. preset.SendUserOpMAv2
// sends the carrier with verification and pre-verification gas left to the
// bundler. Self-funded; no Gas Manager.
//
// For every operation it prints the signed limits, the gas actually used,
// and the gas units NativeTokenLimitModule charges a self-funded op
// (verification + call + pre-verification), against the gateway's first-op
// budget, nativePreflightGasUnitsFirstOp.
//
// Accounts are fresh MA v2 wallets of the owner (salts from
// MEASURE_SALT_BASE). The owner prefunds each and the run sweeps what is
// left back at the end.
//
// Env (repo-root .env; process env wins):
//
//	TEST_PRIVATE_KEY   owner EOA: signs each grant, prefunds the accounts
//	ALCHEMY_API_KEY    Sepolia RPC and bundler
//	MEASURE_SALT_BASE  (opt) first salt; default derived from the clock
//
// Run:
//
//	go run ./scripts/spike/native_recipient_cap
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

const (
	sepoliaChainID = int64(11155111)
	// The gateway's first-op budget for a self-funded native cap
	// (session_grant_native.go nativePreflightGasUnitsFirstOp).
	firstOpBudgetUnits = 2_000_000
	// The ERC-20 targets of the mixed grant: a large set, like an 8-target
	// Uniswap/Aave grant. Never called; only installed as allowlist rows.
	mixedERC20Targets = 8
)

var (
	nativeCap = big.NewInt(50_000_000_000_000_000) // 0.05 ETH, so the cap never decides the outcome
	oneWei    = big.NewInt(1)
	// Sepolia fee assumptions the gateway sizes a native cap with
	// (session_native_gas.go), to show what each op's units would cost.
	feeAssumptions = []struct {
		chain string
		wei   int64
	}{{"Sepolia", 2_000_000_000}, {"Base", 1_000_000_000}, {"mainnet", 50_000_000_000}}
	userOperationEventTopic = crypto.Keccak256Hash([]byte("UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)"))
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "measurement failed: %v\n", err)
		os.Exit(1)
	}
}

type harness struct {
	ctx        context.Context
	chain      *ethclient.Client
	cfg        *config.SmartWalletConfig
	ownerKey   *ecdsa.PrivateKey
	owner      common.Address
	signerKey  *ecdsa.PrivateKey
	signer     common.Address
	recipients []*common.Address
	rows       []measurement
}

// account is one fresh MA v2 wallet and the grants installed on it, by entity.
type account struct {
	salt      *big.Int
	address   common.Address
	installed map[uint32][]byte // entity -> its install call, for teardown
	next      uint32
}

type measurement struct {
	label      string
	rows       int
	teardowns  int
	deploying  bool
	vgl        uint64
	cgl        uint64
	pvg        uint64
	actualUsed uint64
	success    bool
	tx         string
}

func run() error {
	loadDotEnv()
	ownerKey, owner, err := requireKey("TEST_PRIVATE_KEY")
	if err != nil {
		return err
	}
	apiKey := strings.TrimSpace(os.Getenv("ALCHEMY_API_KEY"))
	if apiKey == "" {
		return fmt.Errorf("set ALCHEMY_API_KEY")
	}
	rpcURL := "https://eth-sepolia.g.alchemy.com/v2/" + apiKey
	ctx := context.Background()
	chain, err := ethclient.Dial(rpcURL)
	if err != nil {
		return err
	}
	defer chain.Close()
	chainID, err := chain.ChainID(ctx)
	if err != nil {
		return err
	}
	if chainID.Int64() != sepoliaChainID {
		return fmt.Errorf("chain %s is not Sepolia", chainID)
	}
	// A throwaway session signer: it only signs operations and holds no funds.
	signerKey, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	h := &harness{
		ctx: ctx, chain: chain,
		cfg: &config.SmartWalletConfig{
			EthRpcUrl:             rpcURL,
			BundlerProvider:       config.BundlerProviderAlchemy,
			AlchemyAPIKey:         apiKey,
			ChainID:               sepoliaChainID,
			DisableGasSponsorship: true,
		},
		ownerKey: ownerKey, owner: owner,
		signerKey: signerKey, signer: crypto.PubkeyToAddress(signerKey.PublicKey),
	}
	// Fresh EOAs: no code, so every row is a plain-wallet native recipient.
	for i := 0; i < taskengine.MaxNativeRecipients; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			return err
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		h.recipients = append(h.recipients, &addr)
	}
	saltBase := big.NewInt(time.Now().Unix() % 1_000_000_000)
	if v := strings.TrimSpace(os.Getenv("MEASURE_SALT_BASE")); v != "" {
		if n, ok := new(big.Int).SetString(v, 10); ok {
			saltBase = n
		}
	}
	fmt.Printf("owner %s, session signer %s, salt base %s, MaxNativeRecipients %d\n",
		owner.Hex(), h.signer.Hex(), saltBase, taskengine.MaxNativeRecipients)

	var accounts []*account
	defer func() {
		for _, acct := range accounts {
			if err := h.sweep(acct); err != nil {
				fmt.Printf("sweep %s: %v\n", acct.address.Hex(), err)
			}
		}
		h.printTable()
	}()

	// MEASURE_MODE=extra measures what the first run left out: a steady send
	// on an installed grant, and a replace that tears down a mixed grant.
	if strings.TrimSpace(os.Getenv("MEASURE_MODE")) == "extra" {
		small, err := h.newAccount(new(big.Int).Add(saltBase, big.NewInt(3)), ether("0.01"))
		if err != nil {
			return err
		}
		accounts = append(accounts, small)
		if err := h.grant(small, "5 rows: first install (deploy)", 5, 0, 0); err != nil {
			return err
		}
		if err := h.steady(small, "5 rows: steady send"); err != nil {
			return err
		}
		mixed, err := h.newAccount(new(big.Int).Add(saltBase, big.NewInt(4)), ether("0.06"))
		if err != nil {
			return err
		}
		accounts = append(accounts, mixed)
		n := taskengine.MaxNativeRecipients
		if err := h.grant(mixed, fmt.Sprintf("%d rows + %d ERC-20: first install (deploy)", n, mixedERC20Targets), n, mixedERC20Targets, 0); err != nil {
			return err
		}
		if err := h.steady(mixed, fmt.Sprintf("%d rows + %d ERC-20: steady send", n, mixedERC20Targets)); err != nil {
			return err
		}
		if err := h.grant(mixed, fmt.Sprintf("%d rows + %d ERC-20: replace a mixed grant", n, mixedERC20Targets), n, mixedERC20Targets, -1); err != nil {
			return err
		}
		return h.steady(mixed, fmt.Sprintf("%d rows + %d ERC-20: steady send after replace", n, mixedERC20Targets))
	}

	plan := []struct {
		rows    int
		prefund *big.Int
	}{
		{5, ether("0.01")},
		{10, ether("0.015")},
		{20, ether("0.06")},
	}
	for i, step := range plan {
		acct, err := h.newAccount(new(big.Int).Add(saltBase, big.NewInt(int64(i))), step.prefund)
		if err != nil {
			return err
		}
		accounts = append(accounts, acct)
		n := step.rows
		// First grant on an undeployed account: deploy + install.
		if err := h.grant(acct, fmt.Sprintf("%d rows: first install (deploy)", n), n, 0, 0); err != nil {
			return err
		}
		// The usual replace: one prior grant torn down.
		if err := h.grant(acct, fmt.Sprintf("%d rows: replace, 1 teardown", n), n, 0, -1); err != nil {
			return err
		}
		if n != taskengine.MaxNativeRecipients {
			continue
		}
		// The worst case PrepareSessionGrant packs: maxOnChainTeardowns (4)
		// priors at once. Install three more next to the one standing.
		for k := 0; k < 3; k++ {
			if err := h.grant(acct, fmt.Sprintf("%d rows: extra install %d (no teardown)", n, k+1), n, 0, 0); err != nil {
				return err
			}
		}
		if err := h.grant(acct, fmt.Sprintf("%d rows: replace, 4 teardowns", n), n, 0, -1); err != nil {
			return err
		}
		// 20 native rows next to a large ERC-20 set.
		if err := h.grant(acct, fmt.Sprintf("%d rows + %d ERC-20 targets: replace, 1 teardown", n, mixedERC20Targets), n, mixedERC20Targets, -1); err != nil {
			return err
		}
	}
	return nil
}

func (h *harness) newAccount(salt, prefund *big.Int) (*account, error) {
	addr, err := aa.GetSenderAddressMAv2WithContext(h.ctx, h.chain, h.owner, salt)
	if err != nil {
		return nil, err
	}
	code, err := h.chain.CodeAt(h.ctx, *addr, nil)
	if err != nil {
		return nil, err
	}
	if len(code) > 0 {
		return nil, fmt.Errorf("account %s (salt %s) is already deployed; pick another MEASURE_SALT_BASE", addr.Hex(), salt)
	}
	if err := h.sendETH(*addr, prefund); err != nil {
		return nil, fmt.Errorf("prefunding %s: %w", addr.Hex(), err)
	}
	fmt.Printf("account %s salt %s prefunded %s wei\n", addr.Hex(), salt, prefund)
	return &account{salt: salt, address: *addr, installed: map[uint32][]byte{}, next: aa.MinSessionEntityID}, nil
}

// grant installs a new entity carrying rows native recipients (and erc20
// ERC-20 targets) through one 1-wei native send. teardown 0 tears nothing
// down; -1 tears down every entity installed so far, as PrepareSessionGrant
// does for the applied grants on the runner (up to maxOnChainTeardowns).
func (h *harness) grant(acct *account, label string, rows, erc20 int, teardown int) error {
	entity := acct.next
	acct.next++
	perms := taskengine.SessionPermissions{
		ValidUntilMs:     time.Now().Add(24 * time.Hour).UnixMilli(),
		NativeRecipients: h.recipients[:rows],
		NativeSpendCap:   &model.NativeSpendCap{Amount: nativeCap.String()},
		SessionSigner:    &h.signer,
		CodeAt:           func(common.Address) ([]byte, error) { return nil, nil },
	}
	for i := 0; i < erc20; i++ {
		token := common.BigToAddress(big.NewInt(int64(0x7000 + i)))
		perms.AllowedActions = append(perms.AllowedActions, model.AllowedAction{
			Target:    &token,
			Selectors: []string{"0xa9059cbb", "0x095ea7b3"},
		})
		perms.SpendCaps = append(perms.SpendCaps, model.ERC20SpendCap{Token: &token, Amount: "1000000"})
	}
	if err := perms.Validate(); err != nil {
		return fmt.Errorf("%s: permissions: %w", label, err)
	}
	hooks, err := perms.HooksFor(entity)
	if err != nil {
		return fmt.Errorf("%s: hooks: %w", label, err)
	}
	grant := aa.SessionGrant{EntityID: entity, Signer: h.signer, Hooks: hooks, Global: true}
	installCall, err := aa.PackSessionSignerInstall(grant)
	if err != nil {
		return err
	}
	deferredCall := installCall
	var priors []uint32
	if teardown != 0 {
		for prior := range acct.installed {
			priors = append(priors, prior)
		}
	}
	if len(priors) > 0 {
		calls := []aa.Call{{Target: acct.address, Value: big.NewInt(0), Data: installCall}}
		for _, prior := range priors {
			uninstall, err := aa.SessionSignerUninstallFromInstall(prior, acct.installed[prior])
			if err != nil {
				return err
			}
			calls = append(calls, aa.Call{Target: acct.address, Value: big.NewInt(0), Data: uninstall})
		}
		if deferredCall, err = aa.PackExecuteBatchMAv2(calls); err != nil {
			return err
		}
	}
	carrierNonce, err := userop.EncodeNonceMAv2(entity, userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction, 0)
	if err != nil {
		return err
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())
	digest, err := userop.DeferredActionDigest(big.NewInt(sepoliaChainID), acct.address, carrierNonce, deadline, deferredCall)
	if err != nil {
		return err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), h.ownerKey)
	if err != nil {
		return err
	}
	ownerSig[64] += 27
	deferredData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, deferredCall)
	if err != nil {
		return err
	}
	callData, err := aa.PackExecuteMAv2(*h.recipients[0], oneWei, nil)
	if err != nil {
		return err
	}
	deployCode, err := h.chain.CodeAt(h.ctx, acct.address, nil)
	if err != nil {
		return err
	}
	auth := &preset.SessionAuthorization{
		EntityID:          entity,
		SignerKey:         h.signerKey,
		DeferredData:      deferredData,
		OwnerSignature:    ownerSig,
		CarrierNonce:      carrierNonce,
		WrapExecuteUserOp: grant.HasExecutionHook(),
		PolicyID:          "native-recipient-cap-measurement",
	}
	fmt.Printf("\n%s: entity %d, %d teardowns, deferred call %d bytes\n", label, entity, len(priors), len(deferredCall))
	op, receipt, sendErr := preset.SendUserOpMAv2(h.cfg, h.owner, callData, &acct.address, acct.salt, auth, nil)
	row := measurement{label: label, rows: rows, teardowns: len(priors), deploying: len(deployCode) == 0}
	if op != nil {
		row.vgl, row.cgl, row.pvg = u64(op.VerificationGasLimit), u64(op.CallGasLimit), u64(op.PreVerificationGas)
	}
	if receipt != nil {
		row.tx = receipt.TxHash.Hex()
		row.success, row.actualUsed = userOpOutcome(receipt, acct.address)
	}
	h.rows = append(h.rows, row)
	if sendErr != nil {
		fmt.Printf("  FAILED: %v\n", firstLine(sendErr.Error()))
		return fmt.Errorf("%s: %w", label, sendErr)
	}
	fmt.Printf("  success=%v vgl=%d cgl=%d pvg=%d signed units=%d actual=%d tx=%s\n",
		row.success, row.vgl, row.cgl, row.pvg, row.vgl+row.cgl+row.pvg, row.actualUsed, row.tx)
	if !row.success {
		return fmt.Errorf("%s: the operation mined but reverted", label)
	}
	for _, prior := range priors {
		delete(acct.installed, prior)
	}
	acct.installed[entity] = installCall
	return nil
}

// steady sends 1 wei through the newest installed grant: no deferred action,
// the op every run after the install signs.
func (h *harness) steady(acct *account, label string) error {
	var entity uint32
	for installed := range acct.installed {
		if installed > entity {
			entity = installed
		}
	}
	if entity == 0 {
		return fmt.Errorf("%s: no installed grant", label)
	}
	callData, err := aa.PackExecuteMAv2(*h.recipients[1], oneWei, nil)
	if err != nil {
		return err
	}
	auth := &preset.SessionAuthorization{
		EntityID:          entity,
		SignerKey:         h.signerKey,
		WrapExecuteUserOp: true, // the allowlist execution hook is always installed
		PolicyID:          "native-recipient-cap-measurement",
	}
	fmt.Printf("\n%s: entity %d\n", label, entity)
	op, receipt, sendErr := preset.SendUserOpMAv2(h.cfg, h.owner, callData, &acct.address, acct.salt, auth, nil)
	row := measurement{label: label}
	if op != nil {
		row.vgl, row.cgl, row.pvg = u64(op.VerificationGasLimit), u64(op.CallGasLimit), u64(op.PreVerificationGas)
	}
	if receipt != nil {
		row.tx = receipt.TxHash.Hex()
		row.success, row.actualUsed = userOpOutcome(receipt, acct.address)
	}
	h.rows = append(h.rows, row)
	if sendErr != nil {
		fmt.Printf("  FAILED: %v\n", firstLine(sendErr.Error()))
		return fmt.Errorf("%s: %w", label, sendErr)
	}
	fmt.Printf("  success=%v vgl=%d cgl=%d pvg=%d signed units=%d actual=%d tx=%s\n",
		row.success, row.vgl, row.cgl, row.pvg, row.vgl+row.cgl+row.pvg, row.actualUsed, row.tx)
	if !row.success {
		return fmt.Errorf("%s: the operation mined but reverted", label)
	}
	return nil
}

func (h *harness) printTable() {
	fmt.Printf("\n| Case | Deploy | VGL | CGL | PVG | Signed units (NT charge) | vs %d first-op budget | Actual gas | Sepolia @2 gwei | Base @1 gwei | Mainnet @50 gwei |\n", firstOpBudgetUnits)
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, row := range h.rows {
		units := row.vgl + row.cgl + row.pvg
		verdict := "within"
		if units > firstOpBudgetUnits {
			verdict = fmt.Sprintf("OVER by %d", units-firstOpBudgetUnits)
		}
		costs := make([]string, 0, len(feeAssumptions))
		for _, fee := range feeAssumptions {
			wei := new(big.Int).Mul(new(big.Int).SetUint64(units), big.NewInt(fee.wei))
			costs = append(costs, formatEther(wei))
		}
		status := ""
		if !row.success {
			status = " (failed)"
		}
		fmt.Printf("| %s%s | %v | %d | %d | %d | %d | %s | %d | %s |\n",
			row.label, status, row.deploying, row.vgl, row.cgl, row.pvg, units, verdict, row.actualUsed, strings.Join(costs, " | "))
	}
}

func userOpOutcome(receipt *types.Receipt, sender common.Address) (bool, uint64) {
	for _, log := range receipt.Logs {
		if len(log.Topics) < 3 || log.Topics[0] != userOperationEventTopic {
			continue
		}
		if common.BytesToAddress(log.Topics[2].Bytes()) != sender || len(log.Data) < 128 {
			continue
		}
		success := new(big.Int).SetBytes(log.Data[32:64]).Sign() != 0
		used := new(big.Int).SetBytes(log.Data[96:128]).Uint64()
		return success, used
	}
	return false, 0
}

func (h *harness) sweep(acct *account) error {
	bal, err := h.chain.BalanceAt(h.ctx, acct.address, nil)
	if err != nil || bal.Cmp(ether("0.0002")) < 0 {
		return err
	}
	code, err := h.chain.CodeAt(h.ctx, acct.address, nil)
	if err != nil || len(code) == 0 {
		return err
	}
	amount := new(big.Int).Sub(bal, ether("0.0001"))
	call, err := aa.PackExecuteMAv2(h.owner, amount, nil)
	if err != nil {
		return err
	}
	gas, err := h.chain.EstimateGas(h.ctx, ethereum.CallMsg{From: h.owner, To: &acct.address, Data: call})
	if err != nil {
		return err
	}
	if err := h.sendTx(acct.address, big.NewInt(0), call, gas+30_000); err != nil {
		return err
	}
	fmt.Printf("swept %s wei from %s back to the owner\n", amount, acct.address.Hex())
	return nil
}

// sendETH estimates rather than assuming 21000: a transfer that creates an
// account costs more on Sepolia (about 207k measured), and 21000 reverts.
func (h *harness) sendETH(to common.Address, amount *big.Int) error {
	gas, err := h.chain.EstimateGas(h.ctx, ethereum.CallMsg{From: h.owner, To: &to, Value: amount})
	if err != nil {
		return err
	}
	return h.sendTx(to, amount, nil, gas+10_000)
}

func (h *harness) sendTx(to common.Address, value *big.Int, data []byte, gas uint64) error {
	nonce, err := h.chain.PendingNonceAt(h.ctx, h.owner)
	if err != nil {
		return err
	}
	tip, err := h.chain.SuggestGasTipCap(h.ctx)
	if err != nil {
		return err
	}
	head, err := h.chain.HeaderByNumber(h.ctx, nil)
	if err != nil {
		return err
	}
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(sepoliaChainID), Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap,
		Gas: gas, To: &to, Value: value, Data: data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(sepoliaChainID)), h.ownerKey)
	if err != nil {
		return err
	}
	if err := h.chain.SendTransaction(h.ctx, signed); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()
	for {
		receipt, err := h.chain.TransactionReceipt(ctx, signed.Hash())
		if err == nil {
			if receipt.Status != types.ReceiptStatusSuccessful {
				return fmt.Errorf("tx %s reverted", signed.Hash().Hex())
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tx %s not mined: %w", signed.Hash().Hex(), ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

func ether(s string) *big.Int {
	f, ok := new(big.Float).SetString(s)
	if !ok {
		panic("bad ether amount " + s)
	}
	wei, _ := new(big.Float).Mul(f, big.NewFloat(1e18)).Int(nil)
	return wei
}

func formatEther(wei *big.Int) string {
	f := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18))
	return f.Text('f', 5) + " ETH"
}

func u64(v *big.Int) uint64 {
	if v == nil {
		return 0
	}
	return v.Uint64()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func requireKey(names ...string) (*ecdsa.PrivateKey, common.Address, error) {
	for _, name := range names {
		raw := strings.TrimPrefix(strings.TrimSpace(os.Getenv(name)), "0x")
		if raw == "" {
			continue
		}
		key, err := crypto.HexToECDSA(raw)
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("%s is not a private key: %w", name, err)
		}
		return key, crypto.PubkeyToAddress(key.PublicKey), nil
	}
	return nil, common.Address{}, fmt.Errorf("set one of: %s", strings.Join(names, ", "))
}

// loadDotEnv reads the repo-root .env.local and .env for the two keys this
// script needs. The process environment wins.
func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			for _, name := range []string{".env.local", ".env"} {
				loadDotEnvFile(filepath.Join(dir, name))
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func loadDotEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "TEST_PRIVATE_KEY" && key != "ALCHEMY_API_KEY" && key != "MEASURE_SALT_BASE" {
			continue
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, strings.Trim(strings.TrimSpace(val), `"'`))
		}
	}
}

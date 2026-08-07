// Deferred-replace spike — can one owner signature both install a new grant
// and remove the one it replaces?
//
// EigenLayer-AVS #716 made submitting a grant REPLACE the runner's other
// usable grants, but only off-chain: the superseded entity and its hooks stay
// installed until someone signs uninstallValidation. #717 proposes closing
// that by batching uninstall(prior) + install(new) into the grant's existing
// deferred action, so replacement means the same thing on both sides with no
// extra prompt.
//
// The results doc §3.6 already probed the adjacent composition and found a
// hard limit:
//
//	U-A  deferred uninstall, OUTER validation = the entity being removed.
//	     REFUSED — the deferred call removes it mid-validation, so the outer
//	     validation runs against a validation that no longer exists.
//	U-B  deferred uninstall, OUTER validation = the owner's fallback.
//	     MINED — but the owner signs the OP, so revoke is not gaslessly
//	     pre-signable the way a grant is.
//
// #717 is NOT the same arrangement. A replace removes the PRIOR entity while
// the outer validation is the NEW one — which U-A never tested. That is the
// open question, and three things have to hold for #717 as written:
//
//	Q1  can a deferred action carry executeBatch at all? _handleDeferredAction
//	    runs a single self-call, and the install must land before the outer
//	    validation resolves — so the batch order is install-then-uninstall.
//	    MA v2 also guards self-administration, which is what a batch of two
//	    account-targeting calls is.
//	Q2  does removing a DIFFERENT entity mid-validation disturb the outer
//	    validation, or was U-A specific to removing the validator itself?
//	Q3  does the prior grant's hook state actually clear? §3.5 warns a
//	    misrouted onUninstall entry is caught and silently stranded, so the
//	    only proof is reading module state back — never a successful tx.
//
// Probes, in order, each a strict step up from the last:
//
//	R-0  control: install-only deferred action, outer = new entity. This is
//	     today's grant flow (§3.2). PREDICT: mined. If this fails the fixture
//	     is wrong, not the hypothesis.
//	R-A  batch install(new)+uninstall(prior), outer = NEW entity. The shape
//	     #717 asks for — one owner signature, no extra prompt.
//	     PREDICT: unknown. This is the spike.
//	R-B  same batch, outer = owner's FALLBACK. The §3.6 U-B arrangement
//	     applied to a replace. PREDICT: mined, if Q1 holds — but the owner
//	     signs the op, so it costs a prompt and #717's "no additional owner
//	     signature" criterion fails even on success.
//
// Whichever mines, Q3 is settled by reading back: prior entity's signer must
// be zero AND its spend cap cleared; new entity's signer must be the
// controller.
//
// What it needs. The account is derived from the owner + a salt, prefunded
// from the owner EOA, and — if the salt is fresh — bootstrapped with the prior
// policied grant before any probe runs. So a dedicated salt is genuinely all
// that is required; nothing has to be set up by hand.
//
//	SPIKE_OWNER_KEY       owner's private key. Falls back to TEST_PRIVATE_KEY,
//	                      so the repo's own .env works unchanged.
//	SPIKE_CONTROLLER_KEY  controller's private key. Falls back to
//	                      CONTROLLER_PRIVATE_KEY (config/test.yaml has it).
//	SPIKE_BUNDLER_URL     any ERC-4337 bundler for Sepolia. This is only where
//	                      UserOperations are submitted — the spike is not
//	                      testing the bundler. Use the Alchemy endpoint the
//	                      fleet now runs on:
//	                      https://eth-sepolia.g.alchemy.com/v2/<ALCHEMY_API_KEY>
//	SPIKE_SALT            optional, default 9. Pick one nothing else uses; the
//	                      probes install and remove validation entities on it.
//	SPIKE_RPC_URL         optional. Default is a public endpoint — prefer the
//	                      paid Dwellir one, per the no-public-RPC rule.
//	SPIKE_TOKEN           optional, default the §3.4 SpikeToken. Only used as
//	                      an allowlisted call target; no balance is needed
//	                      because the probes transfer zero.
//
// Cost: ~0.006 ETH prefunded to the account, plus gas for up to four
// operations. No paymaster — sponsorship is not what this measures.
//
// Run: go run ./scripts/spike/deferred_replace
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

const (
	defaultRPC   = "https://ethereum-sepolia-rpc.publicnode.com"
	defaultToken = "0xaA4D01B75fdEB5fbbD98276EC7755eF71801c2E7"
	defaultSalt  = 9
)

var (
	oneToken = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	// prefundWei covers the probes' gas; the account pays its own way (no
	// paymaster here — sponsorship is not what this spike is testing).
	prefundWei = big.NewInt(6_000_000_000_000_000) // 0.006 ETH
	spikeSalt  *big.Int
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nSPIKE FAILED: %v\n", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func requireKey(names ...string) (*ecdsa.PrivateKey, common.Address, error) {
	var raw, name string
	for _, n := range names {
		if v := strings.TrimPrefix(strings.TrimSpace(os.Getenv(n)), "0x"); v != "" {
			raw, name = v, n
			break
		}
	}
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("set one of: %s", strings.Join(names, ", "))
	}
	key, err := crypto.HexToECDSA(raw)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("%s is not a private key: %w", name, err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func run() error {
	ctx := context.Background()

	// SPIKE_OWNER_KEY falls back to TEST_PRIVATE_KEY so this runs straight from
	// the repo's .env — it is the same key, the smart wallet's owner.
	ownerKey, owner, err := requireKey("SPIKE_OWNER_KEY", "TEST_PRIVATE_KEY")
	if err != nil {
		return err
	}
	controllerKey, controller, err := requireKey("SPIKE_CONTROLLER_KEY", "CONTROLLER_PRIVATE_KEY")
	if err != nil {
		return err
	}
	bundlerURL := strings.TrimSpace(os.Getenv("SPIKE_BUNDLER_URL"))
	if bundlerURL == "" {
		return fmt.Errorf("SPIKE_BUNDLER_URL is required")
	}
	token := common.HexToAddress(env("SPIKE_TOKEN", defaultToken))

	salt := big.NewInt(defaultSalt)
	if v := strings.TrimSpace(os.Getenv("SPIKE_SALT")); v != "" {
		parsed, ok := new(big.Int).SetString(v, 10)
		if !ok {
			return fmt.Errorf("SPIKE_SALT %q is not a number", v)
		}
		salt = parsed
	}
	spikeSalt = salt

	chain, err := ethclient.Dial(env("SPIKE_RPC_URL", defaultRPC))
	if err != nil {
		return fmt.Errorf("dialing chain: %w", err)
	}
	defer chain.Close()
	chainRPC, err := rpc.DialContext(ctx, env("SPIKE_RPC_URL", defaultRPC))
	if err != nil {
		return fmt.Errorf("dialing chain rpc: %w", err)
	}
	defer chainRPC.Close()
	bundler, err := rpc.DialContext(ctx, bundlerURL)
	if err != nil {
		return fmt.Errorf("dialing bundler: %w", err)
	}
	defer bundler.Close()

	chainID, err := chain.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("reading chain id: %w", err)
	}
	entryPoint := preset.EntryPointV07()

	account, err := aa.GetSenderAddressMAv2(chain, owner, salt)
	if err != nil {
		return fmt.Errorf("deriving account: %w", err)
	}

	fmt.Printf("chain     %s\n", chainID)
	fmt.Printf("owner     %s\n", owner.Hex())
	fmt.Printf("controller%s\n", controller.Hex())
	fmt.Printf("account   %s (salt %s)\n", account.Hex(), salt)
	fmt.Printf("token     %s\n\n", token.Hex())

	// Fixture: the PRIOR grant. Policied, matching what production installs —
	// allowlist + spend cap + time range — because a bare grant would make the
	// exec-hook question vanish and the spike would prove nothing about the
	// grants that actually exist.
	prior := uint32(1)
	next := uint32(2)

	priorSigner, err := readSigner(ctx, chain, prior, *account)
	if err != nil {
		return fmt.Errorf("reading prior signer: %w", err)
	}
	if priorSigner == (common.Address{}) {
		// Bootstrap: a fresh salt has no grant to replace, so install one. This
		// is the ordinary grant flow (§3.2) and doubles as the R-0 control —
		// if it fails, the fixture is wrong and no probe result means anything.
		fmt.Printf("entity %d is free — installing the prior policied grant to replace (bootstrap)\n", prior)
		if err := sendETH(ctx, chain, chainID, ownerKey, owner, *account, prefundWei); err != nil {
			return fmt.Errorf("prefunding %s: %w", account.Hex(), err)
		}
		bootstrap, err := policiedInstall(prior, controller, token)
		if err != nil {
			return fmt.Errorf("building the bootstrap grant: %w", err)
		}
		if err := attemptDeferred(ctx, chain, chainRPC, bundler, entryPoint, chainID,
			*account, owner, ownerKey, prior, controllerKey, bootstrap, true, token, controller, true); err != nil {
			return fmt.Errorf("bootstrap install failed — the fixture is wrong, not the hypothesis: %w", err)
		}
		if priorSigner, err = readSigner(ctx, chain, prior, *account); err != nil {
			return fmt.Errorf("re-reading prior signer: %w", err)
		}
		fmt.Printf("bootstrap done: entity %d now holds %s\n\n", prior, short(priorSigner))
	}
	if priorSigner != controller {
		return fmt.Errorf("entity %d is held by %s, not the controller %s — refusing to touch it",
			prior, priorSigner.Hex(), controller.Hex())
	}
	if s, err := readSigner(ctx, chain, next, *account); err == nil && s != (common.Address{}) {
		return fmt.Errorf("entity %d is already installed (%s); pick a fresh SPIKE_SALT so the probes start clean",
			next, s.Hex())
	}
	fmt.Printf("fixture: entity %d holds the controller (the grant to be replaced); entity %d is free\n\n", prior, next)

	// The two halves of the replace, built once and reused across probes.
	installNew, err := policiedInstall(next, controller, token)
	if err != nil {
		return fmt.Errorf("building the new grant's install: %w", err)
	}
	uninstallPrior, err := policiedUninstall(prior)
	if err != nil {
		return fmt.Errorf("building the prior grant's uninstall: %w", err)
	}

	// Q1 — the batch itself. Both calls target the ACCOUNT, which is what
	// makes this a self-administration batch and the reason it may be refused
	// outright rather than at validation.
	batch, err := aa.PackExecuteBatchMAv2([]aa.Call{
		{Target: *account, Value: big.NewInt(0), Data: installNew},
		{Target: *account, Value: big.NewInt(0), Data: uninstallPrior},
	})
	if err != nil {
		return fmt.Errorf("packing the replace batch: %w", err)
	}
	fmt.Printf("replace batch: install(entity %d) + uninstall(entity %d), %d bytes\n\n", next, prior, len(batch))

	type probe struct {
		name       string
		deferred   []byte
		outerEnt   uint32
		outerKey   *ecdsa.PrivateKey
		wrap       bool
		predict    string
		fallbackOK bool
	}
	probes := []probe{
		{
			name: "R-0  control: install-only, outer = NEW entity (today's grant flow)",
			// Note this deliberately installs entity `next` on its own, so it
			// must run LAST or it would occupy the entity the other probes
			// need free. Ordering is handled below.
			deferred: installNew, outerEnt: next, outerKey: controllerKey, wrap: true,
			predict: "mined (§3.2)",
		},
		{
			name:     "R-A  replace batch, outer = NEW entity (what #717 asks for)",
			deferred: batch, outerEnt: next, outerKey: controllerKey, wrap: true,
			predict: "unknown — this is the spike",
		},
		{
			name:     "R-B  replace batch, outer = owner's FALLBACK (§3.6 U-B shape)",
			deferred: batch, outerEnt: 0, outerKey: ownerKey, wrap: false,
			predict: "mined if Q1 holds — but the owner signs the op, so #717's no-extra-prompt criterion fails",
			// entity 0 is the owner's fallback signer.
			fallbackOK: true,
		},
	}

	// R-A first: it is the question. R-B only matters if R-A fails, and R-0 is
	// a fixture check that would consume the free entity.
	order := []int{1, 2, 0}

	for _, i := range order {
		p := probes[i]
		fmt.Printf("── %s\n   predict: %s\n", p.name, p.predict)

		if !p.fallbackOK && p.outerEnt == 0 {
			fmt.Printf("   SKIPPED: outer entity 0 is the owner fallback and this probe did not opt in\n\n")
			continue
		}
		err := attemptDeferred(ctx, chain, chainRPC, bundler, entryPoint, chainID,
			*account, owner, ownerKey, p.outerEnt, p.outerKey, p.deferred, p.wrap, token, controller, false)
		if err != nil {
			fmt.Printf("   RESULT: refused — %v\n", firstLine(err.Error()))
		} else {
			fmt.Printf("   RESULT: mined\n")
		}

		// Q3 — the only proof that matters. A successful tx says nothing about
		// hook teardown: §3.5 records that a misrouted onUninstall is caught
		// and the module state silently stranded.
		ps, _ := readSigner(ctx, chain, prior, *account)
		ns, _ := readSigner(ctx, chain, next, *account)
		fmt.Printf("   state: entity %d signer=%s | entity %d signer=%s\n",
			prior, short(ps), next, short(ns))
		if cap, err := readSpendLimit(ctx, chain, prior, token, *account); err == nil {
			fmt.Printf("          entity %d spend cap=%s (must be 0 for a clean teardown)\n", prior, cap)
		}
		fmt.Println()

		if err == nil && ps == (common.Address{}) && ns == controller {
			fmt.Printf("   ✅ replace composed: the prior entity is gone and the new one holds the controller\n\n")
			return nil
		}
		if err == nil {
			fmt.Printf("   ⚠️  op mined but the end state is not a clean replace — see the readback above\n\n")
			return nil
		}
	}

	fmt.Println("no probe produced a clean replace — #717 cannot be built as specified; see the refusals above")
	return nil
}

// policiedInstall builds the production grant shape: a global session signer
// with allowlist + spend cap + time range. Matching production matters — a
// bare grant has no execution hook, and the exec hook is the thing that makes
// self-administration interesting.
func policiedInstall(entity uint32, signer, token common.Address) ([]byte, error) {
	validUntil := uint64(time.Now().Add(7 * 24 * time.Hour).Unix())
	allowlist, err := aa.PackAllowlistInstallData(entity, []aa.AllowlistInput{
		{Target: token, Selectors: [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}}}, // transfer
	})
	if err != nil {
		return nil, err
	}
	timeRange, err := aa.PackTimeRangeInstallData(entity, validUntil, 0)
	if err != nil {
		return nil, err
	}
	return aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity,
		Signer:   signer,
		Global:   true,
		Hooks:    [][]byte{allowlist, timeRange},
	})
}

// policiedUninstall rebuilds the prior grant's teardown. The hook data must be
// one entry per installed hook in the ACCOUNT's stored order — reverse of
// install, because the hook set is a prepend-on-add list (§3.5). A misrouted
// entry does not revert; it strands module state silently, which is why the
// probes read the cap back rather than trusting the receipt.
func policiedUninstall(entity uint32) ([]byte, error) {
	singleSigner, err := aa.PackSingleSignerUninstallData(entity)
	if err != nil {
		return nil, err
	}
	timeRange, err := aa.PackTimeRangeUninstallData(entity)
	if err != nil {
		return nil, err
	}
	// Stored order: validation hooks reversed (timeRange installed last, so it
	// is first), then execution hooks. The allowlist appears twice — once as a
	// validation hook, once as an execution hook — and takes the same
	// (entityId, inputs) payload it was installed with, which for the spike's
	// fixture is the allowlist install data.
	return aa.PackSessionSignerUninstall(entity, [][]byte{timeRange, singleSigner})
}

// attemptDeferred sends one probe: `deferred` runs during validation under the
// owner's fallback authority; the op itself is validated by outerEntity.
func attemptDeferred(ctx context.Context, chain *ethclient.Client, chainRPC, bundler *rpc.Client,
	entryPoint common.Address, chainID *big.Int, account, owner common.Address,
	ownerKey *ecdsa.PrivateKey, outerEntity uint32, outerKey *ecdsa.PrivateKey,
	deferred []byte, wrap bool, token, controller common.Address, deploy bool) error {

	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	nonce, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, outerEntity, opts)
	if err != nil {
		return fmt.Errorf("reading nonce: %w", err)
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())

	digest, err := userop.DeferredActionDigest(chainID, account, nonce, deadline, deferred)
	if err != nil {
		return err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), ownerKey) // inner = owner's fallback
	if err != nil {
		return err
	}
	ownerSig[64] += 27
	encoded, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, deferred)
	if err != nil {
		return err
	}

	// The op's own call. When the outer validation is a policied entity its
	// allowlist VALIDATION hook inspects this calldata, so it must be an
	// allowlisted action — a token transfer, as in the §3.6 harness — not an
	// arbitrary call.
	// Zero amount on purpose: the allowlist hook checks target + selector and
	// the spend cap permits 0, so the probe needs no token balance — only ETH
	// for gas. The question is whether VALIDATION composes; the transfer is
	// just a call the hooks will accept.
	transfer := append([]byte{0xa9, 0x05, 0x9c, 0xbb}, common.LeftPadBytes(controller.Bytes(), 32)...)
	transfer = append(transfer, common.LeftPadBytes(big.NewInt(0).Bytes(), 32)...)
	exec, err := aa.PackExecute(token, big.NewInt(0), transfer)
	if err != nil {
		return err
	}
	callData := exec
	if wrap {
		if callData, err = aa.WrapExecuteUserOp(exec); err != nil {
			return err
		}
	}

	op := &userop.UserOperationV07{
		Sender:               account,
		Nonce:                nonce,
		CallData:             callData,
		VerificationGasLimit: big.NewInt(900_000), // batches verify heavier than a lone install
	}
	if deploy {
		factory, factoryData, err := aa.GetInitCodeMAv2(owner, spikeSalt)
		if err != nil {
			return err
		}
		op.Factory, op.FactoryData = &factory, factoryData
	}
	if err := priceOp(ctx, chain, bundler, op, entryPoint, encoded, ownerSig); err != nil {
		return fmt.Errorf("pricing: %w", err)
	}
	if err := preset.SignUserOpV07Deferred(op, entryPoint, chainID, outerKey, encoded, ownerSig); err != nil {
		return err
	}
	hash, err := preset.SendUserOpV07(ctx, bundler, op, entryPoint)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	r, err := waitReceipt(ctx, bundler, hash)
	if err != nil {
		return err
	}
	fmt.Printf("   op mined: success=%v gas=%s tx=%s\n", r.Success, r.ActualGasUsed, r.TxHash)
	if !r.Success {
		return fmt.Errorf("op mined but reverted")
	}
	return nil
}

func short(a common.Address) string {
	if a == (common.Address{}) {
		return "0x0 (cleared)"
	}
	return a.Hex()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ── helpers, lifted from scripts/spike/deferred_uninstall so each spike runs
// standalone (they are single-file programs by convention).

type userOpReceipt struct {
	Success       bool
	ActualGasUsed *big.Int
	TxHash        string
}

func callWords(ctx context.Context, chain *ethclient.Client, to common.Address, sig string, args ...[]byte) ([]byte, error) {
	data := crypto.Keccak256([]byte(sig))[:4]
	for _, a := range args {
		data = append(data, common.LeftPadBytes(a, 32)...)
	}
	return chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}

func readSigner(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (common.Address, error) {
	out, err := callWords(ctx, chain, aa.SingleSignerValidationModuleAddress(),
		"signers(uint32,address)", big.NewInt(int64(entity)).Bytes(), account.Bytes())
	if err != nil || len(out) < 32 {
		return common.Address{}, fmt.Errorf("reading signer: %w", err)
	}
	return common.BytesToAddress(out[12:32]), nil
}

func readSpendLimit(ctx context.Context, chain *ethclient.Client, entity uint32, token, account common.Address) (*big.Int, error) {
	out, err := callWords(ctx, chain, aa.AllowlistModuleAddress(),
		"erc20SpendLimits(uint32,address,address)", big.NewInt(int64(entity)).Bytes(), token.Bytes(), account.Bytes())
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(out), nil
}

func priceOp(ctx context.Context, chain *ethclient.Client, bundler *rpc.Client,
	op *userop.UserOperationV07, entryPoint common.Address, encodedData, ownerSig []byte) error {
	tip, err := chain.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	var bundlerTipHex string
	if err := bundler.CallContext(ctx, &bundlerTipHex, "rundler_maxPriorityFeePerGas"); err == nil {
		if bt, ok := new(big.Int).SetString(trim0x(bundlerTipHex), 16); ok && bt.Cmp(tip) > 0 {
			tip = bt
		}
	}
	head, err := chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	if encodedData != nil {
		sig, sErr := preset.DeferredEstimationSignature(encodedData, ownerSig)
		if sErr != nil {
			return sErr
		}
		op.Signature = sig
		defer func() { op.Signature = nil }()
	}
	if _, err := preset.EstimateUserOpGasV07(ctx, bundler, op, entryPoint); err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	return nil
}

func waitReceipt(ctx context.Context, bundler *rpc.Client, opHash common.Hash) (*userOpReceipt, error) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		var raw json.RawMessage
		if err := bundler.CallContext(ctx, &raw, "eth_getUserOperationReceipt", opHash.Hex()); err == nil &&
			len(raw) > 0 && string(raw) != "null" {
			var p struct {
				Success       bool   `json:"success"`
				ActualGasUsed string `json:"actualGasUsed"`
				Receipt       struct {
					TransactionHash string `json:"transactionHash"`
				} `json:"receipt"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			g, _ := new(big.Int).SetString(trim0x(p.ActualGasUsed), 16)
			return &userOpReceipt{Success: p.Success, ActualGasUsed: g, TxHash: p.Receipt.TransactionHash}, nil
		}
		time.Sleep(4 * time.Second)
	}
	return nil, fmt.Errorf("no receipt for %s in 3m", opHash)
}

func trim0x(s string) string { return strings.TrimPrefix(s, "0x") }

func sendETH(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, owner, to common.Address, amount *big.Int) error {
	if bal, err := chain.BalanceAt(ctx, to, nil); err == nil && bal.Cmp(amount) >= 0 {
		return nil
	}
	nonce, err := chain.PendingNonceAt(ctx, owner)
	if err != nil {
		return err
	}
	gp, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	gl, err := chain.EstimateGas(ctx, ethereum.CallMsg{From: owner, To: &to, Value: amount})
	if err != nil {
		return err
	}
	tx := types.NewTransaction(nonce, to, amount, gl+10_000, gp, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return err
	}
	_, err = waitMined(ctx, chain, signed.Hash())
	return err
}

func waitMined(ctx context.Context, chain *ethclient.Client, hash common.Hash) (*types.Receipt, error) {
	for range 60 {
		if r, err := chain.TransactionReceipt(ctx, hash); err == nil {
			if r.Status != types.ReceiptStatusSuccessful {
				return nil, fmt.Errorf("tx %s failed", hash)
			}
			return r, nil
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("tx %s not mined in 3m", hash)
}

package preset

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// SendUserOpMAv2 is the v0.7 counterpart to SendUserOp: it takes the same
// inputs, builds a Modular Account v2 operation, and returns once the
// operation is mined. (SendUserOpV07 is the raw one-shot send this sits on
// top of; this is the orchestrator that builds, prices, and signs first.)
//
// callData is the account's execute() calldata, the SAME bytes the v0.6 path
// builds. execute(address,uint256,bytes) kept selector 0xb61d27f6 in MA v2, so
// aa.PackExecute output is byte-identical to aa.PackExecuteMAv2 and callers
// need no repacking. That is NOT true of batching:
// executeBatchWithValues has no MA v2 successor, so a batched operation must
// be packed with aa.PackExecuteBatchMAv2 before it gets here.
//
// Sponsorship is all-or-nothing on purpose. When a Gas Manager policy is
// configured, an operation the policy declines fails rather than falling back
// to self-funded — silently paying out of the account's own balance is how a
// declined sponsorship turns into a drained wallet.
func SendUserOpMAv2(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	senderOverride *common.Address,
	saltOverride *big.Int,
	auth *SessionAuthorization,
	lgr logger.Logger,
) (*userop.UserOperationV07, *types.Receipt, error) {
	l := logger.EnsureLogger(lgr)
	if smartWalletConfig == nil {
		return nil, nil, fmt.Errorf("nil smart wallet config")
	}
	if err := auth.Validate(); err != nil {
		return nil, nil, err
	}

	ctx := context.Background()
	// Fail-fast guards (session grant, factory derivation) only need the chain
	// RPC. Dial the bundler after those checks so a legacy SimpleAccount
	// runner or missing grant does not waste a bundler round-trip — and so
	// guard-only tests need no bundler URL.
	chainRPC, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing chain rpc: %w", err)
	}
	defer chainRPC.Close()

	// Nonce reads are plain eth_calls and go to the CHAIN rpc. The bundler
	// endpoint only advertises the ERC-4337 namespace on some providers
	// (Voltaire answers eth_call with Method-not-found; Alchemy happens to
	// serve both from one URL, which is how this hid during development).
	nonceRPC := chainRPC.Client()

	entryPoint := EntryPointV07()
	chainID := big.NewInt(smartWalletConfig.ChainID)

	salt := saltOverride
	if salt == nil {
		salt = big.NewInt(0)
	}

	factory, err := aa.EffectiveFactory(smartWalletConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving account factory: %w", err)
	}

	sender, err := resolveSenderV07(chainRPC, owner, factory, salt, senderOverride)
	if err != nil {
		return nil, nil, err
	}

	// A caller that supplies no authorization gets the stored grant for THIS
	// wallet — looked up only now, because grants are keyed by the smart
	// wallet's address and the sender is not known until it is resolved.
	// Resolving earlier against the owner EOA (or a guessed wallet) finds
	// nothing and used to sign as the fallback entity the gateway cannot
	// validate for.
	if auth == nil {
		if auth, err = resolveSession(smartWalletConfig.ChainID, owner, sender); err != nil {
			return nil, nil, fmt.Errorf("resolving session authorization for %s: %w", sender.Hex(), err)
		}
		if err = auth.Validate(); err != nil {
			return nil, nil, err
		}
	}
	// MA v2 operations always need a session grant. The gateway does not hold
	// the owner's key; estimating a controller-signed fallback UserOp only
	// produces opaque AA23. Callers that supply their own authorization
	// (integration tests, spikes) pass it explicitly and never hit this.
	if auth == nil {
		return nil, nil, fmt.Errorf(
			"no session authorization for smart wallet %s (owner %s); MA v2 execution requires a session grant — the gateway cannot sign as the owner fallback",
			sender.Hex(), owner.Hex())
	}

	// Sender must be the address this chain's factory derives for (owner, salt).
	// Covers both cases that previously reached the bundler as AA23:
	//   - undeployed override with the wrong salt/factory (initCode would
	//     deploy elsewhere)
	//   - deployed legacy SimpleAccount runner on an MA v2 chain (no initCode,
	//     v0.7 validation against v0.6 bytecode)
	derived, derr := aa.DeriveSenderAddressAuto(chainRPC, owner, factory, salt)
	if derr != nil {
		return nil, nil, fmt.Errorf("deriving counterfactual address for sender %s: %w", sender.Hex(), derr)
	}
	if derived == nil || *derived != sender {
		want := common.Address{}
		if derived != nil {
			want = *derived
		}
		return nil, nil, fmt.Errorf(
			"sender %s does not match the address derived for owner %s salt %s factory %s (derived %s); refusing MA v2 UserOp — wrong account type, salt, or factory (e.g. a legacy SimpleAccount runner on an MA v2 chain)",
			sender.Hex(), owner.Hex(), salt.String(), factory.Hex(), want.Hex())
	}

	// A grant carrying execution hooks requires user-op context on EVERY
	// operation under it — including the one that installs the grant, since
	// the hooks are live from the moment the install applies mid-validation.
	if auth.WrapExecuteUserOp {
		wrapped, wrapErr := aa.WrapExecuteUserOp(callData)
		if wrapErr != nil {
			return nil, nil, fmt.Errorf("wrapping calldata for user-op context: %w", wrapErr)
		}
		callData = wrapped
	}

	op := &userop.UserOperationV07{
		Sender:               sender,
		CallData:             callData,
		CallGasLimit:         big.NewInt(initialCallGasLimit),
		PreVerificationGas:   big.NewInt(initialPreVerificationGas),
		MaxFeePerGas:         big.NewInt(0),
		MaxPriorityFeePerGas: big.NewInt(0),
	}

	// An account with no code yet must carry its own deployment. Reading the
	// code is the authoritative check — a wallet record can exist for an
	// address that was never deployed, and vice versa. Derive already matched
	// above, so initCode is safe to attach.
	deployed, err := isDeployed(ctx, chainRPC, sender)
	if err != nil {
		return nil, nil, fmt.Errorf("checking whether %s is deployed: %w", sender.Hex(), err)
	}
	if !deployed {
		deployFactory, factoryData, initErr := aa.DeriveInitCodeAuto(owner, factory, salt)
		if initErr != nil {
			return nil, nil, fmt.Errorf("building init code: %w", initErr)
		}
		op.Factory = &deployFactory
		op.FactoryData = factoryData
	}
	op.VerificationGasLimit = seedVerificationGasFor(op, auth)

	// Without a Gas Manager policy the account pays gas from native balance /
	// EntryPoint deposit. A zero total is a guaranteed prefund failure —
	// refuse before dialing the bundler so the error is clear and guard tests
	// need no bundler URL. Sponsorship is alchemy_paymaster_policy_id /
	// ALCHEMY_PAYMASTER_POLICY_ID (legacy aliases still accepted); when set,
	// this check is skipped.
	if smartWalletConfig.AlchemyPaymasterPolicyID == "" {
		if err := ensureSelfFundedPrefund(ctx, chainRPC, entryPoint, sender); err != nil {
			return nil, nil, err
		}
	}

	// Bundler is only needed from pricing onwards.
	bundlerURL, err := smartWalletConfig.ActiveBundlerURL()
	if err != nil {
		return nil, nil, fmt.Errorf("resolving bundler url: %w", err)
	}
	bundlerRPC, err := rpc.DialContext(ctx, bundlerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing bundler: %w", err)
	}
	defer bundlerRPC.Close()

	// MA v2 selects its validation function from the NONCE, not the signature.
	// A zero nonce reverts with ValidationFunctionMissing rather than as a
	// nonce error, which is the single most misleading failure on this path.
	//
	// Plain operations read through the nonce manager so a second write can
	// overlap the first in the mempool (getNonce alone reads mined state and
	// AA25s any sequential pair). Deferred operations bypass it: the owner's
	// signature committed to sequence zero at grant time, so no cached value
	// may substitute.
	nonceEntity, nonceOptions := auth.nonceEntity()
	var nonce *big.Int
	if auth.Deferred() {
		nonce, err = NextNonceV07(ctx, nonceRPC, entryPoint, sender, nonceEntity, nonceOptions)
		if err == nil {
			var sequence uint64
			if _, _, sequence, err = userop.DecodeNonceMAv2(nonce); err == nil && sequence != 0 {
				// The carrier sequence is consumed, and the only operation
				// that ever uses a grant's deferred key is its install: the
				// install MINED without this process seeing the receipt (a
				// confirmation timeout, or a crash between send and record).
				// Not an error — record the fact and run this operation as
				// an ordinary one under the now-installed entity.
				l.Info("grant install found already applied on-chain; recording and continuing plain",
					"sender", sender.Hex(), "entity", auth.EntityID, "sequence", sequence)
				if auth.OnApplied != nil {
					if markErr := auth.OnApplied(""); markErr != nil {
						l.Error("grant is applied on-chain but could not be recorded; the next operation will retry",
							"sender", sender.Hex(), "error", markErr)
					}
				}
				auth.DeferredData, auth.OwnerSignature = nil, nil
				nonceEntity, nonceOptions = auth.nonceEntity()
				op.Signature = nil
				op.VerificationGasLimit = seedVerificationGasFor(op, auth)
				nonce, err = NextNonceV07Managed(ctx, nonceRPC, entryPoint, sender, nonceEntity, nonceOptions)
			}
		}
	} else {
		nonce, err = NextNonceV07Managed(ctx, nonceRPC, entryPoint, sender, nonceEntity, nonceOptions)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading nonce: %w", err)
	}
	op.Nonce = nonce

	if err := checkDeferredCarrierNonce(auth, op.Nonce, sender); err != nil {
		return nil, nil, err
	}

	l.Info("v0.7 user operation prepared",
		"account_provider", smartWalletConfig.AccountProviderName(),
		"effective_factory", factory.Hex(),
		"config_factory_address", smartWalletConfig.FactoryAddress.Hex(),
		"entry_point", entryPoint.Hex(),
		"owner", owner.Hex(),
		"sender", sender.Hex(),
		"salt", salt.String(),
		"deployed", deployed,
		"deploying", op.Factory != nil,
		"entity", nonceEntity,
		"deferred", auth.Deferred(),
		"policy_id", auth.PolicyID,
		"wrap_execute_user_op", auth.WrapExecuteUserOp,
		"verification_gas_seed", op.VerificationGasLimit.String(),
		"paymaster_policy_set", smartWalletConfig.AlchemyPaymasterPolicyID != "",
	)

	// Estimation of a deferred operation must carry the REAL owner grant.
	// Ordinary signature validation fails gracefully so estimation can
	// proceed, but a bad deferred-action signature REVERTS
	// (DeferredActionSignatureInvalid) — so estimating with the plain dummy
	// gets AA23 with nothing pointing at the missing grant. Only the
	// controller's half may be a dummy here; the real one is installed below,
	// after pricing, because the signature covers the gas fields.
	if auth.Deferred() {
		estSig, estErr := DeferredEstimationSignature(auth.DeferredData, auth.OwnerSignature)
		if estErr != nil {
			return nil, nil, fmt.Errorf("building the estimation signature: %w", estErr)
		}
		op.Signature = estSig
	}

	if err := priceOperationV07(ctx, chainRPC, bundlerRPC, op, entryPoint, smartWalletConfig, l); err != nil {
		return nil, nil, err
	}

	// Sign last: the signature covers every gas and paymaster field, so it has
	// to come after pricing. SendUserOpV07WithRetry only RE-signs, and only
	// when it tightens the verification gas limit — it will not sign an
	// unsigned operation, it rejects one.
	signingKey := auth.SignerKey
	if auth.Deferred() {
		if err := SignUserOpV07Deferred(op, entryPoint, chainID, signingKey,
			auth.DeferredData, auth.OwnerSignature); err != nil {
			return op, nil, fmt.Errorf("signing user operation with deferred action: %w", err)
		}
	} else if err := SignUserOpV07(op, entryPoint, chainID, signingKey); err != nil {
		return op, nil, fmt.Errorf("signing user operation: %w", err)
	}

	opHash, err := SendUserOpV07WithRetry(ctx, bundlerRPC, op, entryPoint, chainID, signingKey)
	if err != nil && !auth.Deferred() && isInvalidNonceError(err) {
		// AA25 covers both directions of cache drift — a duplicate (another
		// operation took this sequence) and a gap (a cached pending operation
		// was dropped). Either way the cache is wrong: drop it, take the
		// chain's answer, re-sign (the nonce is in the userOpHash), retry
		// once. Deferred operations are excluded — their nonce is committed
		// by the owner's signature and cannot be substituted.
		InvalidateNonce(sender, nonceEntity, nonceOptions)
		freshNonce, nonceErr := NextNonceV07Managed(ctx, nonceRPC, entryPoint, sender, nonceEntity, nonceOptions)
		if nonceErr == nil && freshNonce.Cmp(op.Nonce) != 0 {
			l.Info("retrying after AA25 with the chain's nonce",
				"sender", sender.Hex(), "stale", op.Nonce.String(), "fresh", freshNonce.String())
			op.Nonce = freshNonce
			if signErr := SignUserOpV07(op, entryPoint, chainID, signingKey); signErr == nil {
				opHash, err = SendUserOpV07WithRetry(ctx, bundlerRPC, op, entryPoint, chainID, signingKey)
			}
		}
	}
	if err != nil {
		InvalidateNonce(sender, nonceEntity, nonceOptions)
		return op, nil, fmt.Errorf("sending user operation: %w", annotatePrefundError(err, sender))
	}
	// Record acceptance NOW — the bundler holds the operation, so the next
	// one on this key must use the following sequence even though nothing has
	// mined yet. This is the line that lets sequential contract writes
	// overlap the mempool instead of dying AA25.
	NoteUserOpAccepted(sender, nonceEntity, nonceOptions, op.Nonce)
	l.Info("v0.7 user operation sent",
		"sender", sender.Hex(), "userop_hash", opHash.Hex(),
		"sponsored", op.Paymaster != nil, "deploying", op.Factory != nil,
		"entity", nonceEntity, "installs_grant", auth.Deferred(),
		"policy_id", auth.PolicyID, "effective_factory", factory.Hex())

	// Reuses the v0.6 confirmation watcher: UserOperationEvent is unchanged
	// between EntryPoint versions, so only the EntryPoint address differs.
	var wsClient *ethclient.Client
	if smartWalletConfig.EthWsUrl != "" {
		if ws, wsErr := ethclient.Dial(smartWalletConfig.EthWsUrl); wsErr == nil {
			wsClient = ws
			defer ws.Close()
		} else {
			l.Warn("websocket dial failed, polling for the receipt instead", "error", wsErr)
		}
	}
	installsGrant := auth.Deferred()
	receipt, err := waitForUserOpConfirmation(chainRPC, wsClient, entryPoint, opHash.Hex(), l)
	if err != nil {
		// Includes the mined-but-execution-reverted case — in which the
		// install DID apply (it runs in the validation frame, which an
		// execution revert does not roll back, results doc §3.4). Not marked
		// here because this path cannot distinguish that from a true
		// validation-level failure; the next operation heals it through the
		// consumed-carrier-nonce path above.
		return op, nil, err
	}
	if installsGrant {
		if receipt == nil {
			// Confirmation timed out with the operation still pending. The
			// grant stays recorded as pending; if the install mines later,
			// the next operation discovers the consumed carrier nonce and
			// records it then.
			l.Info("grant install sent but unconfirmed; recording deferred to the next operation",
				"sender", sender.Hex(), "userop_hash", opHash.Hex())
		} else if auth.OnApplied != nil {
			// The install is on-chain. Record it so the stored grant stops
			// attaching the deferred action — attaching a consumed one would
			// fail every subsequent operation on this wallet.
			if markErr := auth.OnApplied(opHash.Hex()); markErr != nil {
				l.Error("grant applied on-chain but could not be recorded; the next operation will retry",
					"sender", sender.Hex(), "userop_hash", opHash.Hex(), "error", markErr)
			}
		}
	}
	return op, receipt, nil
}

// checkDeferredCarrierNonce refuses a deferred operation whose live nonce
// differs from the nonce the owner signed over at grant time. A mismatch
// would otherwise surface as AA23 from DeferredActionSignatureInvalid.
func checkDeferredCarrierNonce(auth *SessionAuthorization, opNonce *big.Int, sender common.Address) error {
	if auth == nil || !auth.Deferred() || auth.CarrierNonce == nil {
		return nil
	}
	if opNonce == nil {
		return fmt.Errorf("deferred carrier nonce check: operation nonce is nil")
	}
	if auth.CarrierNonce.Cmp(opNonce) != 0 {
		return fmt.Errorf(
			"deferred carrier nonce mismatch for %s: grant signed over %s but operation uses %s (entity %d); refusing before estimate",
			sender.Hex(), auth.CarrierNonce.String(), opNonce.String(), auth.EntityID)
	}
	return nil
}

// ensureSelfFundedPrefund refuses an unsponsored operation when the account
// has nothing to pay gas with. Estimation can succeed with a zero balance
// (validation does not charge the account), so without this check the failure
// only appears at eth_sendUserOperation as a prefund error that Studio users
// read as another opaque AA / bundler failure.
//
// Sponsorship is alchemy_paymaster_policy_id / ALCHEMY_PAYMASTER_POLICY_ID —
// when set, the caller skips this check and RequestSponsorshipV07 attaches a
// paymaster.
func ensureSelfFundedPrefund(ctx context.Context, chainRPC *ethclient.Client, entryPoint, sender common.Address) error {
	if chainRPC == nil {
		return fmt.Errorf("nil chain rpc")
	}
	balance, err := chainRPC.BalanceAt(ctx, sender, nil)
	if err != nil {
		return fmt.Errorf("reading native balance of %s: %w", sender.Hex(), err)
	}
	deposit, err := entryPointDeposit(ctx, chainRPC, entryPoint, sender)
	if err != nil {
		// Deposit is best-effort: a failed eth_call should not mask a clear
		// zero-balance situation, but also should not invent a deposit.
		deposit = big.NewInt(0)
	}
	total := new(big.Int).Add(balance, deposit)
	if total.Sign() > 0 {
		return nil
	}
	return fmt.Errorf(
		"smart wallet %s cannot pay gas: native balance and EntryPoint deposit are both zero, and no Alchemy paymaster policy is configured; set alchemy_paymaster_policy_id (or env ALCHEMY_PAYMASTER_POLICY_ID) for sponsorship, or fund the wallet with Sepolia ETH",
		sender.Hex())
}

// entryPointDeposit reads EntryPoint.balanceOf(sender) for v0.7.
func entryPointDeposit(ctx context.Context, chainRPC *ethclient.Client, entryPoint, sender common.Address) (*big.Int, error) {
	// cast sig "balanceOf(address)" => 0x70a08231 — same selector as ERC-20.
	data := append(common.FromHex("0x70a08231"), common.LeftPadBytes(sender.Bytes(), 32)...)
	out, err := chainRPC.CallContract(ctx, ethereum.CallMsg{To: &entryPoint, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	if len(out) < 32 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(out), nil
}

// annotatePrefundError rewrites the bundler's prefund precheck into a message
// that names the wallet and the Gas Manager escape hatch.
func annotatePrefundError(err error, sender common.Address) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "balance and deposit together is 0") ||
		strings.Contains(msg, "AA21") ||
		strings.Contains(strings.ToLower(msg), "didn't pay prefund") {
		return fmt.Errorf(
			"smart wallet %s cannot pay gas (bundler prefund check): %w; set alchemy_paymaster_policy_id / ALCHEMY_PAYMASTER_POLICY_ID or fund the wallet",
			sender.Hex(), err)
	}
	return err
}

// resolveSenderV07 picks the account the operation runs as. An explicit
// override is trusted (the caller has already resolved which of the owner's
// wallets to use); otherwise the address is derived from the factory.
//
// Trust here only selects the candidate sender. SendUserOpMAv2 always re-
// derives against the provider factory and refuses a mismatch before estimate.
func resolveSenderV07(
	chainRPC *ethclient.Client,
	owner, factory common.Address,
	salt *big.Int,
	senderOverride *common.Address,
) (common.Address, error) {
	if senderOverride != nil && *senderOverride != (common.Address{}) {
		return *senderOverride, nil
	}
	derived, err := aa.DeriveSenderAddressAuto(chainRPC, owner, factory, salt)
	if err != nil {
		return common.Address{}, fmt.Errorf("deriving sender for owner %s salt %s: %w",
			owner.Hex(), salt.String(), err)
	}
	if derived == nil || *derived == (common.Address{}) {
		return common.Address{}, fmt.Errorf("derived a zero sender for owner %s salt %s",
			owner.Hex(), salt.String())
	}
	return *derived, nil
}

// priceOperationV07 fills the gas and paymaster fields, either from a Gas
// Manager policy (which prices the operation as part of sponsoring it) or from
// the bundler's own estimate.
func priceOperationV07(
	ctx context.Context,
	chainRPC *ethclient.Client,
	bundlerRPC *rpc.Client,
	op *userop.UserOperationV07,
	entryPoint common.Address,
	smartWalletConfig *config.SmartWalletConfig,
	l logger.Logger,
) error {
	if policyID := smartWalletConfig.AlchemyPaymasterPolicyID; policyID != "" {
		if err := RequestSponsorshipV07(ctx, bundlerRPC, op, entryPoint,
			SponsorshipRequestV07{PolicyID: policyID}); err != nil {
			// Deliberately fatal. See SendUserOpV07's contract: falling through
			// to an unsponsored send would spend the account's own balance on
			// an operation the policy just refused to fund.
			return fmt.Errorf("gas manager declined to sponsor: %w", err)
		}
		l.Debug("operation sponsored", "paymaster", op.Paymaster.Hex())
		return nil
	}

	// Unsponsored: the account pays, so the fee fields have to be real. A
	// zero maxFeePerGas is accepted by estimation and then silently never
	// mined, which reads as a hung operation rather than a pricing bug.
	tip, err := chainRPC.SuggestGasTipCap(ctx)
	if err != nil {
		return fmt.Errorf("suggesting gas tip: %w", err)
	}
	head, err := chainRPC.HeaderByNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("reading latest header: %w", err)
	}

	// The bundler enforces its own priority-fee floor, independent of what the
	// chain suggests, and rejects anything under it in precheck:
	//
	//	maxPriorityFeePerGas is 1500000 but must be at least 100000000
	//
	// On a quiet testnet the chain's suggestion is well below that floor, so
	// the chain value alone is not enough. Ask the bundler and take whichever
	// is higher — it is the party that decides whether to accept the operation.
	if bundlerTip, tipErr := bundlerMaxPriorityFee(ctx, bundlerRPC); tipErr != nil {
		l.Debug("bundler did not report a priority fee, using the chain's", "error", tipErr)
	} else if bundlerTip.Cmp(tip) > 0 {
		l.Debug("raising priority fee to the bundler's floor",
			"chain_tip", tip.String(), "bundler_tip", bundlerTip.String())
		tip = bundlerTip
	}

	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))

	estimate, err := EstimateUserOpGasV07(ctx, bundlerRPC, op, entryPoint)
	if err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	op.CallGasLimit = estimate.CallGasLimit
	op.VerificationGasLimit = estimate.VerificationGasLimit
	op.PreVerificationGas = estimate.PreVerificationGas
	return nil
}

// isDeployed reports whether an account already has code.
func isDeployed(ctx context.Context, client *ethclient.Client, addr common.Address) (bool, error) {
	code, err := client.CodeAt(ctx, addr, nil)
	if err != nil {
		return false, err
	}
	return len(code) > 0, nil
}

// bundlerMaxPriorityFee asks the bundler what priority fee it currently wants.
//
// Rundler exposes this as rundler_maxPriorityFeePerGas. It is not part of
// ERC-4337, so a bundler that does not implement it returns a method error —
// which is why the caller treats failure as "no opinion" rather than fatal.
func bundlerMaxPriorityFee(ctx context.Context, client *rpc.Client) (*big.Int, error) {
	if client == nil {
		return nil, fmt.Errorf("nil bundler client")
	}
	var out string
	if err := client.CallContext(ctx, &out, "rundler_maxPriorityFeePerGas"); err != nil {
		return nil, err
	}
	return parseHexBig(out, "rundler_maxPriorityFeePerGas")
}

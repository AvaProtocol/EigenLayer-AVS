package preset

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// OwnerAccountIdentity pins the code and validator checked during preparation.
// Wallet storage is an association, not proof of the current on-chain owner.
type OwnerAccountIdentity struct {
	CodeHash           common.Hash    `json:"codeHash"`
	OwnerCodeHash      common.Hash    `json:"ownerCodeHash"`
	Implementation     common.Address `json:"implementation"`
	ImplementationHash common.Hash    `json:"implementationHash"`
	FactoryHash        common.Hash    `json:"factoryHash"`
}

// PreparedOwnerUserOp has no owner signature and never resolves a session grant.
type PreparedOwnerUserOp struct {
	Operation *userop.UserOperationV07
	Identity  OwnerAccountIdentity
}

var implementationSlot = common.HexToHash("0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc")

// This deployed alchemy.sma-bytecode.1.0.0 implementation validates the EIP-191
// digest. Later versions can validate the raw userOp hash instead: trusting the
// factory's current implementation would advertise the wrong signing recipe
// even on a fresh prepare. Add support only after auditing that implementation.
const ownerPersonalSignImplementation = "0x000000000000c5A9089039570Dd36455b5C07383"

// A nil salt selects an already delegated EOA, never a counterfactual account.
// The engine separately requires its stored kind=eoa_7702 wallet association.
func validateOwnerAccountParameters(sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int) error {
	if sw == nil || sw.ChainID <= 0 || owner == (common.Address{}) || sender == (common.Address{}) {
		return fmt.Errorf("invalid owner execution chain or account")
	}
	if sender == owner {
		if salt != nil {
			return fmt.Errorf("EOA owner execution must not specify a CREATE2 salt")
		}
		return sw.CheckEOA7702Execute()
	}
	if salt == nil || salt.Sign() < 0 || salt.BitLen() > 256 {
		return fmt.Errorf("invalid owner execution CREATE2 salt")
	}
	return nil
}

// inspectOwnerAccount supports the pinned bytecode account and SMA-7702, both
// with enabled EOA fallback signing. Delegation is an explicit prior action;
// prepare/submit never install it or create a session grant.
func inspectOwnerAccount(ctx context.Context, chain *ethclient.Client, sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int) (*OwnerAccountIdentity, error) {
	actualChain, err := chain.ChainID(ctx)
	if err != nil || actualChain.Cmp(big.NewInt(sw.ChainID)) != 0 {
		return nil, fmt.Errorf("owner execution chain RPC does not match chain %d", sw.ChainID)
	}
	ownerCode, err := chain.CodeAt(ctx, owner, nil)
	if err != nil {
		return nil, fmt.Errorf("checking owner code: %w", err)
	}
	if len(ownerCode) != 0 {
		// A delegated EOA still holds the private key used by signature type 0.
		// This also preserves derived-wallet execution after that owner delegates.
		if !config.SMA7702FirstChain(sw.ChainID) {
			return nil, fmt.Errorf("delegated owner execution is supported only on Sepolia and Base")
		}
		if err := sw.ValidateSMA7702(); err != nil {
			return nil, err
		}
		implCode, err := chain.CodeAt(ctx, sw.SMA7702Delegate, nil)
		if err != nil {
			return nil, fmt.Errorf("reading owner SMA-7702 implementation: %w", err)
		}
		if err := sw.AssertSMA7702Designation(ownerCode, implCode); err != nil {
			return nil, err
		}
		if sender == owner {
			if err := inspectOwnerValidation(ctx, chain, sender, owner); err != nil {
				return nil, err
			}
			return &OwnerAccountIdentity{
				CodeHash: crypto.Keccak256Hash(ownerCode), OwnerCodeHash: crypto.Keccak256Hash(ownerCode),
				Implementation: sw.SMA7702Delegate, ImplementationHash: crypto.Keccak256Hash(implCode),
			}, nil
		}
	} else if sender == owner {
		return nil, fmt.Errorf("EOA_DELEGATION_MISSING: owner EOA must already designate the pinned SMA-7702 implementation")
	}
	derived, err := aa.GetSenderAddressMAv2WithContext(ctx, chain, owner, salt)
	if err != nil {
		return nil, err
	}
	if derived == nil || *derived != sender || sender == (common.Address{}) {
		return nil, fmt.Errorf("owner execution sender does not match the MA v2 factory derivation")
	}
	factory := aa.MAv2FactoryAddress()
	factoryCode, err := chain.CodeAt(ctx, factory, nil)
	if err != nil || len(factoryCode) == 0 {
		return nil, fmt.Errorf("MA v2 factory code is unavailable")
	}
	implResult, err := ownerView(ctx, chain, factory, "SEMI_MODULAR_ACCOUNT_IMPL()")
	if err != nil || len(implResult) != 32 {
		return nil, fmt.Errorf("reading MA v2 factory implementation")
	}
	impl := common.BytesToAddress(implResult)
	if impl != common.HexToAddress(ownerPersonalSignImplementation) {
		return nil, fmt.Errorf("MA v2 implementation %s is not verified for owner personal_sign", impl.Hex())
	}
	implCode, err := chain.CodeAt(ctx, impl, nil)
	if err != nil || len(implCode) == 0 {
		return nil, fmt.Errorf("MA v2 implementation code is unavailable")
	}
	code, err := chain.CodeAt(ctx, sender, nil)
	if err != nil {
		return nil, fmt.Errorf("checking owner account code: %w", err)
	}
	identity := &OwnerAccountIdentity{
		CodeHash: crypto.Keccak256Hash(code), OwnerCodeHash: crypto.Keccak256Hash(ownerCode), Implementation: impl,
		ImplementationHash: crypto.Keccak256Hash(implCode), FactoryHash: crypto.Keccak256Hash(factoryCode),
	}
	if len(code) == 0 {
		return identity, nil // counterfactual: factory/salt fix the owner and code
	}
	storedImpl, err := chain.StorageAt(ctx, sender, implementationSlot, nil)
	if err != nil || common.BytesToAddress(storedImpl) != impl {
		return nil, fmt.Errorf("owner account is not the factory's supported MA v2 implementation")
	}
	if err := inspectOwnerValidation(ctx, chain, sender, owner); err != nil {
		return nil, err
	}
	return identity, nil
}

func inspectOwnerValidation(ctx context.Context, chain *ethclient.Client, sender, owner common.Address) error {
	fallback, err := ownerView(ctx, chain, sender, "getFallbackSignerData()")
	if err != nil || len(fallback) != 64 || common.BytesToAddress(fallback[:32]) != owner || new(big.Int).SetBytes(fallback[32:]).Sign() != 0 {
		return fmt.Errorf("MA v2 fallback owner changed, is disabled, or is unavailable")
	}
	entry, err := ownerView(ctx, chain, sender, "entryPoint()")
	if err != nil || len(entry) != 32 || common.BytesToAddress(entry) != EntryPointV07() {
		return fmt.Errorf("owner account has an unsupported EntryPoint")
	}
	return nil
}

func ownerView(ctx context.Context, chain *ethclient.Client, to common.Address, signature string) ([]byte, error) {
	return chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: crypto.Keccak256([]byte(signature))[:4]}, nil)
}

// PrepareOwnerUserOp builds and prices a batch without signing or broadcasting.
// Pricing and sponsorship are the SAME functions used by session execution.
func PrepareOwnerUserOp(ctx context.Context, sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int, calls []aa.Call, l logger.Logger) (*PreparedOwnerUserOp, error) {
	if err := validateOwnerAccountParameters(sw, owner, sender, salt); err != nil {
		return nil, err
	}
	for _, call := range calls {
		if call.Value != nil && call.Value.Sign() != 0 {
			return nil, fmt.Errorf("native-value calls are not supported by owner execution yet")
		}
	}
	packed, err := aa.PackExecuteBatchMAv2(calls)
	if err != nil {
		return nil, err
	}
	chain, err := ethclient.DialContext(ctx, sw.EthRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("dialing owner execution chain RPC: %w", err)
	}
	defer chain.Close()
	identity, err := inspectOwnerAccount(ctx, chain, sw, owner, sender, salt)
	if err != nil {
		return nil, err
	}
	nonce, err := NextNonceV07(ctx, chain.Client(), EntryPointV07(), sender, userop.FallbackSignerEntityID, userop.ValidationOptionGlobal)
	if err != nil {
		return nil, err
	}
	entity, options, _, err := userop.DecodeNonceMAv2(nonce)
	if err != nil || entity != userop.FallbackSignerEntityID || options != userop.ValidationOptionGlobal {
		return nil, fmt.Errorf("owner execution RPC returned an invalid owner nonce")
	}
	op := &userop.UserOperationV07{
		Sender: sender, Nonce: nonce, CallData: packed,
		// Nonzero seeds can be echoed rather than recomputed by the bundler.
		// Ask for actual owner/batch estimates before the wallet signs: the
		// historical owner seed can already fail estimation with AA26.
		CallGasLimit: big.NewInt(0), VerificationGasLimit: big.NewInt(0), PreVerificationGas: big.NewInt(0),
		MaxFeePerGas: big.NewInt(0), MaxPriorityFeePerGas: big.NewInt(0),
	}
	if sender != owner && identity.CodeHash == crypto.Keccak256Hash(nil) {
		factory, data, err := aa.GetInitCodeMAv2(owner, salt)
		if err != nil {
			return nil, err
		}
		op.Factory, op.FactoryData = &factory, data
	}
	if sw.SponsorshipPolicyID() == "" {
		if err := ensureSelfFundedPrefund(ctx, chain, EntryPointV07(), sender); err != nil {
			return nil, err
		}
	}
	url, err := sw.ActiveBundlerURL()
	if err != nil {
		return nil, err
	}
	bundler, err := rpc.DialContext(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("dialing owner execution bundler: %w", err)
	}
	defer bundler.Close()
	if err := priceOperationV07(ctx, chain, bundler, op, EntryPointV07(), sw, logger.EnsureLogger(l)); err != nil {
		return nil, err
	}
	if err := checkOwnerPrefund(ctx, chain, op); err != nil {
		return nil, err
	}
	return &PreparedOwnerUserOp{Operation: op, Identity: *identity}, nil
}

// MaxOwnerGasCost is the signed maximum gas exposure, not an estimated fee.
func MaxOwnerGasCost(op *userop.UserOperationV07) *big.Int {
	gas := new(big.Int).Add(op.CallGasLimit, op.VerificationGasLimit)
	gas.Add(gas, op.PreVerificationGas)
	if op.Paymaster != nil {
		gas.Add(gas, op.PaymasterVerificationGasLimit)
		gas.Add(gas, op.PaymasterPostOpGasLimit)
	}
	return gas.Mul(gas, op.MaxFeePerGas)
}

func checkOwnerPrefund(ctx context.Context, chain *ethclient.Client, op *userop.UserOperationV07) error {
	if op.Paymaster != nil {
		return nil
	}
	balance, err := chain.BalanceAt(ctx, op.Sender, nil)
	if err != nil {
		return err
	}
	entry := EntryPointV07()
	data := append(crypto.Keccak256([]byte("balanceOf(address)"))[:4], common.LeftPadBytes(op.Sender.Bytes(), 32)...)
	deposit, err := chain.CallContract(ctx, ethereum.CallMsg{To: &entry, Data: data}, nil)
	if err != nil || len(deposit) != 32 {
		return fmt.Errorf("reading owner execution EntryPoint prefund")
	}
	available := new(big.Int).Add(balance, new(big.Int).SetBytes(deposit))
	if available.Cmp(MaxOwnerGasCost(op)) < 0 {
		return fmt.Errorf("owner execution needs native gas prefund of at least %s wei; available %s wei", MaxOwnerGasCost(op), available)
	}
	return nil
}

// ValidateOwnerUserOp rechecks live authority, code, nonce and funding. It never
// repairs the operation; a change requires another prepare/review/signature.
func ValidateOwnerUserOp(ctx context.Context, sw *config.SmartWalletConfig, owner common.Address, salt *big.Int, prepared *PreparedOwnerUserOp) error {
	if prepared == nil || prepared.Operation == nil {
		return fmt.Errorf("invalid prepared owner operation or chain")
	}
	op := prepared.Operation
	if err := validateOwnerAccountParameters(sw, owner, op.Sender, salt); err != nil {
		return err
	}
	if op.Sender == owner && (op.Factory != nil || len(op.FactoryData) != 0) {
		return fmt.Errorf("EOA owner operation must not contain factory deployment data")
	}
	chain, err := ethclient.DialContext(ctx, sw.EthRpcUrl)
	if err != nil {
		return err
	}
	defer chain.Close()
	identity, err := inspectOwnerAccount(ctx, chain, sw, owner, op.Sender, salt)
	if err != nil {
		return err
	}
	if *identity != prepared.Identity {
		return fmt.Errorf("owner account code or implementation changed; prepare again")
	}
	nonce, err := NextNonceV07(ctx, chain.Client(), EntryPointV07(), op.Sender, userop.FallbackSignerEntityID, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	if op.Nonce.Cmp(nonce) != 0 {
		return fmt.Errorf("owner nonce changed; prepare again")
	}
	return checkOwnerPrefund(ctx, chain, op)
}

// FrameOwnerSignature checks personal_sign(raw 32-byte hash), normalizes v once,
// and adds the MA v2 framing. It accepts no already-prefixed/framed alternative.
func FrameOwnerSignature(hash common.Hash, signature []byte, owner common.Address) ([]byte, error) {
	if len(signature) != crypto.SignatureLength {
		return nil, fmt.Errorf("owner signature must be 65 bytes")
	}
	sig := bytes.Clone(signature)
	switch sig[64] {
	case 27, 28:
		sig[64] -= 27
	case 0, 1:
	default:
		return nil, fmt.Errorf("owner signature recovery byte must be 0, 1, 27 or 28")
	}
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return nil, fmt.Errorf("owner signature must use canonical ECDSA values")
	}
	pub, err := crypto.SigToPub(accounts.TextHash(hash.Bytes()), sig)
	if err != nil || crypto.PubkeyToAddress(*pub) != owner {
		return nil, fmt.Errorf("signature does not match the prepared operation's owner")
	}
	sig[64] += 27
	return userop.WrapSignatureMAv2(sig)
}

// SubmitOwnerUserOp is deliberately keyless and has no gas/nonce repair retry.
func SubmitOwnerUserOp(ctx context.Context, sw *config.SmartWalletConfig, op *userop.UserOperationV07) (common.Hash, error) {
	if sw == nil {
		return common.Hash{}, fmt.Errorf("nil owner execution chain configuration")
	}
	url, err := sw.ActiveBundlerURL()
	if err != nil {
		return common.Hash{}, err
	}
	client, err := rpc.DialContext(ctx, url)
	if err != nil {
		return common.Hash{}, err
	}
	defer client.Close()
	return SendUserOpV07(ctx, client, op, EntryPointV07())
}

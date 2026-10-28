package taskengine

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// Self-funded NativeTokenLimitModule charges gas and value from one cap.
// Sponsored UserOps charge value only, so a nil fee leaves the cap as the
// ETH the workflow sends. The fee is an assumption for the life of the
// grant, not the live base fee: preflight prices each run at tip+2*baseFee,
// and a weekly schedule has to survive a later spike up to this assumption.
//
// Units match nativePreflightGasUnits*: one install ceiling for the grant's
// first UserOp, then the steady ceiling for every other op. An ethTransfer
// loop counts one UserOp per recipient per run. A deployed contract write
// counts one UserOp per method call. Run-node batches those calls; the
// grant is sized for the deployed path.

const (
	// nativeGrantFeeMainnetWei is 50 gwei. Ordinary mainnet conditions sit
	// under it; a spike past it can still fail run 1's preflight.
	nativeGrantFeeMainnetWei int64 = 50_000_000_000
	// nativeGrantFeeSepoliaWei matches the signed-op floor the preflight
	// already uses as a conservative Sepolia maxFee.
	nativeGrantFeeSepoliaWei int64 = 2_000_000_000
	// nativeGrantFeeBaseWei is 1 gwei, above Base's usual sub-gwei base fee.
	nativeGrantFeeBaseWei int64 = 1_000_000_000
	// nativeGrantFeeBaseSepoliaWei is 0.1 gwei.
	nativeGrantFeeBaseSepoliaWei int64 = 100_000_000
	// nativeGrantFeeDefaultWei is the assumption for a chain without its own row.
	nativeGrantFeeDefaultWei int64 = 2_000_000_000
)

// NativeGrantMaxFee is the wei-per-gas assumption for a self-funded native
// cap on chainID. Callers pass nil instead when the chain sponsors gas.
func NativeGrantMaxFee(chainID int64) *big.Int {
	switch chainID {
	case 1:
		return big.NewInt(nativeGrantFeeMainnetWei)
	case 11155111:
		return big.NewInt(nativeGrantFeeSepoliaWei)
	case 8453:
		return big.NewInt(nativeGrantFeeBaseWei)
	case 84532:
		return big.NewInt(nativeGrantFeeBaseSepoliaWei)
	default:
		return big.NewInt(nativeGrantFeeDefaultWei)
	}
}

// mergeOption adjusts a skill-grant merge. Existing callers omit it and
// keep today's value-only caps.
type mergeOption func(*mergeConfig)

type mergeConfig struct {
	fees           map[int64]*big.Int
	applied        bool
	priorDelegated []model.DelegatedNativeRecipient
	// teardowns are the prior grants the install's first operation tears
	// down, priced into its gas. Nil is a first grant, which deploys.
	teardowns []grantGasShape
}

// WithTeardowns prices the prior grants this grant's install tears down
// (teardownShapes). Pass the same set to every merge and coverage check of
// one prepare or submit, so they compute the same native cap.
func WithTeardowns(shapes []grantGasShape) mergeOption {
	return func(cfg *mergeConfig) {
		if cfg != nil {
			cfg.teardowns = shapes
		}
	}
}

// installShape is the grant's install as priced: its own rows and targets,
// and the teardowns this merge was given.
func (cfg mergeConfig) installShape(shape grantGasShape) grantGasShape {
	shape.teardowns = cfg.teardowns
	return shape
}

// WithSelfFundedGrantFee sizes native gas into the cap for chainID.
// A nil or non-positive fee is sponsored: the cap stays the value sent.
func WithSelfFundedGrantFee(chainID int64, fee *big.Int) mergeOption {
	return func(cfg *mergeConfig) {
		if cfg == nil || chainID == 0 || fee == nil || fee.Sign() <= 0 {
			return
		}
		if cfg.fees == nil {
			cfg.fees = map[int64]*big.Int{}
		}
		cfg.fees[chainID] = new(big.Int).Set(fee)
	}
}

func applyMergeOptions(opts []mergeOption) mergeConfig {
	var cfg mergeConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// WithPriorDelegatedRecipients carries EIP-7702 acknowledgements from a
// grant that is no longer usable. Only a merge with no current grant reads
// them, and only for recipients that are on that fresh grant. The addition
// wins when both name a delegate. A usable grant already carries its own
// acknowledgements, so this option is ignored on that path. Addresses are
// copied.
func WithPriorDelegatedRecipients(acks []model.DelegatedNativeRecipient) mergeOption {
	if len(acks) == 0 {
		return func(*mergeConfig) {}
	}
	cloned := make([]model.DelegatedNativeRecipient, len(acks))
	for i, ack := range acks {
		cloned[i] = cloneDelegatedNativeRecipient(ack)
	}
	return func(cfg *mergeConfig) {
		if cfg == nil {
			return
		}
		cfg.priorDelegated = cloned
	}
}

// WithGrantApplied says the native module is already installed, so this
// merge does not add another first-op premium.
func WithGrantApplied(applied bool) mergeOption {
	return func(cfg *mergeConfig) {
		if cfg != nil && applied {
			cfg.applied = true
		}
	}
}

func (cfg mergeConfig) fee(chainID int64) *big.Int {
	if cfg.fees == nil {
		return nil
	}
	return cfg.fees[chainID]
}

// nativeInstallPremium is the wei of the grant's first UserOp above its
// steady ceiling. The send and payable premiums are the same.
func nativeInstallPremium(fee *big.Int, shape grantGasShape) *big.Int {
	if fee == nil || fee.Sign() <= 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Mul(big.NewInt(shape.installPremiumUnits()), fee)
}

func formatGrantFee(fee *big.Int) string {
	if fee == nil {
		return "0"
	}
	gwei := big.NewInt(1_000_000_000)
	quo, rem := new(big.Int).QuoRem(new(big.Int).Set(fee), gwei, new(big.Int))
	if rem.Sign() == 0 {
		return quo.String() + " gwei"
	}
	return fee.String() + " wei"
}

// selfFundedGasWei is the wei NativeTokenLimitModule will charge for these
// ops at fee. A non-nil install replaces one steady op with that grant's
// first op. The premium is that difference, so a wallet with several
// automations can keep a single install.
func selfFundedGasWei(sendOps, valueOps int64, fee *big.Int, install *grantGasShape) (total, premium *big.Int) {
	zero := big.NewInt(0)
	if fee == nil || fee.Sign() <= 0 || sendOps < 0 || valueOps < 0 || sendOps+valueOps == 0 {
		return zero, new(big.Int)
	}
	units := new(big.Int).Mul(big.NewInt(sendOps), big.NewInt(nativePreflightGasUnitsSteady))
	units.Add(units, new(big.Int).Mul(big.NewInt(valueOps), big.NewInt(nativePreflightGasUnitsValueSteady)))
	var premiumUnits int64
	if install != nil {
		premiumUnits = install.installPremiumUnits()
		units.Add(units, big.NewInt(premiumUnits))
	}
	if units.Sign() <= 0 {
		return zero, new(big.Int)
	}
	total = new(big.Int).Mul(units, fee)
	premium = new(big.Int).Mul(big.NewInt(premiumUnits), fee)
	return total, premium
}

func positiveNativeAmount(cap *model.NativeSpendCap) bool {
	if cap == nil {
		return false
	}
	amt, err := parseCapAmount(cap.Amount)
	return err == nil && amt.Sign() > 0
}

// priceWalletNativeGas adds self-funded gas onto needs that share one
// wallet grant. force is set when the addition or the current grant is
// what installs the native module, so a sibling ERC-20 automation's gas
// is included too. A nil fee does nothing. A non-nil install keeps one
// first-op premium across the set, sized for the grant that shape
// describes; nil prices steady ops only, which is what an already-applied
// grant and a simulate draft use. Needs are mutated. added is the wei of
// gas newly folded into their caps, which the caller adds to a total it
// already summed.
func priceWalletNativeGas(needs []*WorkflowNeed, fee *big.Int, force bool, install *grantGasShape) (added *big.Int, notes []string, err error) {
	added = big.NewInt(0)
	if fee == nil || fee.Sign() <= 0 {
		return added, nil, nil
	}
	if !force && !needsInstallNative(needs) {
		return added, nil, nil
	}
	var priced []*WorkflowNeed
	for _, need := range needs {
		if need == nil || need.Unresolved || need.GasPriced {
			continue
		}
		if need.GasUnsized {
			notes = append(notes, fmt.Sprintf("%s: remaining runs are unknown, so the native cap does not include its gas. Those runs still spend the cap.", displayName(need.Name)))
		}
		if need.NativeSendOps <= 0 && need.NativeValueOps <= 0 {
			need.GasPriced = true
			continue
		}
		total, premium := selfFundedGasWei(need.NativeSendOps, need.NativeValueOps, fee, install)
		if total.Sign() <= 0 {
			need.GasPriced = true
			continue
		}
		if !addNative(need, total) {
			return nil, nil, fmt.Errorf("%w: self-funded gas for %s exceeds uint256", ErrSessionPolicyUnsized, displayName(need.Name))
		}
		addGasAccount(need, total)
		need.InstallPremium = premium
		need.GasPriced = true
		added.Add(added, total)
		priced = append(priced, need)
	}
	if len(priced) == 0 {
		return added, notes, nil
	}
	keep := 0
	for i := 1; i < len(priced); i++ {
		if priced[i].InstallPremium != nil && (priced[keep].InstallPremium == nil || priced[i].InstallPremium.Cmp(priced[keep].InstallPremium) > 0) {
			keep = i
		}
	}
	for i, need := range priced {
		if i == keep || need.InstallPremium == nil || need.InstallPremium.Sign() <= 0 {
			continue
		}
		if !subtractNative(need, need.InstallPremium) {
			return nil, nil, fmt.Errorf("%w: self-funded gas for %s", ErrSessionPolicyUnsized, displayName(need.Name))
		}
		subGasAccount(need, need.InstallPremium)
		added.Sub(added, need.InstallPremium)
		need.InstallPremium = big.NewInt(0)
	}
	if added.Sign() > 0 {
		line := fmt.Sprintf("Native cap includes self-funded gas (%s per gas unit).", formatGrantFee(fee))
		if keptInstallPremium(priced) {
			line = fmt.Sprintf("Native cap includes self-funded gas (%s per gas unit) and one install.", formatGrantFee(fee))
		}
		notes = append([]string{line}, notes...)
	}
	return added, notes, nil
}

// keptInstallPremium is true when this price kept one first-op premium.
func keptInstallPremium(needs []*WorkflowNeed) bool {
	for _, need := range needs {
		if need != nil && need.InstallPremium != nil && need.InstallPremium.Sign() > 0 {
			return true
		}
	}
	return false
}

// noteMissingInstall records the install foldMissingInstall added when no
// priced task already carried that premium.
func noteMissingInstall(notes []string, extra *big.Int, fee *big.Int) []string {
	if extra == nil || extra.Sign() <= 0 {
		return notes
	}
	for _, line := range notes {
		if strings.Contains(line, "one install") {
			return notes
		}
	}
	return append(notes, fmt.Sprintf("Native cap includes self-funded gas (%s per gas unit) and one install.", formatGrantFee(fee)))
}

// foldMissingInstall adds one first-op premium when no priced task already
// carries it. A simulate draft's cap is steady gas plus value, so the
// wallet's one install is added here, including when the new automation
// is not an enabled task yet. An applied grant pays no second install.
func foldMissingInstall(total *big.Int, needs []*WorkflowNeed, fee *big.Int, applied, installing bool, shape grantGasShape) *big.Int {
	extra := big.NewInt(0)
	if applied || !installing || fee == nil || fee.Sign() <= 0 || total == nil {
		return extra
	}
	for _, need := range needs {
		if need != nil && need.InstallPremium != nil && need.InstallPremium.Sign() > 0 {
			return extra
		}
	}
	prem := nativeInstallPremium(fee, shape)
	if prem.Sign() <= 0 {
		return extra
	}
	total.Add(total, prem)
	return prem
}

func addGasAccount(need *WorkflowNeed, amt *big.Int) {
	if need == nil || amt == nil || amt.Sign() == 0 {
		return
	}
	if need.NativeGasWei == nil {
		need.NativeGasWei = big.NewInt(0)
	}
	need.NativeGasWei.Add(need.NativeGasWei, amt)
}

func subGasAccount(need *WorkflowNeed, amt *big.Int) {
	if need == nil || need.NativeGasWei == nil || amt == nil {
		return
	}
	need.NativeGasWei.Sub(need.NativeGasWei, amt)
	if need.NativeGasWei.Sign() <= 0 {
		need.NativeGasWei = nil
	}
}

// additionGasWei is the gas portion of an addition's native cap. It is
// already inside that cap, so callers add it only to the reported split.
func additionGasWei(addition PolicyAddition) (*big.Int, error) {
	if addition.NativeGasWei == "" {
		return big.NewInt(0), nil
	}
	amt, err := parseCapAmount(addition.NativeGasWei)
	if err != nil {
		return nil, fmt.Errorf("%w: new automation native gas: %v", ErrSessionPolicyUnsized, err)
	}
	if addition.NativeSpendCap != nil {
		cap, capErr := parseCapAmount(addition.NativeSpendCap.Amount)
		if capErr == nil && amt.Cmp(cap) > 0 {
			return nil, fmt.Errorf("%w: nativeGasWei exceeds nativeSpendCap", ErrSessionPolicyUnsized)
		}
	}
	return amt, nil
}

func withNativeGas(changes PolicyChanges, gas *big.Int) PolicyChanges {
	if gas == nil || gas.Sign() <= 0 {
		return changes
	}
	changes.NativeGasWei = gas.String()
	return changes
}

func needsInstallNative(needs []*WorkflowNeed) bool {
	for _, need := range needs {
		if need == nil || need.Unresolved || need.NativeUnsized || need.NativeSpendCap == nil {
			continue
		}
		amt, err := parseCapAmount(need.NativeSpendCap.Amount)
		if err == nil && amt.Sign() > 0 {
			return true
		}
	}
	return false
}

func subtractNative(need *WorkflowNeed, amt *big.Int) bool {
	if need == nil || need.NativeSpendCap == nil || amt == nil || amt.Sign() <= 0 {
		return true
	}
	cur, err := parseCapAmount(need.NativeSpendCap.Amount)
	if err != nil {
		return false
	}
	cur.Sub(cur, amt)
	if cur.Sign() < 0 {
		return false
	}
	if cur.Sign() == 0 {
		need.NativeSpendCap = nil
		return true
	}
	need.NativeSpendCap.Amount = cur.String()
	return true
}

func appendGasNotes(changes PolicyChanges, notes []string) PolicyChanges {
	if len(notes) == 0 {
		return changes
	}
	changes.Summary = append(changes.Summary, notes...)
	return changes
}

// noteNativeOps records UserOps whose gas a self-funded native cap must
// hold. A bound split or batch does not say how many ops one run sends, so
// the cap cannot include them and the prepare summary says so.
func noteNativeOps(need *WorkflowNeed, site fundSite, runs int64, runsKnown bool) {
	if need == nil {
		return
	}
	onchain := site.kind == fundEth || site.kind == fundCall || site.opsUnknown
	if !onchain {
		return
	}
	if site.opsUnknown || site.loopUnknown || !runsKnown {
		need.GasUnsized = true
		return
	}
	if site.userOp <= 0 {
		return
	}
	n := runs * int64(site.userOp)
	if site.loopMult > 1 {
		n *= int64(site.loopMult)
	}
	if n <= 0 {
		return
	}
	switch site.kind {
	case fundEth:
		need.NativeSendOps += n
	case fundCall:
		need.NativeValueOps += n
	}
}

func firstMissingNativeRecipient(policy *model.SessionPolicy, need *WorkflowNeed) *common.Address {
	if policy == nil || need == nil {
		return nil
	}
	for _, rec := range need.NativeRecipients {
		if rec == nil {
			continue
		}
		if !nativeRecipientAllowed(policy, *rec) {
			return rec
		}
	}
	return nil
}

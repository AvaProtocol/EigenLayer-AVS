package taskengine

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

const (
	BudgetDiscoveryRequiredCode    = "BUDGET_DISCOVERY_REQUIRED"
	BudgetDiscoveryUnavailableCode = "BUDGET_DISCOVERY_UNAVAILABLE"
	BudgetResetUnsupportedCode     = "BUDGET_RESET_UNSUPPORTED_TOKEN"
	budgetDiscoveryMaxPairs        = 10000
	budgetDiscoveryMaxAge          = 2 * time.Minute
)

type BudgetApprovalPair struct {
	Token   common.Address `json:"token"`
	Spender common.Address `json:"spender"`
}

type BudgetApprovalDiscovery struct {
	Owner            common.Address       `json:"owner"`
	Wallet           common.Address       `json:"wallet"`
	ChainID          int64                `json:"chainId"`
	Candidates       []BudgetApprovalPair `json:"candidates"`
	AllowedContracts []common.Address     `json:"allowedContracts"`
	Source           string               `json:"source"`
	FetchedAt        int64                `json:"fetchedAt"`
	Complete         bool                 `json:"complete"`
	Signature        []byte               `json:"signature"`
}

type BudgetApprovalReset struct {
	Token   common.Address `json:"token"`
	Spender common.Address `json:"spender"`
	Amount  string         `json:"amount"`
}

func canonicalBudgetPairs(pairs []BudgetApprovalPair) ([]BudgetApprovalPair, error) {
	if len(pairs) > budgetDiscoveryMaxPairs {
		return nil, fmt.Errorf("approval discovery exceeds %d pairs", budgetDiscoveryMaxPairs)
	}
	seen := map[BudgetApprovalPair]bool{}
	out := make([]BudgetApprovalPair, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Token == (common.Address{}) || pair.Spender == (common.Address{}) {
			return nil, fmt.Errorf("approval discovery contains a zero address")
		}
		if !seen[pair] {
			seen[pair] = true
			out = append(out, pair)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Token != out[j].Token {
			return bytes.Compare(out[i].Token[:], out[j].Token[:]) < 0
		}
		return bytes.Compare(out[i].Spender[:], out[j].Spender[:]) < 0
	})
	return out, nil
}

func budgetABIWords(values ...interface{}) ([]byte, error) {
	args := abi.Arguments{}
	for _, value := range values {
		name := "bytes32"
		switch value.(type) {
		case common.Address:
			name = "address"
		case *big.Int:
			name = "uint256"
		case bool:
			name = "bool"
		}
		typ, err := abi.NewType(name, "", nil)
		if err != nil {
			return nil, err
		}
		args = append(args, abi.Argument{Type: typ})
	}
	return args.Pack(values...)
}

// BudgetDiscoveryDigest is EIP-712, independent of the owner's UserOperation
// signature. Canonical ordering and deduplication do not change inventory scope.
func BudgetDiscoveryDigest(d BudgetApprovalDiscovery) (common.Hash, error) {
	if d.Owner == (common.Address{}) || d.Wallet == (common.Address{}) || d.ChainID <= 0 || d.FetchedAt <= 0 {
		return common.Hash{}, fmt.Errorf("invalid discovery identity or timestamp")
	}
	pairs, err := canonicalBudgetPairs(d.Candidates)
	if err != nil {
		return common.Hash{}, err
	}
	var pairWords []byte
	for _, pair := range pairs {
		pairWords = append(pairWords, common.LeftPadBytes(pair.Token.Bytes(), 32)...)
		pairWords = append(pairWords, common.LeftPadBytes(pair.Spender.Bytes(), 32)...)
	}
	targetsHash, err := budgetDiscoveryTargetsHash(d.AllowedContracts)
	if err != nil {
		return common.Hash{}, err
	}
	domain, err := budgetABIWords(crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")), crypto.Keccak256Hash([]byte("Ava Wallet Budget Approval Discovery")), crypto.Keccak256Hash([]byte("1")), big.NewInt(d.ChainID), d.Wallet)
	if err != nil {
		return common.Hash{}, err
	}
	body, err := budgetABIWords(crypto.Keccak256Hash([]byte("ApprovalDiscovery(address owner,address wallet,bytes32 candidatesHash,bytes32 allowedContractsHash,string source,uint256 fetchedAt,bool complete)")), d.Owner, d.Wallet, crypto.Keccak256Hash(pairWords), targetsHash, crypto.Keccak256Hash([]byte(d.Source)), big.NewInt(d.FetchedAt), d.Complete)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, crypto.Keccak256(domain), crypto.Keccak256(body)), nil
}

func budgetDiscoveryTargetsHash(targets []common.Address) (common.Hash, error) {
	if len(targets) > budgetDiscoveryMaxPairs {
		return common.Hash{}, fmt.Errorf("too many discovery targets")
	}
	unique := map[common.Address]bool{}
	for _, target := range targets {
		if target == (common.Address{}) {
			return common.Hash{}, fmt.Errorf("zero discovery target")
		}
		unique[target] = true
	}
	sorted := make([]common.Address, 0, len(unique))
	for target := range unique {
		sorted = append(sorted, target)
	}
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i][:], sorted[j][:]) < 0 })
	var words []byte
	for _, target := range sorted {
		words = append(words, common.LeftPadBytes(target.Bytes(), 32)...)
	}
	return crypto.Keccak256Hash(words), nil
}

func budgetDiscoveryCovers(d *BudgetApprovalDiscovery, permissions SessionPermissions) bool {
	allowed := map[common.Address]bool{}
	for _, target := range d.AllowedContracts {
		allowed[target] = true
	}
	for _, action := range permissions.AllowedActions {
		if action.Target != nil && !allowed[*action.Target] {
			return false
		}
	}
	for _, recipient := range permissions.NativeRecipients {
		if recipient != nil && !allowed[*recipient] {
			return false
		}
	}
	return true
}

//nolint:gocyclo // Scope, freshness, completeness and canonical EIP-712 signature checks are all mandatory.
func verifyBudgetDiscovery(d *BudgetApprovalDiscovery, owner, wallet, signer common.Address, chainID int64, now time.Time) ([]BudgetApprovalPair, error) {
	refused := func(detail string) ([]BudgetApprovalPair, error) {
		return nil, &BudgetPolicyError{Code: BudgetDiscoveryRequiredCode, Detail: detail}
	}
	if d == nil || signer == (common.Address{}) {
		return refused("Fresh approval discovery from the trusted backend is required.")
	}
	if d.Owner != owner || d.Wallet != wallet || d.ChainID != chainID || d.Source != "moralis" || d.FetchedAt <= 0 || d.FetchedAt > now.UnixMilli() || now.UnixMilli()-d.FetchedAt > budgetDiscoveryMaxAge.Milliseconds() {
		return refused("Approval discovery does not match this owner, wallet and chain, or is stale.")
	}
	pairs, err := canonicalBudgetPairs(d.Candidates)
	if err != nil {
		return refused(err.Error())
	}
	digest, err := BudgetDiscoveryDigest(*d)
	if err != nil {
		return refused("Invalid discovery digest.")
	}
	sig := append([]byte(nil), d.Signature...)
	if len(sig) != 65 {
		return refused("Invalid discovery signature.")
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return refused("Invalid discovery signature.")
	}
	pub, err := crypto.SigToPub(digest.Bytes(), sig)
	if err != nil || crypto.PubkeyToAddress(*pub) != signer {
		return refused("Discovery was not signed by the configured backend.")
	}
	if !d.Complete {
		return nil, &BudgetPolicyError{Code: BudgetDiscoveryUnavailableCode, Detail: "Approval inventory is incomplete. Try again after discovery succeeds."}
	}
	return pairs, nil
}

//nolint:gocyclo // First install/migration/new totals reset all targets; additions reset only new targets.
func budgetResetSpenders(current *model.SessionPolicy, draft BudgetPolicyDraft, owner *WalletBudgetInput) map[common.Address]bool {
	old := map[common.Address]bool{}
	if current != nil {
		for _, action := range current.AllowedActions {
			if action.Target != nil {
				old[*action.Target] = true
			}
		}
		for _, recipient := range current.NativeRecipients {
			if recipient != nil {
				old[*recipient] = true
			}
		}
	}
	all := current == nil || !isWalletBudget(current) || owner != nil && owner.StartNewTotals
	out := map[common.Address]bool{}
	for _, action := range draft.Permissions.AllowedActions {
		if action.Target != nil && (all || !old[*action.Target]) {
			out[*action.Target] = true
		}
	}
	// Native recipients are also callable account targets (including acknowledged
	// delegated EOAs). A fallback can spend an existing approval without calldata.
	for _, recipient := range draft.Permissions.NativeRecipients {
		if recipient != nil && (all || !old[*recipient]) {
			out[*recipient] = true
		}
	}
	return out
}

// Only USDC on Sepolia has the phase-1 failure/zeroing proof so far. More
// deployments must get that proof before they can be reset automatically.
func budgetResetTokenTested(chainID int64, token common.Address) bool {
	return chainID == 11155111 && token == common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
}

func readBudgetApprovals(ctx context.Context, caller aa.ContractCaller, wallet common.Address, chainID int64, pairs []BudgetApprovalPair, resetSpenders map[common.Address]bool) ([]BudgetApprovalReset, []BudgetApprovalReset, error) {
	reads := make([]BudgetApprovalReset, 0, len(pairs))
	resets := []BudgetApprovalReset{}
	if len(pairs) > 0 && nilBudgetDependency(caller) {
		return nil, nil, fmt.Errorf("%w: no allowance reader", ErrBudgetReadUnavailable)
	}
	for _, pair := range pairs {
		data := append(crypto.Keccak256([]byte("allowance(address,address)"))[:4], common.LeftPadBytes(wallet.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(pair.Spender.Bytes(), 32)...)
		result, err := caller.CallContract(ctx, ethereum.CallMsg{To: &pair.Token, Data: data}, nil)
		if err != nil || len(result) != 32 {
			return nil, nil, fmt.Errorf("%w: allowance for %s/%s is unavailable", ErrBudgetReadUnavailable, pair.Token.Hex(), pair.Spender.Hex())
		}
		amount := new(big.Int).SetBytes(result)
		row := BudgetApprovalReset{Token: pair.Token, Spender: pair.Spender, Amount: amount.String()}
		reads = append(reads, row)
		if !resetSpenders[pair.Spender] || amount.Sign() == 0 {
			continue
		}
		if !budgetResetTokenTested(chainID, pair.Token) {
			return nil, nil, &BudgetPolicyError{Code: BudgetResetUnsupportedCode, Detail: "Revoke this approval with the approval scan before preparing the budget.", Details: map[string]string{"token": pair.Token.Hex(), "spender": pair.Spender.Hex()}}
		}
		resets = append(resets, row)
	}
	return reads, resets, nil
}

package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type budgetChainView interface {
	aa.ContractCaller
	Block() BudgetBlock
	CodeAt(context.Context, common.Address) ([]byte, error)
	Nonce(context.Context, common.Address) (*big.Int, error)
	ValidateOwner(context.Context, *BudgetPolicyPreparation) error
	CheckAuthorities(context.Context, common.Address, common.Address) error
	VerifyCanonical(context.Context) error
	Close()
}
type budgetObservation struct {
	Nonce           *big.Int
	Hash            common.Hash
	TransactionHash common.Hash
	Block           BudgetBlock
	Success         bool
}
type budgetChangeBackend interface {
	View(context.Context, int64, *big.Int) (budgetChainView, error)
	Observe(context.Context, *BudgetHold) (*budgetObservation, error)
	Send(context.Context, *BudgetPolicyPreparation, *userop.UserOperationV07) (common.Hash, error)
}
type defaultBudgetChangeBackend struct{ n *Engine }
type pinnedBudgetView struct {
	chain   *ethclient.Client
	block   BudgetBlock
	n       *Engine
	chainID int64
}

func (n *Engine) budgetBackend() budgetChangeBackend {
	if n.budgetChangeBackend != nil {
		return n.budgetChangeBackend
	}
	return &defaultBudgetChangeBackend{n: n}
}
func (b *defaultBudgetChangeBackend) View(ctx context.Context, chain int64, number *big.Int) (budgetChainView, error) {
	sw := b.n.ResolveSmartWalletConfig(chain)
	if sw == nil {
		return nil, ErrSessionChainNotServed
	}
	client, e := ethclient.DialContext(ctx, sw.EthRpcUrl)
	if e != nil {
		return nil, e
	}
	block, e := budgetBlockAt(ctx, client, number)
	if e != nil {
		client.Close()
		return nil, e
	}
	return &pinnedBudgetView{chain: client, block: block, n: b.n, chainID: chain}, nil
}

// Preserve the RPC's canonical hash. Recomputing Header.Hash after decoding
// into a narrower client header can omit fork-specific fields and produce a
// hash the chain never had. The numbered identity reads and hash-pinned state
// reads must refer to the same block, including on newly upgraded networks.
func budgetBlockAt(ctx context.Context, client *ethclient.Client, number *big.Int) (BudgetBlock, error) {
	tag := "latest"
	if number != nil {
		tag = hexutil.EncodeBig(number)
	}
	block, err := budgetBlockAtTag(ctx, client, tag)
	if err != nil {
		return BudgetBlock{}, err
	}
	if number != nil && new(big.Int).SetUint64(block.Number).Cmp(number) != 0 {
		return BudgetBlock{}, fmt.Errorf("%w: wrong canonical block number", ErrBudgetReadUnavailable)
	}
	return block, nil
}

func budgetBlockAtTag(ctx context.Context, client *ethclient.Client, tag string) (BudgetBlock, error) {
	var block struct {
		Number *hexutil.Big `json:"number"`
		Hash   common.Hash  `json:"hash"`
	}
	if err := client.Client().CallContext(ctx, &block, "eth_getBlockByNumber", tag, false); err != nil {
		return BudgetBlock{}, fmt.Errorf("%w: canonical block unavailable: %w", ErrBudgetReadUnavailable, err)
	}
	if block.Number == nil || (*big.Int)(block.Number).Sign() < 0 || (*big.Int)(block.Number).BitLen() > 64 || block.Hash == (common.Hash{}) {
		return BudgetBlock{}, fmt.Errorf("%w: invalid canonical block", ErrBudgetReadUnavailable)
	}
	return BudgetBlock{Number: (*big.Int)(block.Number).Uint64(), Hash: block.Hash}, nil
}
func (v *pinnedBudgetView) Block() BudgetBlock { return v.block }
func (v *pinnedBudgetView) Close()             { v.chain.Close() }
func (v *pinnedBudgetView) CodeAt(ctx context.Context, address common.Address) ([]byte, error) {
	return v.chain.CodeAtHash(ctx, address, v.block.Hash)
}
func (v *pinnedBudgetView) CallContract(ctx context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return v.chain.CallContractAtHash(ctx, msg, v.block.Hash)
}
func (v *pinnedBudgetView) VerifyCanonical(ctx context.Context) error {
	block, e := budgetBlockAt(ctx, v.chain, new(big.Int).SetUint64(v.block.Number))
	if e != nil {
		return e
	}
	if block.Hash != v.block.Hash {
		return fmt.Errorf("%w: budget read block is no longer canonical", ErrBudgetReadUnavailable)
	}
	return nil
}
func (v *pinnedBudgetView) Nonce(ctx context.Context, wallet common.Address) (*big.Int, error) {
	data := append(crypto.Keccak256([]byte("getNonce(address,uint192)"))[:4], common.LeftPadBytes(wallet.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes([]byte{1}, 32)...)
	ep := preset.EntryPointV07()
	out, e := v.CallContract(ctx, ethereum.CallMsg{To: &ep, Data: data}, nil)
	if e != nil {
		return nil, fmt.Errorf("%w: owner nonce: %w", ErrBudgetReadUnavailable, e)
	}
	if len(out) != 32 {
		return nil, fmt.Errorf("%w: owner nonce", ErrBudgetReadUnavailable)
	}
	return new(big.Int).SetBytes(out), nil
}
func (v *pinnedBudgetView) ValidateOwner(ctx context.Context, p *BudgetPolicyPreparation) error {
	sw := *v.n.ResolveSmartWalletConfig(p.ChainID)
	sw.DisableGasSponsorship = true
	sw.AlchemyPaymasterPolicyID = ""
	return preset.ValidateOwnerUserOpAt(ctx, &sw, p.Owner, p.Salt, &preset.PreparedOwnerUserOp{Operation: (*userop.UserOperationV07)(p.Operation), Identity: p.Identity}, new(big.Int).SetUint64(v.block.Number))
}

var budgetUserOpEvent = crypto.Keccak256Hash([]byte("UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)"))

func (b *defaultBudgetChangeBackend) Observe(ctx context.Context, hold *BudgetHold) (*budgetObservation, error) {
	view, e := b.View(ctx, hold.ChainID, nil)
	if e != nil {
		return nil, e
	}
	defer view.Close()
	v, ok := view.(*pinnedBudgetView)
	if !ok {
		return nil, fmt.Errorf("invalid production budget view")
	}
	nonce, e := v.Nonce(ctx, hold.Wallet)
	if e != nil {
		return nil, e
	}
	result := &budgetObservation{Nonce: nonce}
	old, ok := new(big.Int).SetString(hold.Nonce, 10)
	if !ok {
		return nil, fmt.Errorf("corrupt budget hold nonce")
	}
	if nonce.Cmp(old) < 0 {
		return nil, fmt.Errorf("owner nonce regressed; budget hold retained")
	}
	if nonce.Cmp(old) == 0 {
		return result, v.VerifyCanonical(ctx)
	}
	// Read canonical EntryPoint events rather than treating a bundler's missing
	// receipt as proof of absence. Unknown consuming hashes stay visible.
	e = v.forEachBudgetLog(ctx, ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(hold.FromBlock), ToBlock: new(big.Int).SetUint64(v.block.Number), Addresses: []common.Address{preset.EntryPointV07()}, Topics: [][]common.Hash{{budgetUserOpEvent}, nil, {common.BytesToHash(hold.Wallet.Bytes())}}}, func(log types.Log) error {
		if log.Removed || len(log.Topics) != 4 || len(log.Data) != 128 || new(big.Int).SetBytes(log.Data[:32]).Cmp(old) != 0 {
			return nil
		}
		if result.Hash != (common.Hash{}) {
			return fmt.Errorf("multiple canonical owner events for one nonce")
		}
		result.Hash = log.Topics[1]
		result.TransactionHash = log.TxHash
		result.Block = BudgetBlock{Number: log.BlockNumber, Hash: log.BlockHash}
		result.Success = new(big.Int).SetBytes(log.Data[32:64]).Sign() != 0
		return nil
	})
	if e != nil {
		return nil, e
	}
	if e = v.VerifyCanonical(ctx); e != nil {
		return nil, e
	}
	return result, nil
}
func (b *defaultBudgetChangeBackend) Send(ctx context.Context, p *BudgetPolicyPreparation, op *userop.UserOperationV07) (common.Hash, error) {
	sw := *b.n.ResolveSmartWalletConfig(p.ChainID)
	sw.DisableGasSponsorship = true
	sw.AlchemyPaymasterPolicyID = ""
	return preset.SubmitOwnerUserOp(ctx, &sw, op)
}

// Explicit v1 refusal. A selector that shares ERC-20's approve signature does
// not establish that an arbitrary contract is a fungible token.
func refuseUnsupportedBudgetRoutes(chain int64, p SessionPermissions) error {
	refuse := func(target common.Address, selector string) error {
		return &BudgetPolicyError{Code: BudgetRouteUnsupportedCode, Detail: "Unsupported Permit2/NFT/operator or unaudited route: " + target.Hex() + " " + selector}
	}
	permitted := map[common.Address]map[string]bool{}
	add := func(target common.Address, signatures ...string) {
		if target == (common.Address{}) {
			return
		}
		if permitted[target] == nil {
			permitted[target] = map[string]bool{}
		}
		for _, sig := range signatures {
			permitted[target][functionSelector(sig)] = true
		}
	}
	add(budgetContracts["swapRouter"][chain], "exactInputSingle((address,address,uint24,address,uint256,uint256,uint160))", "exactInput((bytes,address,uint256,uint256))", "exactOutputSingle((address,address,uint24,address,uint256,uint256,uint160))", "exactOutput((bytes,address,uint256,uint256))", "multicall(bytes[])", "multicall(uint256,bytes[])", "wrapETH(uint256)", "unwrapWETH9(uint256,address)", "refundETH()")
	// Legacy standard permissions include collateral configuration. It remains
	// an explicitly disclosed unmetered call, like the carried withdraw/borrow rights.
	add(budgetContracts["aavePool"][chain], "supply(address,uint256,address,uint16)", "repay(address,uint256,uint256,address)", "withdraw(address,uint256,address)", "borrow(address,uint256,uint256,uint16,address)", "setUserUseReserveAsCollateral(address,bool)")
	add(budgetContracts["wrappedNative"][chain], "deposit()", "withdraw(uint256)")
	// Studio's first-budget catalog includes Lido staking. Its ETH input must
	// be metered by the native limit, matching unmeteredKind's classification.
	if p.NativeSpendCap != nil {
		add(budgetContracts["lidoStake"][chain], "submit(address)")
	}
	for _, a := range p.AllowedActions {
		if a.Target == nil {
			return budgetInputError("budget action target is missing")
		}
		target := *a.Target
		loadTokenCatalog(nil)
		tokenCatalogMutex.RLock()
		_, knownToken := tokenCatalog[uint64(chain)][strings.ToLower(target.Hex())]
		tokenCatalogMutex.RUnlock()
		for _, s := range a.Selectors {
			s = normalizeSelector(s)
			if permitted[target][s] {
				continue
			}
			if knownToken && (s == selectorApprove || s == selectorTransfer) {
				continue
			}
			return refuse(target, s)
		}
	}
	// Unknown callable fallback code is not established safe by an EOA label.
	if p.AllowContractRecipient {
		for _, target := range p.NativeRecipients {
			if target != nil && permitted[*target] == nil {
				return refuse(*target, "native fallback")
			}
		}
	}
	return nil
}

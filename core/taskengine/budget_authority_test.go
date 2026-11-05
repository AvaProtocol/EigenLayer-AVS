package taskengine

import (
	"context"
	"errors"
	"math"
	"math/big"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

type budgetInventoryRPC struct {
	mu                sync.Mutex
	block             BudgetBlock
	wallet            common.Address
	deployed          uint64
	code              hexutil.Bytes
	logLimit          uint64
	logs              []types.Log
	queries           [][2]uint64
	codeReads         []rpc.BlockNumberOrHash
	registrationCalls int
	registered        bool
	pinnedCodeError   bool
	historyError      bool
	registrationError bool
	logErrorFrom      *uint64
	canonicalHash     common.Hash
	finalized         *uint64
	blockReadError    *uint64
	blockHashes       map[uint64]common.Hash
}

func (f *budgetInventoryRPC) GetCode(_ context.Context, address common.Address, at rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codeReads = append(f.codeReads, at)
	if address != f.wallet {
		return nil, errors.New("wrong inventory wallet")
	}
	if hash, ok := at.Hash(); ok {
		number := new(big.Int).Sub(hash.Big(), big.NewInt(1000))
		if f.pinnedCodeError || hash != f.block.Hash && (!number.IsUint64() || number.Uint64() > f.block.Number) {
			return nil, errors.New("pinned code unavailable")
		}
		return f.code, nil
	}
	if f.historyError {
		return nil, errors.New("historical code unavailable")
	}
	number, ok := at.Number()
	if !ok || number < 0 {
		return nil, errors.New("expected numbered historical code")
	}
	if uint64(number) < f.deployed {
		return hexutil.Bytes{}, nil
	}
	return f.code, nil
}

func budgetInventoryBlockHash(number uint64) common.Hash {
	return common.BigToHash(new(big.Int).Add(new(big.Int).SetUint64(number), big.NewInt(1000)))
}

func (f *budgetInventoryRPC) GetBlockByNumber(_ context.Context, requested rpc.BlockNumber, _ bool) (map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	number := f.block.Number
	if requested >= 0 {
		number = uint64(requested)
	} else if requested == rpc.FinalizedBlockNumber && f.finalized != nil {
		number = *f.finalized
	}
	if f.blockReadError != nil && number == *f.blockReadError {
		return nil, errors.New("canonical checkpoint unavailable")
	}
	hash := budgetInventoryBlockHash(number)
	if override, ok := f.blockHashes[number]; ok {
		hash = override
	}
	if number == f.block.Number {
		hash = f.block.Hash
		if f.canonicalHash != (common.Hash{}) {
			hash = f.canonicalHash
		}
	}
	return map[string]interface{}{"number": hexutil.EncodeUint64(number), "hash": hash}, nil
}

func (f *budgetInventoryRPC) GetLogs(_ context.Context, query struct {
	FromBlock hexutil.Uint64   `json:"fromBlock"`
	ToBlock   hexutil.Uint64   `json:"toBlock"`
	Addresses []common.Address `json:"address"`
	Topics    [][]common.Hash  `json:"topics"`
}) ([]types.Log, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	from, to := uint64(query.FromBlock), uint64(query.ToBlock)
	f.queries = append(f.queries, [2]uint64{from, to})
	if to < from || to-from >= f.logLimit {
		return nil, errors.New("eth_getLogs range exceeds provider limit")
	}
	if f.logErrorFrom != nil && from >= *f.logErrorFrom {
		return nil, errors.New("inventory page unavailable")
	}
	logs := []types.Log{}
	for _, log := range f.logs {
		if log.BlockNumber < from || log.BlockNumber > to {
			continue
		}
		if len(query.Addresses) != 1 || query.Addresses[0] != log.Address || len(query.Topics) == 0 || len(query.Topics[0]) != 1 || query.Topics[0][0] != log.Topics[0] {
			return nil, errors.New("wrong budget inventory filter")
		}
		logs = append(logs, log)
	}
	return logs, nil
}

func (f *budgetInventoryRPC) Call(_ context.Context, msg struct {
	To common.Address `json:"to"`
}, at rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if hash, ok := at.Hash(); !ok || hash != f.block.Hash {
		return nil, errors.New("registration/nonce read was not pinned")
	}
	if msg.To == preset.EntryPointV07() {
		return budgetWord(2), nil
	}
	f.registrationCalls++
	if f.registrationError {
		return nil, errors.New("registration unavailable")
	}
	// ABI encoding of (uint8,bytes25[],bytes25[],bytes4[]) with empty lists.
	flag := int64(0)
	if f.registered {
		flag = 1
	}
	var out hexutil.Bytes
	for _, word := range []int64{32, flag, 128, 160, 192, 0, 0, 0} {
		out = append(out, budgetWord(word)...)
	}
	return out, nil
}

func newBudgetInventoryView(t *testing.T, head, deployed, blockRange uint64) (*pinnedBudgetView, *budgetInventoryRPC) {
	t.Helper()
	f := &budgetInventoryRPC{block: BudgetBlock{Number: head, Hash: budgetInventoryBlockHash(head)}, wallet: common.HexToAddress("0xa1"), deployed: deployed, code: hexutil.Bytes{0x60, 0x00}, logLimit: blockRange}
	server := rpc.NewServer()
	require.NoError(t, server.RegisterName("eth", f))
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	chain, err := ethclient.Dial(httpServer.URL)
	require.NoError(t, err)
	t.Cleanup(chain.Close)
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	n := &Engine{db: db, smartWalletConfig: &config.SmartWalletConfig{EthRpcUrl: httpServer.URL, BudgetLogBlockRange: blockRange}}
	return &pinnedBudgetView{chain: chain, block: f.block, n: n, chainID: testPolicyChain}, f
}

func budgetInstallLog(f *budgetInventoryRPC, block uint64) types.Log {
	return types.Log{Address: f.wallet, BlockNumber: block, BlockHash: f.block.Hash, Topics: []common.Hash{budgetValidationInstalled, common.HexToHash("0x123"), common.BigToHash(big.NewInt(99))}, Data: []byte{}}
}

func TestBudgetAuthoritiesUndeployedSkipsInventory(t *testing.T) {
	v, f := newBudgetInventoryView(t, 12_071_724, 0, 3)
	f.code = nil
	require.NoError(t, v.CheckAuthorities(context.Background(), common.HexToAddress("0xb1"), f.wallet))
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Empty(t, f.queries)
	require.Len(t, f.codeReads, 1)
	hash, pinned := f.codeReads[0].Hash()
	require.True(t, pinned)
	require.Equal(t, v.block.Hash, hash)
}

func TestBudgetAuthoritiesFindsRegisteredAuthorityInLaterPage(t *testing.T) {
	v, f := newBudgetInventoryView(t, 12_071_724, 12_071_715, 3)
	// The chosen chain's setting must win over the default-chain setting.
	v.n.chainConfigs = map[int64]*config.ChainConfig{testPolicyChain: {SmartWallet: &config.SmartWalletConfig{BudgetLogBlockRange: 3}}}
	v.n.smartWalletConfig.BudgetLogBlockRange = 100
	f.registered = true
	f.logs = []types.Log{budgetInstallLog(f, 12_071_722)}
	err := v.CheckAuthorities(context.Background(), common.HexToAddress("0xb1"), f.wallet)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, [][2]uint64{{12_071_715, 12_071_717}, {12_071_718, 12_071_720}, {12_071_721, 12_071_723}, {12_071_724, 12_071_724}}, f.queries)
	require.Equal(t, 1, f.registrationCalls)
	require.NotContains(t, err.Error(), "account validation inventory:")
}

func TestBudgetAuthoritiesScansEveryPageIncludingConstructor(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	f.logs = []types.Log{budgetInstallLog(f, 11), budgetInstallLog(f, 20)}
	require.NoError(t, v.CheckAuthorities(context.Background(), common.HexToAddress("0xb1"), f.wallet))
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, [][2]uint64{{11, 13}, {14, 16}, {17, 19}, {20, 20}}, f.queries)
	require.Equal(t, 1, f.registrationCalls, "repeat candidates are checked only once at the pinned block")
}

func TestBudgetAuthoritiesDelegationDoesNotEraseEarlierInventory(t *testing.T) {
	v, f := newBudgetInventoryView(t, 10, 7, 3)
	f.code = append(hexutil.Bytes{0xef, 0x01, 0x00}, config.SMA7702Delegate().Bytes()...)
	f.logs = []types.Log{budgetInstallLog(f, 2)}
	f.registered = true
	err := v.CheckAuthorities(context.Background(), f.wallet, f.wallet)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, [][2]uint64{{0, 2}, {3, 5}, {6, 8}, {9, 10}}, f.queries)
	require.Len(t, f.codeReads, 1, "delegation history must not be binary-searched as if clearing code cleared storage")
}

func TestBudgetAuthoritiesReadFailuresRemainClosed(t *testing.T) {
	for _, name := range []string{"pinned code", "deployment history", "later log page", "registration", "malformed event", "reorg", "undeployed reorg"} {
		t.Run(name, func(t *testing.T) {
			v, f := newBudgetInventoryView(t, 20, 11, 3)
			switch name {
			case "pinned code":
				f.pinnedCodeError = true
			case "deployment history":
				f.historyError = true
			case "later log page":
				from := uint64(14)
				f.logErrorFrom = &from
			case "registration":
				f.logs = []types.Log{budgetInstallLog(f, 11)}
				f.registrationError = true
			case "malformed event":
				log := budgetInstallLog(f, 11)
				log.Topics = log.Topics[:2]
				f.logs = []types.Log{log}
			case "reorg":
				f.canonicalHash = common.HexToHash("0xdead")
			case "undeployed reorg":
				f.code = nil
				f.canonicalHash = common.HexToHash("0xdead")
			}
			require.ErrorIs(t, v.CheckAuthorities(context.Background(), common.HexToAddress("0xb1"), f.wallet), ErrBudgetReadUnavailable)
		})
	}
}

func TestBudgetLogRangesAreInclusiveAndDoNotOverflow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		from  uint64
		to    uint64
		limit uint64
		want  [][2]uint64
	}{
		{"genesis", 0, 0, 3, [][2]uint64{{0, 0}}},
		{"exact range", 0, 2, 3, [][2]uint64{{0, 2}}},
		{"extra block", 0, 3, 3, [][2]uint64{{0, 2}, {3, 3}}},
		{"uint64 end", math.MaxUint64 - 2, math.MaxUint64, 2, [][2]uint64{{math.MaxUint64 - 2, math.MaxUint64 - 1}, {math.MaxUint64, math.MaxUint64}}},
		{"default range", 0, 10_000, 10_000, [][2]uint64{{0, 9_999}, {10_000, 10_000}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, f := newBudgetInventoryView(t, tc.to, 0, tc.limit)
			if tc.name == "default range" {
				v.n.smartWalletConfig.BudgetLogBlockRange = 0
			}
			err := v.forEachBudgetLog(context.Background(), ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(tc.from), ToBlock: new(big.Int).SetUint64(tc.to)}, func(types.Log) error { return nil })
			require.NoError(t, err)
			f.mu.Lock()
			defer f.mu.Unlock()
			require.Equal(t, tc.want, f.queries)
		})
	}
}

func TestBudgetObserveFindsOwnerOperationInLaterPage(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 0, 3)
	hash := common.HexToHash("0x9876")
	event := types.Log{Address: preset.EntryPointV07(), BlockNumber: 18, BlockHash: f.block.Hash, TxHash: common.HexToHash("0x9877"), Topics: []common.Hash{budgetUserOpEvent, hash, common.BytesToHash(f.wallet.Bytes()), {}}}
	for _, word := range []int64{1, 1, 0, 0} {
		event.Data = append(event.Data, budgetWord(word)...)
	}
	f.logs = []types.Log{event}
	backend := &defaultBudgetChangeBackend{n: v.n}
	result, err := backend.Observe(context.Background(), &BudgetHold{ChainID: testPolicyChain, Wallet: f.wallet, Nonce: "1", FromBlock: 11})
	require.NoError(t, err)
	require.Equal(t, hash, result.Hash)
	require.True(t, result.Success)
	require.Equal(t, event.TxHash, result.TransactionHash)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, [][2]uint64{{11, 13}, {14, 16}, {17, 19}, {20, 20}}, f.queries)
}

func TestBudgetLogCancellationDoesNotSkipRemainingPages(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 0, 3)
	f.logs = []types.Log{budgetInstallLog(f, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := v.forEachBudgetLog(ctx, ethereum.FilterQuery{FromBlock: big.NewInt(0), ToBlock: big.NewInt(20), Addresses: []common.Address{f.wallet}, Topics: [][]common.Hash{{budgetValidationInstalled}}}, func(types.Log) error { cancel(); return nil })
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.ErrorIs(t, err, context.Canceled)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.queries, 1)
}

type budgetInventoryBackend struct {
	budgetChangeBackend
	inventory *pinnedBudgetView
}

func (b *budgetInventoryBackend) View(ctx context.Context, chain int64, at *big.Int) (budgetChainView, error) {
	view, err := b.budgetChangeBackend.View(ctx, chain, at)
	if err != nil {
		return nil, err
	}
	return &budgetInventoryChainView{budgetChainView: view, inventory: b.inventory}, nil
}

type budgetInventoryChainView struct {
	budgetChainView
	inventory *pinnedBudgetView
}

func (v *budgetInventoryChainView) CheckAuthorities(ctx context.Context, owner, wallet common.Address) error {
	return v.inventory.CheckAuthorities(ctx, owner, wallet)
}

func TestBudgetSubmitInventoryPreservesSendBoundary(t *testing.T) {
	for _, undeployed := range []bool{true, false} {
		name := "deployed inventory failure"
		if undeployed {
			name = "counterfactual account"
		}
		t.Run(name, func(t *testing.T) {
			fixture, p, backend, signature := newBudgetChangeTest(t)
			inventory, rpcFixture := newBudgetInventoryView(t, 20, 11, 3)
			rpcFixture.wallet = p.Wallet
			if undeployed {
				rpcFixture.code = nil
			} else {
				failedPage := uint64(14)
				rpcFixture.logErrorFrom = &failedPage
			}
			fixture.n.budgetChangeBackend = &budgetInventoryBackend{budgetChangeBackend: backend, inventory: inventory}
			result, err := fixture.n.SubmitBudgetPolicyPreparation(context.Background(), fixture.user, p.Wallet, p.ChainID, p.ID, signature)
			if undeployed {
				require.NoError(t, err)
				require.Equal(t, ownerSubmissionSubmitted, result.State)
				require.EqualValues(t, 1, backend.sent.Load(), "only the offline fake relay runs")
				return
			}
			require.ErrorIs(t, err, ErrBudgetReadUnavailable)
			var submitError *BudgetSubmitError
			require.ErrorAs(t, err, &submitError)
			require.False(t, submitError.SubmissionStarted)
			require.Zero(t, backend.sent.Load())
			require.NoError(t, budgetHoldPresent(fixture.n.db, p.ChainID, p.Owner, p.Wallet))
		})
	}
}

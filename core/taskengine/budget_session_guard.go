package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type budgetSessionFlight struct {
	Hash  common.Hash `json:"hash"`
	Nonce string      `json:"nonce"`
	Block uint64      `json:"block"`
}

func budgetFlightPrefix(chain int64, owner, wallet common.Address) []byte {
	return []byte(fmt.Sprintf("budgetflight:%d:%s:%s:", chain, strings.ToLower(owner.Hex()), strings.ToLower(wallet.Hex())))
}
func budgetWatermarkKey(chain int64, owner, wallet common.Address) []byte {
	return append(budgetFlightPrefix(chain, owner, wallet), "settled"...)
}
func (n *Engine) guardBudgetSessionSend(ctx context.Context, chain int64, owner, wallet common.Address) (*preset.SessionSendLease, error) {
	release, e := n.budgetSendLocks.acquire(ctx, budgetGateKey(chain, owner, wallet))
	if e != nil {
		return nil, e
	}
	lock := sessionAuthorityLock(chain, owner, wallet)
	lock.Lock()
	e = n.reconcileBudgetWalletLocked(ctx, chain, owner, wallet)
	if e == nil {
		e = budgetHoldPresent(n.db, chain, owner, wallet)
	}
	if e == nil {
		e = n.checkBudgetAuthorities(ctx, chain, owner, wallet)
	}
	lock.Unlock()
	if e != nil {
		release()
		return nil, e
	}
	flights := map[string]budgetSessionFlight{}
	return &preset.SessionSendLease{Release: release, BeforeBroadcast: func(_ context.Context, op *userop.UserOperationV07) error {
		if op == nil || op.Nonce == nil {
			return fmt.Errorf("invalid session operation")
		}
		hash, e := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(chain))
		if e != nil {
			return e
		}
		f := budgetSessionFlight{Hash: hash, Nonce: op.Nonce.String()}
		raw, e := json.Marshal(f)
		if e != nil {
			return e
		}
		key := append(budgetFlightPrefix(chain, owner, wallet), hash.Hex()...)
		if e = n.db.Set(key, raw); e != nil {
			return e
		}
		flights[string(key)] = f
		return nil
	}, Settled: func(_ context.Context, receipt *types.Receipt) error {
		if receipt == nil || receipt.BlockNumber == nil {
			return fmt.Errorf("missing session inclusion evidence")
		}
		raw, e := json.Marshal(budgetSessionFlight{Block: receipt.BlockNumber.Uint64()})
		if e != nil {
			return e
		}
		// A lower-nonce retry can settle while an earlier higher-nonce signature
		// remains executable. Only an included operation proves consumption;
		// receipt arrival is not settlement of every broadcast in this lease.
		deletes := consumedBudgetFlightKeys(receipt, wallet, flights)
		return atomicBudgetWrite(n.db, map[string][]byte{string(budgetWatermarkKey(chain, owner, wallet)): raw}, deletes...)
	}}, nil
}

func consumedBudgetFlightKeys(receipt *types.Receipt, wallet common.Address, flights map[string]budgetSessionFlight) [][]byte {
	byHash := make(map[common.Hash]budgetSessionFlight, len(flights))
	for _, flight := range flights {
		byHash[flight.Hash] = flight
	}
	consumed := map[string]*big.Int{}
	for _, log := range receipt.Logs {
		if !budgetSessionInclusionLog(receipt, log, wallet) {
			continue
		}
		flight, ok := byHash[log.Topics[1]]
		nonce := new(big.Int).SetBytes(log.Data[:32])
		if !ok || nonce.String() != flight.Nonce {
			continue
		}
		key := new(big.Int).Rsh(new(big.Int).Set(nonce), 64).String()
		if previous := consumed[key]; previous == nil || nonce.Cmp(previous) > 0 {
			consumed[key] = nonce
		}
	}
	var deletes [][]byte
	for key, flight := range flights {
		nonce, ok := new(big.Int).SetString(flight.Nonce, 10)
		if !ok {
			continue
		}
		nonceKey := new(big.Int).Rsh(new(big.Int).Set(nonce), 64).String()
		if confirmed := consumed[nonceKey]; confirmed != nil && nonce.Cmp(confirmed) <= 0 {
			deletes = append(deletes, []byte(key))
		}
	}
	return deletes
}

func budgetSessionInclusionLog(receipt *types.Receipt, log *types.Log, wallet common.Address) bool {
	if log == nil || log.Removed || log.Address != preset.EntryPointV07() || len(log.Topics) != 4 || len(log.Data) != 128 {
		return false
	}
	return log.Topics[0] == budgetUserOpEvent && log.Topics[2] == common.BytesToHash(wallet.Bytes()) &&
		log.BlockHash == receipt.BlockHash && log.BlockNumber == receipt.BlockNumber.Uint64()
}

func (n *Engine) checkBudgetSessionFlights(ctx context.Context, p *BudgetPolicyPreparation, v budgetChainView) error {
	rows, e := n.db.GetByPrefix(budgetFlightPrefix(p.ChainID, p.Owner, p.Wallet))
	if e != nil {
		return e
	}
	for _, row := range rows {
		var f budgetSessionFlight
		if e = json.Unmarshal(row.Value, &f); e != nil {
			return fmt.Errorf("%w: unreadable session flight", ErrBudgetReadUnavailable)
		}
		if f.Block > v.Block().Number {
			return fmt.Errorf("%w: session receipt is newer than the read snapshot", ErrBudgetReadUnavailable)
		}
		if f.Nonce == "" {
			continue
		}
		full, ok := new(big.Int).SetString(f.Nonce, 10)
		if !ok {
			return fmt.Errorf("invalid session flight nonce")
		}
		key := new(big.Int).Rsh(new(big.Int).Set(full), 64)
		ep := preset.EntryPointV07()
		data := append(crypto.Keccak256([]byte("getNonce(address,uint192)"))[:4], common.LeftPadBytes(p.Wallet.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(key.Bytes(), 32)...)
		out, e := v.CallContract(ctx, ethereum.CallMsg{To: &ep, Data: data}, nil)
		if e != nil {
			return fmt.Errorf("%w: session flight nonce unavailable: %w", ErrBudgetReadUnavailable, e)
		}
		if len(out) != 32 {
			return fmt.Errorf("%w: session flight nonce unavailable", ErrBudgetReadUnavailable)
		}
		if new(big.Int).SetBytes(out).Cmp(full) <= 0 {
			return budgetChanging("A session operation is still in flight. Wait for it to settle before changing the budget.")
		}
	}
	return nil
}
func (n *Engine) budgetWorkflowLease(ctx context.Context, task *model.Workflow) (func(), error) {
	if task == nil || task.Task == nil {
		return func() {}, nil
	}
	settings := task.InputVariables["settings"].GetStructValue()
	runner := task.SmartWalletAddress
	if settings != nil && settings.Fields["runner"].GetStringValue() != "" {
		runner = settings.Fields["runner"].GetStringValue()
	}
	if !common.IsHexAddress(runner) || !common.IsHexAddress(task.Owner) {
		return func() {}, nil
	}
	owner, wallet := common.HexToAddress(task.Owner), common.HexToAddress(runner)
	chains := billableExecutionChainIDs(task)
	sort.Slice(chains, func(i, j int) bool { return chains[i] < chains[j] })
	releases := []func(){}
	unlock := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, chain := range chains {
		release, e := n.budgetWorkflowLocks.acquire(ctx, budgetGateKey(chain, owner, wallet))
		if e != nil {
			unlock()
			return nil, e
		}
		releases = append(releases, release)
		if e = n.ReconcileBudgetChanges(ctx, chain, owner, wallet); e != nil {
			unlock()
			return nil, budgetChanging("Budget reconciliation is unavailable; this run will wait.")
		}
		if e = budgetHoldPresent(n.db, chain, owner, wallet); e != nil {
			unlock()
			return nil, e
		}
		if e = n.checkBudgetAuthorities(ctx, chain, owner, wallet); e != nil {
			unlock()
			var refusal *BudgetPolicyError
			if errors.As(e, &refusal) && refusal.Code == BudgetAuthorityUnknownCode {
				return nil, budgetChanging(refusal.Detail + " Ava's runs for this wallet are paused.")
			}
			return nil, budgetChanging(e.Error())
		}
	}
	return unlock, nil
}

func (n *Engine) checkBudgetAuthorities(ctx context.Context, chain int64, owner, wallet common.Address) error {
	// Called with the runner's send/workflow gate held. Non-budget wallets
	// keep their existing path and require no additional network reads.
	p, err := activeSessionPolicyLocked(n.db, chain, owner, wallet)
	if err != nil || !isWalletBudget(p) {
		return err
	}
	v, err := n.budgetBackend().View(ctx, chain, nil)
	if err != nil {
		return fmt.Errorf("%w: authority snapshot", ErrBudgetReadUnavailable)
	}
	defer v.Close()
	return v.CheckAuthorities(ctx, owner, wallet)
}

// Queue workers recognize this delay without recording a failed execution or
// consuming its execution count. The queue record itself remains durable.
func (e *BudgetPolicyError) RetryAfter() time.Duration {
	if e.Code == BudgetChangingCode {
		return 5 * time.Second
	}
	return 0
}

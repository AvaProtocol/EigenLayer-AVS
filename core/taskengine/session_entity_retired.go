package taskengine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
)

// A retired registration without a policy row must not become reusable after
// owner removal. In particular, empty-hook cleanup can leave module storage,
// and a direct install may never have consumed a deferred-action nonce.
func retiredSessionEntityKey(chain int64, wallet common.Address) []byte {
	return []byte(fmt.Sprintf("retiredsessionentity:%d:%s", chain, strings.ToLower(wallet.Hex())))
}

func retiredSessionEntityFloor(db storage.Storage, chain int64, wallet common.Address) (uint32, error) {
	raw, err := db.GetKey(retiredSessionEntityKey(chain, wallet))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading retired session entity reservation: %w", err)
	}
	if len(raw) != 4 {
		return 0, fmt.Errorf("invalid retired session entity reservation")
	}
	return binary.BigEndian.Uint32(raw), nil
}

func (v *pinnedBudgetView) reserveRetiredSessionEntity(ctx context.Context, wallet common.Address, entity uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Same last-acquired per-wallet lock as inventory writes. No other lock
	// is acquired while held; simultaneous checks cannot regress the floor.
	release, err := v.n.budgetAuthorityLocks.acquire(ctx, budgetGateKey(v.chainID, common.Address{}, wallet))
	if err != nil {
		return err
	}
	defer release()
	floor, err := retiredSessionEntityFloor(v.n.db, v.chainID, wallet)
	if err != nil || floor >= entity {
		return err
	}
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], entity)
	return v.n.db.Set(retiredSessionEntityKey(v.chainID, wallet), encoded[:])
}

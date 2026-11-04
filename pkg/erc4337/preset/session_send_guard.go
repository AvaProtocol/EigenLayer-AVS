package preset

import (
	"context"
	"sync"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// A lease covers the complete send/confirmation window. Every physical send,
// including a gas/nonce retry, records its exact hash before the network call.
type SessionSendLease struct {
	BeforeBroadcast func(context.Context, *userop.UserOperationV07) error
	Settled         func(context.Context, *types.Receipt) error
	Release         func()
}
type SessionSendGuard func(context.Context, int64, common.Address, common.Address) (*SessionSendLease, error)

var sessionSendGuardMu sync.RWMutex
var sessionSendGuard SessionSendGuard

type sessionSendLeaseKey struct{}

func SetSessionSendGuard(g SessionSendGuard) {
	sessionSendGuardMu.Lock()
	defer sessionSendGuardMu.Unlock()
	sessionSendGuard = g
}
func acquireSessionSend(ctx context.Context, chain int64, owner, wallet common.Address) (*SessionSendLease, error) {
	sessionSendGuardMu.RLock()
	guard := sessionSendGuard
	sessionSendGuardMu.RUnlock()
	if guard == nil {
		return &SessionSendLease{Release: func() {}}, nil
	}
	return guard(ctx, chain, owner, wallet)
}
func beforeSessionBroadcast(ctx context.Context, op *userop.UserOperationV07) error {
	lease, _ := ctx.Value(sessionSendLeaseKey{}).(*SessionSendLease)
	if lease != nil && lease.BeforeBroadcast != nil {
		return lease.BeforeBroadcast(ctx, op)
	}
	return nil
}

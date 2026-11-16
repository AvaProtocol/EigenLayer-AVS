package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (n *Engine) WalletInventoryLimit() int {
	limit := walletinventory.DefaultLimit
	if n.config != nil && n.config.WalletInventory != nil {
		limit = n.config.WalletInventory.MaxActiveWalletsPerOwner
	} else if n.smartWalletConfig != nil && n.smartWalletConfig.MaxWalletsPerOwner > 0 {
		limit = n.smartWalletConfig.MaxWalletsPerOwner
	}
	if limit <= 0 {
		limit = walletinventory.DefaultLimit
	}
	if limit > config.HardMaxWalletsPerOwner {
		limit = config.HardMaxWalletsPerOwner
	}
	return limit
}
func inventoryStatus(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, walletinventory.ErrLimit):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, walletinventory.ErrConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, walletinventory.ErrKeyReused):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, badger.ErrKeyNotFound):
		return status.Error(codes.NotFound, "Wallet not found")
	default:
		return err
	}
}
func (n *Engine) deriveInventoryAddress(ctx context.Context, chainID int64, owner common.Address, salt *big.Int) (common.Address, error) {
	if rpcConn == nil {
		return mockDerivedSenderAddress(owner, aa.MAv2FactoryAddress(), salt), nil
	}
	if reader := GetChainStateReaderForChain(uint64(chainID)); reader != nil {
		return reader.GetSmartWalletAddress(ctx, owner, aa.MAv2FactoryAddress(), salt)
	}
	sw := n.ResolveSmartWalletConfig(chainID)
	if sw == nil || sw.EthRpcUrl == "" {
		return common.Address{}, fmt.Errorf("no smart wallet RPC for chain %d", chainID)
	}
	conn, err := ethclient.DialContext(ctx, sw.EthRpcUrl)
	if err != nil {
		return common.Address{}, err
	}
	defer conn.Close()
	address, err := aa.GetSenderAddressMAv2WithContext(ctx, conn, owner, salt)
	if err != nil {
		return common.Address{}, err
	}
	if address == nil || *address == (common.Address{}) {
		return common.Address{}, fmt.Errorf("factory returned zero address")
	}
	return *address, nil
}
func (n *Engine) walletResponse(w *model.SmartWallet) *avsproto.GetWalletResp {
	result := &avsproto.GetWalletResp{Address: w.Address.Hex(), IsHidden: w.IsHidden}
	if w.Salt != nil {
		result.Salt = w.Salt.String()
	}
	if w.Factory != nil {
		result.FactoryAddress = w.Factory.Hex()
	}
	stat, err := NewStatService(n.db).GetTaskCount(w)
	if err == nil {
		result.TotalTaskCount = stat.Total
		result.EnabledTaskCount = stat.Enabled
		result.CompletedTaskCount = stat.Completed
		result.FailedTaskCount = stat.Failed
		result.DisabledTaskCount = stat.Disabled
	}
	return result
}

// InventoryWallets returns one owner-wide derived inventory plus any designated
// EOA on the requested runtime network. Its usage snapshot excludes that EOA.
func (n *Engine) InventoryWallets(user *model.User, payload *avsproto.ListWalletReq) ([]*model.SmartWallet, walletinventory.Usage, error) {
	if user == nil {
		return nil, walletinventory.Usage{}, status.Error(codes.InvalidArgument, "authenticated owner required")
	}
	all, usage, err := walletinventory.Snapshot(n.db, user.Address, n.WalletInventoryLimit())
	if err != nil {
		return nil, usage, err
	}
	if len(all) == 0 && !usage.HasHistory {
		// Bootstrap only a genuinely new global inventory. Never recreate salt 0
		// just because the caller selected a network without a runtime row.
		_, err = n.GetWalletWithContext(context.Background(), user, &avsproto.GetWalletReq{Salt: "0"})
		if err != nil {
			return nil, usage, err
		}
		all, usage, err = walletinventory.Snapshot(n.db, user.Address, n.WalletInventoryLimit())
		if err != nil {
			return nil, usage, err
		}
	}
	rows, err := n.db.GetByPrefix(WalletByOwnerPrefix(n.ResolveUserChainID(user), user.Address))
	if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
		return nil, usage, err
	}
	for _, row := range rows {
		var w model.SmartWallet
		if err = w.FromStorageData(row.Value); err != nil {
			return nil, usage, err
		}
		if w.IsEOA7702() {
			all = append(all, &w)
		}
	}
	result := make([]*model.SmartWallet, 0, len(all))
	for _, w := range all {
		if w.Retired || w.StaleDerivation {
			continue
		}
		if salt := payload.GetSalt(); salt != "" && (w.Salt == nil || w.Salt.String() != salt) {
			continue
		}
		if factory := payload.GetFactoryAddress(); factory != "" && (w.Factory == nil || !strings.EqualFold(w.Factory.Hex(), factory)) {
			continue
		}
		result = append(result, w)
	}
	return result, usage, nil
}
func (n *Engine) AllocateWallet(ctx context.Context, user *model.User, key, profile string) (*model.SmartWallet, error) {
	if user == nil {
		return nil, status.Error(codes.Unauthenticated, "owner JWT required")
	}
	chainID := n.ResolveUserChainID(user)
	return walletinventory.Allocate(ctx, n.db, chainID, user.Address, key, profile, n.WalletInventoryLimit(), func(salt *big.Int) (*model.SmartWallet, error) {
		address, err := n.deriveInventoryAddress(ctx, chainID, user.Address, salt)
		if err != nil {
			return nil, err
		}
		factory := aa.MAv2FactoryAddress()
		owner := user.Address
		return &model.SmartWallet{Owner: &owner, Address: &address, Factory: &factory, Salt: salt}, nil
	})
}

// materializeInventoryRunner is called per write/transfer node before local
// policy or sponsorship work. Inventory's dedicated lock is a leaf even when
// owner preparation already holds an execution shard during simulation.
func (v *VM) materializeInventoryRunner(chainID int64) error {
	if v.db == nil || v.task == nil || v.task.Task == nil || v.task.SmartWalletAddress == "" {
		return nil
	}
	_, err := walletinventory.Get(v.db, v.TaskOwner, v.task.SmartWalletAddress)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil
	} // legacy validation remains authoritative
	if err != nil {
		return err
	}
	_, err = walletinventory.Materialize(v.db, chainID, v.TaskOwner, v.task.SmartWalletAddress)
	return err
}

package taskengine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

type StatService struct {
	db storage.Storage
	// chainIDs is the list of chains the service should scan when counting
	// per-wallet tasks. Empty falls back to the legacy single-chain key
	// prefix for backward compat with older callers and tests.
	chainIDs []int64
}

// NewStatService constructs a StatService that scans the chain_id=0 bucket.
// Useful for tests and tools that lack an Engine reference — chain_id=0
// matches what the engine writes when SmartWallet.ChainID is unset.
// Production callers should use NewStatServiceWithChains.
func NewStatService(db storage.Storage) *StatService {
	return &StatService{
		db:       db,
		chainIDs: []int64{0},
	}
}

// NewStatServiceWithChains constructs a StatService that scans the given
// chains when counting per-wallet tasks. Pass the aggregator's knownChainIDs
// so chain-scoped storage is enumerated.
func NewStatServiceWithChains(db storage.Storage, chainIDs []int64) *StatService {
	return &StatService{
		db:       db,
		chainIDs: chainIDs,
	}
}

func (svc *StatService) GetTaskCount(smartWalletAddress *model.SmartWallet) (*model.SmartWalletTaskStat, error) {
	stat := &model.SmartWalletTaskStat{}

	// Build per-chain prefixes; fall back to legacy single-key prefix when
	// no chain context was supplied (older callers / tests).
	var items []*storage.KeyValueItem
	if len(svc.chainIDs) == 0 {
		prefix := SmartWalletTaskStoragePrefix(*smartWalletAddress.Owner, *smartWalletAddress.Address)
		chunk, err := svc.db.GetByPrefix(prefix)
		if err != nil {
			return stat, err
		}
		items = chunk
	} else {
		for _, chainID := range svc.chainIDs {
			prefix := []byte(fmt.Sprintf("u:%d:%s:%s",
				chainID,
				strings.ToLower(smartWalletAddress.Owner.Hex()),
				strings.ToLower(smartWalletAddress.Address.Hex())))
			chunk, err := svc.db.GetByPrefix(prefix)
			if err != nil {
				return stat, err
			}
			items = append(items, chunk...)
		}
	}

	for _, item := range items {
		taskStatus, _ := strconv.ParseInt(string(item.Value), 10, 32)
		stat.Total += 1
		switch avsproto.TaskStatus(taskStatus) {
		case avsproto.TaskStatus_Enabled:
			stat.Enabled += 1
		case avsproto.TaskStatus_Completed:
			stat.Completed += 1
		case avsproto.TaskStatus_Failed:
			stat.Failed += 1
		case avsproto.TaskStatus_Disabled:
			stat.Disabled += 1
		}
	}

	return stat, nil
}

package taskengine

import (
	"strconv"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

type StatService struct {
	db storage.Storage
}

func NewStatService(db storage.Storage) *StatService {
	return &StatService{
		db: db,
	}
}

func (svc *StatService) GetTaskCount(smartWalletAddress *model.SmartWallet) (*model.SmartWalletTaskStat, error) {
	stat := &model.SmartWalletTaskStat{}

	prefix := SmartWalletTaskStoragePrefix(*smartWalletAddress.Owner, *smartWalletAddress.Address)
	items, err := svc.db.GetByPrefix(prefix)
	if err != nil {
		return stat, err
	}

	for _, item := range items {
		taskStatus, _ := strconv.ParseInt(string(item.Value), 10, 32)
		stat.Total += 1
		switch avsproto.TaskStatus(taskStatus) {
		case avsproto.TaskStatus_Active:
			stat.Active += 1
		case avsproto.TaskStatus_Completed:
			stat.Completed += 1
		case avsproto.TaskStatus_Failed:
			stat.Failed += 1
		case avsproto.TaskStatus_Inactive:
			stat.Inactive += 1
		}
	}

	return stat, nil
}

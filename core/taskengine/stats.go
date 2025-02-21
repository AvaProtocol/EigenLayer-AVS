package taskengine

import (
	"strconv"

	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
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
		case avsproto.TaskStatus_Canceled:
			stat.Canceled += 1
		}
	}

	return stat, nil
}


func (svc *StatService) GetTaskCountByOwner(owner common.Address) (int64, error) {
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
		case avsproto.TaskStatus_Canceled:
			stat.Canceled += 1
		}
	}

	return stat, nil
}

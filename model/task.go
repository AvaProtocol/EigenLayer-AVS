package model

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"

	avsproto "github.com/OAK-Foundation/oak-avs/protobuf"
)

type TaskType int
type TriggerType int
type ScheduleType string

const (
	ETHTransferType       TaskType = 1
	ContractExecutionType          = 2
)

const (
	TimeTriggerType         TriggerType = 1
	ContractCallTriggerType TriggerType = 2
)

type TimeTrigger struct {
	Fixed []int64 `json:"fixed,omitempty"`
	Cron  string  `json:"cron,omitempty"`
}

type ContractSignalTrigger struct {
}

type Trigger struct {
	Type TriggerType `json:"type"`

	TimeTrigger           *TimeTrigger           `json:"time_trigger,omitempty"`
	ContractSignalTrigger *ContractSignalTrigger `json:"contract_call_trigger,omitempty"`
}

type EthTransferPayload struct {
	Destination string `json:"destination"`
	Amount      string `json:"amount"`
}

type ContractExecutionPayload struct {
	Destination string `json:"destination"`
	Amount      string `json:"amount"`
}

type TaskPayload struct {
	Type              TaskType                  `json:"type"`
	EthTransfer       *EthTransferPayload       `json:"eth_transfer,omitempty"`
	ContractExecution *ContractExecutionPayload `json:"contract_execution,omitempty"`
}

type Task struct {
	// a unique id identifi this task in entire system
	ID string

	// owner address in hex
	Owner string

	// The smartwallet that deploy this, it is important to store this because
	// there are maybe more than one AA per owner
	SmartWalletAddress string

	// trigger defined whether the task can be executed
	// trigger can be time based, price based, or contract call based
	Trigger Trigger

	// the actual call will be executed in ethereum, it can be a simple transfer
	// a method call, or a batch call through multicall contract
	Payload TaskPayload
}

// Generate a sorted uuid
func GenerateTaskID() string {
	taskId := ulid.Make()

	return taskId.String()
}

// Populate a task structure from proto payload
func NewTaskFromProtobuf(body *avsproto.CreateTaskReq) (*Task, error) {
	if body == nil {
		return nil, nil
	}

	owner := common.HexToAddress(body.Owner)
	aaAddress := common.HexToAddress(body.SmartWalletAddress)

	t := &Task{
		ID: GenerateTaskID(),

		// convert back to string with EIP55-compliant
		Owner:              owner.Hex(),
		SmartWalletAddress: aaAddress.Hex(),

		Trigger: Trigger{},
		Payload: TaskPayload{},
	}

	if body.GetTrigger().GetType() == avsproto.TriggerType_TimeTrigger {
		t.Trigger.Type = TimeTriggerType
		if schedule := body.GetTrigger().GetSchedule(); schedule != nil {
			t.Trigger.TimeTrigger = &TimeTrigger{
				Fixed: schedule.Fixed,
				Cron:  schedule.Cron,
			}
		}
	} else {
		// TODO: Contract trigger
	}

	return t, nil
}

// Return a compact json ready to persist to storage
func (t *Task) ToJSON() ([]byte, error) {
	return json.Marshal(t)
}

package model

import (
	"encoding/json"
	"fmt"

	"github.com/oklog/ulid/v2"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type TaskType int
type TriggerType int
type ScheduleType string

type TaskStatusType string

const (
	ETHTransferType       TaskType = 0
	ContractExecutionType TaskType = 1
)

const (
	TaskStatusActive TaskStatusType = "active"

	TimeTriggerType         TriggerType = 1
	ContractCallTriggerType TriggerType = 2
)

// Convert protocolbuf task type to our task type
func TaskTypeFromProtobuf(t avsproto.TaskType) TaskType {
	switch t {
	case avsproto.TaskType_ContractExecutionTask:
		return ContractExecutionType
	default:
		return ETHTransferType
	}
}

func (t TaskType) ToProtoBuf() avsproto.TaskType {
	switch t {
	case ContractExecutionType:
		return avsproto.TaskType_ContractExecutionTask
	default:
		return avsproto.TaskType_ETHTransferTask
	}

}

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

type ETHTransferPayload struct {
	Destination string `json:"destination"`
	Amount      string `json:"amount"`
}

type ContractExecutionPayload struct {
	ContractAddress string `json:"contract_address"`
	Method          string `json:"method"`
	EncodedParams   string `json:"encoded_params"`
}

type TaskBody struct {
	ETHTransfer       *ETHTransferPayload       `json:"eth_transfer,omitempty"`
	ContractExecution *ContractExecutionPayload `json:"contract_execution,omitempty"`
}

type Task struct {
	// a unique id identifi this task in entire system
	ID string `json:"id"`

	// owner address in hex
	Owner string `json:"owner"`

	// The smartwallet that deploy this, it is important to store this because
	// there are maybe more than one AA per owner
	SmartAccountAddress string `json:"smart_account_address"`

	Type TaskType `json:"type"`

	// trigger defined whether the task can be executed
	// trigger can be time based, price based, or contract call based
	Trigger Trigger `json:"trigger"`

	// the actual call will be executed in ethereum, it can be a simple transfer
	// a method call, or a batch call through multicall contract
	Body TaskBody `json:"body"`

	Memo      string `json:"memo"`
	ExpiredAt int64  `json:"expired_at"`
}

// Generate a sorted uuid
func GenerateTaskID() string {
	taskId := ulid.Make()

	return taskId.String()
}

// Populate a task structure from proto payload
func NewTaskFromProtobuf(user *User, body *avsproto.CreateTaskReq) (*Task, error) {
	if body == nil {
		return nil, nil
	}

	owner := user.Address
	aaAddress := user.SmartAccountAddress

	if aaAddress == nil {
		return nil, fmt.Errorf("Cannot get acount abstraction wallet")
	}

	//TODO: Validate

	t := &Task{
		ID: GenerateTaskID(),

		// convert back to string with EIP55-compliant
		Owner:               owner.Hex(),
		SmartAccountAddress: aaAddress.Hex(),

		Trigger:   Trigger{},
		Body:      TaskBody{},
		Type:      TaskTypeFromProtobuf(body.TaskType),
		Memo:      body.Memo,
		ExpiredAt: body.ExpiredAt,
	}

	if body.Body.GetEthTransfer() != nil {
		t.Body.ETHTransfer = &ETHTransferPayload{
			Destination: body.Body.EthTransfer.Destination,
			Amount:      body.Body.EthTransfer.Amount,
		}
	} else if body.Body.GetContractExecution() != nil {
		t.Body.ContractExecution = &ContractExecutionPayload{
			ContractAddress: body.Body.ContractExecution.ContractAddress,
			Method:          body.Body.ContractExecution.Method,
			EncodedParams:   body.Body.ContractExecution.EncodedParams,
		}
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

// Convert to protobuf
func (t *Task) ToProtoBuf() (*avsproto.Task, error) {
	protoTask := avsproto.Task{
		Owner:               t.Owner,
		SmartAccountAddress: t.SmartAccountAddress,

		Id: &avsproto.UUID{
			Bytes: t.ID,
		},
		TaskType: t.Type.ToProtoBuf(),
		Trigger:  &avsproto.TaskTrigger{},
		Body:     &avsproto.TaskBody{},

		ExpiredAt: t.ExpiredAt,
		Memo:      t.Memo,
	}

	if t.Body.ETHTransfer != nil {
		protoTask.Body.EthTransfer = &avsproto.ETHTransfer{
			Destination: t.Body.ETHTransfer.Destination,
			Amount:      t.Body.ETHTransfer.Amount,
		}
	}

	if t.Trigger.Type == TimeTriggerType {
		protoTask.Trigger.Type = avsproto.TriggerType_TimeTrigger
		protoTask.Trigger.Schedule = &avsproto.TaskTrigger_TimeCondition{
			Fixed: t.Trigger.TimeTrigger.Fixed,
			Cron:  t.Trigger.TimeTrigger.Cron,
		}
	} else {
		// TODO: Contract trigger
	}

	return &protoTask, nil
}

func (t *Task) FromStorageData(body []byte) error {
	err := json.Unmarshal(body, t)

	return err
}

// Generate a global unique key for the task in our system
func (t *Task) Key() []byte {
	return []byte(fmt.Sprintf("%s:%s", t.Owner, t.ID))
}

// Given a task key generated from Key(), extract the ID part
func TaskKeyToId(key []byte) []byte {
	// the first 43 bytes is owner address
	return key[43:]
}

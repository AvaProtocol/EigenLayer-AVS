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

	TimeTriggerType          TriggerType = 1
	ContractQueryTriggerType TriggerType = 2
	ExpressionTriggerType    TriggerType = 3
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

func (t TriggerType) ToProtoBuf() avsproto.TriggerType {
	switch t {
	case TimeTriggerType:
		return avsproto.TriggerType_TimeTrigger
	case ContractQueryTriggerType:
		return avsproto.TriggerType_ContractQueryTrigger
	case ExpressionTriggerType:
		return avsproto.TriggerType_ExpressionTrigger
	}
	return avsproto.TriggerType_TimeTrigger
}

type TimeTrigger struct {
	Fixed []int64 `json:"fixed,omitempty"`
	Cron  string  `json:"cron,omitempty"`
}

type ContractQueryTrigger struct {
	ContractAddress string `json:"contract_address"`
	CallData        string `json:"calldata"`
}

type ExpressionTrigger struct {
	Expression string `json:"expression,omitempty"`
}

type Trigger struct {
	Type TriggerType `json:"type"`

	TimeTrigger          *TimeTrigger          `json:"time_trigger,omitempty"`
	ContractQueryTrigger *ContractQueryTrigger `json:"contract_query_trigger,omitempty"`
	ExpressionTrigger    *ExpressionTrigger    `json:"expression_trigger,omitempty"`
}

type ETHTransferPayload struct {
	Destination string `json:"destination"`
	Amount      string `json:"amount"`
}

type ContractExecutionPayload struct {
	ContractAddress string `json:"contract_address"`
	CallData        string `json:"calldata"`
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
	ExpiredAt int64  `json:"expired_at,omitempty"`
	StartAt   int64  `json:"start_at,omitempty"`
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
		StartAt:   body.StartAt,
	}

	if body.Body.GetEthTransfer() != nil {
		t.Body.ETHTransfer = &ETHTransferPayload{
			Destination: body.Body.EthTransfer.Destination,
			Amount:      body.Body.EthTransfer.Amount,
		}
	} else if body.Body.GetContractExecution() != nil {
		t.Body.ContractExecution = &ContractExecutionPayload{
			ContractAddress: body.Body.ContractExecution.ContractAddress,
			CallData:        body.Body.ContractExecution.Calldata,
		}
	}

	switch body.GetTrigger().GetTriggerType() {
	case avsproto.TriggerType_TimeTrigger:
		t.Trigger.Type = TimeTriggerType
		if schedule := body.GetTrigger().GetSchedule(); schedule != nil {
			t.Trigger.TimeTrigger = &TimeTrigger{
				Fixed: schedule.Fixed,
				Cron:  schedule.Cron,
			}
		}

	case avsproto.TriggerType_ExpressionTrigger:
		t.Trigger.Type = ExpressionTriggerType
		if expression := body.GetTrigger().GetExpression(); expression != nil {
			t.Trigger.ExpressionTrigger = &ExpressionTrigger{
				Expression: expression.Expression,
			}
		}

	case avsproto.TriggerType_ContractQueryTrigger:
		t.Trigger.Type = ContractQueryTriggerType
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

	switch t.Trigger.Type {
	case TimeTriggerType:
		protoTask.Trigger.TriggerType = avsproto.TriggerType_TimeTrigger
		protoTask.Trigger.Schedule = &avsproto.TimeCondition{
			Fixed: t.Trigger.TimeTrigger.Fixed,
			Cron:  t.Trigger.TimeTrigger.Cron,
		}
	case ExpressionTriggerType:
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

func (t *TimeTrigger) ToProtoBuf() *avsproto.TimeCondition {
	v := avsproto.TimeCondition{
		Fixed: t.Fixed,
		Cron:  t.Cron,
	}

	return &v
}

func (t *ContractQueryTrigger) ToProtoBuf() *avsproto.ContractQueryCondition {
	return &avsproto.ContractQueryCondition{
		ContractAddress: t.ContractAddress,
		Callmsg:         t.CallData,
	}
}

func (t *ExpressionTrigger) ToProtoBuf() *avsproto.ExpressionCondition {
	v := avsproto.ExpressionCondition{
		Expression: t.Expression,
	}

	return &v
}

func (t *Trigger) ToProtoBuf() *avsproto.TaskTrigger {
	v := avsproto.TaskTrigger{
		TriggerType: t.Type.ToProtoBuf(),
	}

	if t.TimeTrigger != nil {
		v.Schedule = t.TimeTrigger.ToProtoBuf()
	}

	if t.ContractQueryTrigger != nil {
		v.ContractQuery = t.ContractQueryTrigger.ToProtoBuf()
	}

	if t.ExpressionTrigger != nil {
		v.Expression = t.ExpressionTrigger.ToProtoBuf()
	}

	return &v
}

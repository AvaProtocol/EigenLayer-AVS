package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type TaskType int
type TriggerType int
type ScheduleType string

const (
	ETHTransferType       TaskType = 0
	ContractExecutionType TaskType = 1
)

const (
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

type TaskAction struct {
	ETHTransfer       *ETHTransferPayload       `json:"eth_transfer,omitempty"`
	ContractExecution *ContractExecutionPayload `json:"contract_execution,omitempty"`
}

type Execution struct {
	Epoch      int64  `json:"epoch"`
	UserOpHash string `json:"userop_hash,omitempty"`
	Error      string `json:"failed_reason,omitempty"`
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
	Action TaskAction `json:"Action"`

	Memo        string `json:"memo"`
	ExpiredAt   int64  `json:"expired_at,omitempty"`
	StartAt     int64  `json:"start_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`

	Status     avsproto.TaskStatus `json:"status"`
	Executions []*Execution        `json:"executions,omitempty"`
}

// Generate a sorted uuid
func GenerateTaskID() string {
	taskId := ulid.Make()

	return taskId.String()
}

// Populate a task structure from proto payload
func NewTaskFromProtobuf(taskID string, user *User, body *avsproto.CreateTaskReq) (*Task, error) {
	if body == nil {
		return nil, nil
	}

	owner := user.Address
	aaAddress := user.SmartAccountAddress

	//TODO: Validate
	t := &Task{
		ID: taskID,

		// convert back to string with EIP55-compliant
		Owner:               owner.Hex(),
		SmartAccountAddress: aaAddress.Hex(),

		Trigger:   Trigger{},
		Action:    TaskAction{},
		Type:      TaskTypeFromProtobuf(body.TaskType),
		Memo:      body.Memo,
		ExpiredAt: body.ExpiredAt,
		StartAt:   body.StartAt,

		// initial state for task
		Status:     avsproto.TaskStatus_Active,
		Executions: []*Execution{},
	}

	if body.Action.GetEthTransfer() != nil {
		t.Action.ETHTransfer = &ETHTransferPayload{
			Destination: body.Action.EthTransfer.Destination,
			Amount:      body.Action.EthTransfer.Amount,
		}
	} else if body.Action.GetContractExecution() != nil {
		t.Action.ContractExecution = &ContractExecutionPayload{
			ContractAddress: body.Action.ContractExecution.ContractAddress,
			CallData:        body.Action.ContractExecution.Calldata,
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
		Action:   &avsproto.TaskAction{},

		StartAt:   t.StartAt,
		ExpiredAt: t.ExpiredAt,
		Memo:      t.Memo,

		Executions:  ExecutionsToProtoBuf(t.Executions),
		CompletedAt: t.CompletedAt,
		Status:      t.Status,
	}

	if t.Action.ETHTransfer != nil {
		protoTask.Action.EthTransfer = &avsproto.ETHTransfer{
			Destination: t.Action.ETHTransfer.Destination,
			Amount:      t.Action.ETHTransfer.Amount,
		}
	}

	if t.Action.ContractExecution != nil {
		protoTask.Action.ContractExecution = &avsproto.ContractExecution{
			ContractAddress: t.Action.ContractExecution.ContractAddress,
			Calldata:        t.Action.ContractExecution.CallData,
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
		protoTask.Trigger.TriggerType = avsproto.TriggerType_ExpressionTrigger
		protoTask.Trigger.Expression = &avsproto.ExpressionCondition{
			Expression: t.Trigger.ExpressionTrigger.Expression,
		}
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

func (t *Task) SetCompleted() {
	t.Status = avsproto.TaskStatus_Completed
	t.CompletedAt = time.Now().Unix()
}

func (t *Task) SetFailed() {
	t.Status = avsproto.TaskStatus_Failed
	t.CompletedAt = time.Now().Unix()
}

func (t *Task) SetCanceled() {
	t.Status = avsproto.TaskStatus_Canceled
	t.CompletedAt = time.Now().Unix()
}

func (t *Task) AppendExecution(epoch int64, userOpHash string, err error) {
	exc := &Execution{
		Epoch:      epoch,
		UserOpHash: userOpHash,
	}

	if err != nil {
		exc.Error = err.Error()
	}

	t.Executions = append(t.Executions, exc)
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

func ExecutionsToProtoBuf(exc []*Execution) []*avsproto.Execution {
	data := make([]*avsproto.Execution, len(exc))

	for i, v := range exc {
		data[i] = &avsproto.Execution{
			UseropHash: v.UserOpHash,
			Epoch:      v.Epoch,
			Error:      v.Error,
		}
	}

	return data
}

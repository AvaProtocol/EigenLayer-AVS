package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type Task struct {
	// a unique id identifi this task in entire system
	ID string `json:"id"`

	// owner address in hex
	Owner string `json:"owner"`

	// The smartwallet that deploy this, it is important to store this because
	// there are maybe more than one AA per owner
	SmartAccountAddress string `json:"smart_account_address"`

	// trigger defined whether the task can be executed
	// trigger can be time based, price based, or contract call based
	Trigger *avsproto.TaskTrigger `json:"trigger"`

	// the actual call will be executed in ethereum, it can be a simple transfer
	// a method call, or a batch call through multicall contract
	Nodes []*avsproto.TaskAction `json:"nodes"`

	Memo        string `json:"memo"`
	ExpiredAt   int64  `json:"expired_at,omitempty"`
	StartAt     int64  `json:"start_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`

	Status     avsproto.TaskStatus   `json:"status"`
	Executions []*avsproto.Execution `json:"executions,omitempty"`
	Repeatable bool                  `json:"repeatable,omitempty"`
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

	t := &Task{
		ID: taskID,

		// convert back to string with EIP55-compliant
		Owner:               owner.Hex(),
		SmartAccountAddress: aaAddress.Hex(),

		Trigger:   body.Trigger,
		Nodes:     body.Actions,
		Memo:      body.Memo,
		ExpiredAt: body.ExpiredAt,
		StartAt:   body.StartAt,

		// initial state for task
		Status:     avsproto.TaskStatus_Active,
		Executions: []*avsproto.Execution{},
	}

	// Validate
	if ok := t.Validate(); !ok {
		return nil, fmt.Errorf("Invalid task argument")
	}

	return t, nil
}

// Return a compact json ready to persist to storage
func (t *Task) ToJSON() ([]byte, error) {
	return json.Marshal(t)
}

// Return a compact json ready to persist to storage
func (t *Task) Validate() bool {
	return true
}

// Convert to protobuf
func (t *Task) ToProtoBuf() (*avsproto.Task, error) {
	protoTask := avsproto.Task{
		Owner:               t.Owner,
		SmartAccountAddress: t.SmartAccountAddress,

		Id: &avsproto.UUID{
			Bytes: t.ID,
		},
		Trigger: t.Trigger,
		Nodes:   t.Nodes,

		StartAt:   t.StartAt,
		ExpiredAt: t.ExpiredAt,
		Memo:      t.Memo,

		Executions:  t.Executions,
		CompletedAt: t.CompletedAt,
		Status:      t.Status,
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
	exc := &avsproto.Execution{
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

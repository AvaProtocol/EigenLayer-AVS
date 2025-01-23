package model

import (
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type Task struct {
	*avsproto.Task
}

// Generate a sorted uuid
func GenerateTaskID() string {
	taskId := ulid.Make()

	return taskId.String()
}

func NewTask() *Task {
	return &Task{
		Task: &avsproto.Task{},
	}
}

// Populate a task structure from proto payload
func NewTaskFromProtobuf(user *User, body *avsproto.CreateTaskReq) (*Task, error) {
	if body == nil {
		return nil, nil
	}

	owner := user.Address
	aaAddress := *user.SmartAccountAddress

	if body.SmartWalletAddress != "" {
		aaAddress = common.HexToAddress(body.SmartWalletAddress)
	}

	taskID := GenerateTaskID()

	if len(body.Edges) == 0 || len(body.Nodes) == 0 {
		return nil, fmt.Errorf("Missing task data")
	}

	t := &Task{
		Task: &avsproto.Task{
			Id: taskID,

			// convert back to string with EIP55-compliant
			Owner:              owner.Hex(),
			SmartWalletAddress: aaAddress.Hex(),

			Trigger:      body.Trigger,
			Nodes:        body.Nodes,
			Edges:        body.Edges,
			Name:         body.Name,
			ExpiredAt:    body.ExpiredAt,
			StartAt:      body.StartAt,
			MaxExecution: body.MaxExecution,

			// initial state for task
			Status: avsproto.TaskStatus_Active,
			//Executions: []*avsproto.Execution{},
		},
	}

	// Validate
	if ok := t.Validate(); !ok {
		return nil, fmt.Errorf("Invalid task argument")
	}

	return t, nil
}

// Return a compact json ready to persist to storage
func (t *Task) ToJSON() ([]byte, error) {
	// return json.Marshal(t)
	return protojson.Marshal(t)
}

func (t *Task) FromStorageData(body []byte) error {
	// err := json.Unmarshal(body, t)
	err := protojson.Unmarshal(body, t)
	return err
}

// Return a compact json ready to persist to storage
func (t *Task) Validate() bool {
	return true
}

func (t *Task) ToProtoBuf() (*avsproto.Task, error) {
	return t.Task, nil
}

// Generate a global unique key for the task in our system
func (t *Task) Key() []byte {
	return []byte(t.Id)
}

func (t *Task) SetCompleted() {
	t.Status = avsproto.TaskStatus_Completed
	t.CompletedAt = time.Now().Unix()
}

func (t *Task) SetActive() {
	t.Status = avsproto.TaskStatus_Active
}

func (t *Task) SetFailed() {
	t.Status = avsproto.TaskStatus_Failed
	t.CompletedAt = time.Now().Unix()
}

func (t *Task) SetCanceled() {
	t.Status = avsproto.TaskStatus_Canceled
	t.CompletedAt = time.Now().Unix()
}

// Check whether the task own by the given address
func (t *Task) OwnedBy(address common.Address) bool {
	return strings.EqualFold(t.Owner, address.Hex())
}

// A task is runable when both of these condition are matched
//  1. Its max execution has not reached
//  2. Its expiration time has not reached
func (t *Task) Runable() bool {
	// When MaxExecution is 0, it is unlimited run
	reachedMaxRun := t.MaxExecution > 0 && t.TotalExecution >= t.MaxExecution

	reachedExpiredTime := t.ExpiredAt > 0 && time.Unix(t.ExpiredAt, 0).Before(time.Now())

	return !reachedMaxRun && !reachedExpiredTime
}

// Given a task key generated from Key(), extract the ID part
func TaskKeyToId(key []byte) []byte {
	// <43-byte>:<43-byte>:
	// the first 43 bytes is owner address
	return key[86:]
}

func UlidFromTaskId(taskID string) ulid.ULID {
	return ulid.MustParse(taskID)
}

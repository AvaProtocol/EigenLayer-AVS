package model

import (
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type Task struct {
	*avsproto.Task
}

const (
	ErrEmptyNodesField = "invalid: nodes field cannot be an empty array"
	ErrEmptyEdgesField = "invalid: edges field cannot be an empty array"
)

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

	if len(body.Edges) == 0 {
		return nil, fmt.Errorf("%s", ErrEmptyEdgesField)
	}

	if len(body.Nodes) == 0 {
		return nil, fmt.Errorf("%s", ErrEmptyNodesField)
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
	if err := t.ValidateWithError(); err != nil {
		return nil, fmt.Errorf("Invalid task argument: %w", err)
	}

	return t, nil
}

// Return a compact json ready to persist to storage
func (t *Task) ToJSON() ([]byte, error) {
	// return json.Marshal(t)
	return protojson.Marshal(t.Task)
}

func (t *Task) FromStorageData(body []byte) error {
	// err := json.Unmarshal(body, t)
	err := protojson.Unmarshal(body, t.Task)
	return err
}

// Return a compact json ready to persist to storage
func (t *Task) Validate() bool {
	return t.ValidateWithError() == nil
}

// ValidateWithError returns detailed validation error messages
func (t *Task) ValidateWithError() error {
	// Validate block trigger intervals
	if t.Task.Trigger != nil {
		if blockTrigger := t.Task.Trigger.GetBlock(); blockTrigger != nil {
			config := blockTrigger.GetConfig()
			// Config must exist and have a valid interval
			if config == nil {
				return fmt.Errorf("block trigger config is required but missing")
			}
			if config.GetInterval() <= 0 {
				return fmt.Errorf("block trigger interval must be greater than 0, got %d", config.GetInterval())
			}
		}

		// Validate cron trigger
		if cronTrigger := t.Task.Trigger.GetCron(); cronTrigger != nil {
			config := cronTrigger.GetConfig()
			if config == nil {
				return fmt.Errorf("cron trigger config is required but missing")
			}
			if len(config.GetSchedules()) == 0 {
				return fmt.Errorf("cron trigger must have at least one schedule")
			}
		}

		// Validate fixed time trigger
		if fixedTimeTrigger := t.Task.Trigger.GetFixedTime(); fixedTimeTrigger != nil {
			config := fixedTimeTrigger.GetConfig()
			if config == nil {
				return fmt.Errorf("fixed time trigger config is required but missing")
			}
			if len(config.GetEpochs()) == 0 {
				return fmt.Errorf("fixed time trigger must have at least one epoch")
			}
		}

		// Validate event trigger
		if eventTrigger := t.Task.Trigger.GetEvent(); eventTrigger != nil {
			config := eventTrigger.GetConfig()
			if config == nil {
				return fmt.Errorf("event trigger config is required but missing")
			}
			if len(config.GetQueries()) == 0 {
				return fmt.Errorf("event trigger must have at least one query")
			}
		}
	}

	return nil
}

func (t *Task) ToProtoBuf() (*avsproto.Task, error) {
	return t.Task, nil
}

// Generate a global unique key for the task in our system
func (t *Task) Key() []byte {
	return []byte(t.Task.Id)
}

func (t *Task) SetCompleted() {
	t.Task.Status = avsproto.TaskStatus_Completed
	t.Task.CompletedAt = time.Now().UnixMilli()
}

func (t *Task) SetActive() {
	t.Task.Status = avsproto.TaskStatus_Active
}

func (t *Task) SetFailed() {
	t.Task.Status = avsproto.TaskStatus_Failed
	t.Task.CompletedAt = time.Now().UnixMilli()
}

func (t *Task) SetCanceled() {
	t.Task.Status = avsproto.TaskStatus_Canceled
	t.Task.CompletedAt = time.Now().UnixMilli()
}

// Check whether the task own by the given address
func (t *Task) OwnedBy(address common.Address) bool {
	return strings.EqualFold(t.Task.Owner, address.Hex())
}

// A task is runable when all of these conditions are matched
//  1. Its max execution has not reached
//  2. Its expiration time has not reached
func (t *Task) IsRunable() bool {
	// When MaxExecution is 0, it is unlimited run
	reachedMaxRun := t.Task.MaxExecution > 0 && t.Task.ExecutionCount >= t.Task.MaxExecution

	reachedExpiredTime := t.Task.ExpiredAt > 0 && time.Unix(t.Task.ExpiredAt/1000, 0).Before(time.Now())

	beforeStartTime := t.Task.StartAt > 0 && time.Now().UnixMilli() < t.Task.StartAt

	return !reachedMaxRun && !reachedExpiredTime && !beforeStartTime
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

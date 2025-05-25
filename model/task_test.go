package model

import (
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestIsRunable(t *testing.T) {
	t.Run("task with future startAt should not be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(time.Hour).UnixMilli(),
				MaxExecution:   1,
				ExecutionCount: 0,
			},
		}

		assert.False(t, task.IsRunable(), "Task with future startAt should not be runable")
	})

	t.Run("task with past startAt should be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   1,
				ExecutionCount: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with past startAt should be runable")
	})

	t.Run("task with startAt equal to current time should be runable", func(t *testing.T) {
		now := time.Now().UnixMilli()
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        now,
				MaxExecution:   1,
				ExecutionCount: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with startAt equal to current time should be runable")
	})

	t.Run("task with zero startAt should be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        0,
				MaxExecution:   1,
				ExecutionCount: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with zero startAt should be runable")
	})

	t.Run("task with maxExecution reached should not be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   1,
				ExecutionCount: 1,
			},
		}

		assert.False(t, task.IsRunable(), "Task with maxExecution reached should not be runable")
	})

	t.Run("task with expiredAt in the past should not be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(-2 * time.Hour).UnixMilli(),
				ExpiredAt:      time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   1,
				ExecutionCount: 0,
			},
		}

		assert.False(t, task.IsRunable(), "Task with expiredAt in the past should not be runable")
	})

	t.Run("task with all conditions met should be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				ExpiredAt:      time.Now().Add(time.Hour).UnixMilli(),
				MaxExecution:   1,
				ExecutionCount: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with all conditions met should be runable")
	})
}

func TestExecutionCountPersistence(t *testing.T) {
	t.Run("execution count should be incremented and persisted", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				Id:             "test-task-id",
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   5,
				ExecutionCount: 0,
				Status:         avsproto.TaskStatus_Active,
			},
		}

		task.ExecutionCount += 1
		assert.Equal(t, int64(1), task.ExecutionCount, "ExecutionCount should be incremented")

		jsonData, err := task.ToJSON()
		assert.NoError(t, err, "ToJSON should not error")
		assert.NotNil(t, jsonData, "JSON data should not be nil")

		newTask := NewTask()
		err = newTask.FromStorageData(jsonData)
		assert.NoError(t, err, "FromStorageData should not error")
		assert.Equal(t, int64(1), newTask.ExecutionCount, "ExecutionCount should be preserved after serialization/deserialization")

		protobufTask, err := newTask.ToProtoBuf()
		assert.NoError(t, err, "ToProtoBuf should not error")
		assert.Equal(t, int64(1), protobufTask.ExecutionCount, "ExecutionCount should be preserved in protobuf conversion")
	})

	t.Run("execution count should increment multiple times", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				Id:             "test-task-id-multi",
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   5,
				ExecutionCount: 0,
				Status:         avsproto.TaskStatus_Active,
			},
		}

		for i := 1; i <= 3; i++ {
			task.ExecutionCount += 1

			jsonData, err := task.ToJSON()
			assert.NoError(t, err, "ToJSON should not error")

			newTask := NewTask()
			err = newTask.FromStorageData(jsonData)
			assert.NoError(t, err, "FromStorageData should not error")
			assert.Equal(t, int64(i), newTask.ExecutionCount, "ExecutionCount should be %d after %d executions", i, i)
		}
	})

	t.Run("execution count should work with max execution limit", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				Id:             "test-task-id-max",
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   2,
				ExecutionCount: 1,
				Status:         avsproto.TaskStatus_Active,
			},
		}

		task.ExecutionCount += 1
		assert.Equal(t, int64(2), task.ExecutionCount, "ExecutionCount should reach max execution")

		if task.MaxExecution > 0 && task.ExecutionCount >= task.MaxExecution {
			task.SetCompleted()
		}

		jsonData, err := task.ToJSON()
		assert.NoError(t, err, "ToJSON should not error")

		newTask := NewTask()
		err = newTask.FromStorageData(jsonData)
		assert.NoError(t, err, "FromStorageData should not error")
		assert.Equal(t, int64(2), newTask.ExecutionCount, "ExecutionCount should be preserved when task is completed")
		assert.Equal(t, avsproto.TaskStatus_Completed, newTask.Status, "Task should be completed when max execution is reached")
	})
}

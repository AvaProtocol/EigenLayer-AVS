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
				TotalExecution: 0,
			},
		}

		assert.False(t, task.IsRunable(), "Task with future startAt should not be runable")
	})

	t.Run("task with past startAt should be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   1,
				TotalExecution: 0,
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
				TotalExecution: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with startAt equal to current time should be runable")
	})

	t.Run("task with zero startAt should be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        0,
				MaxExecution:   1,
				TotalExecution: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with zero startAt should be runable")
	})

	t.Run("task with maxExecution reached should not be runable", func(t *testing.T) {
		task := &Task{
			Task: &avsproto.Task{
				StartAt:        time.Now().Add(-time.Hour).UnixMilli(),
				MaxExecution:   1,
				TotalExecution: 1,
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
				TotalExecution: 0,
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
				TotalExecution: 0,
			},
		}

		assert.True(t, task.IsRunable(), "Task with all conditions met should be runable")
	})
}

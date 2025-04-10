package migrations

import (
	//"strconv"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"	
)

func ChangeEpochToMs(db storage.Storage) (int, error) {
	// This migration converts epoch timestamps from seconds to milliseconds
	// We need to identify keys that store epoch timestamps and convert their values

	// Our app used to use epoch time in seconds, but we switched to milliseconds per this issue https://github.com/AvaProtocol/EigenLayer-AVS/issues/191

	// Based on the context, we need to find all keys that store epoch timestamps
	// Let's define a threshold to determine if a timestamp is in seconds
	// If timestamp <= threshold, we assume it's in seconds and needs conversion
	const (
		// A reasonable threshold for distinguishing seconds vs milliseconds
		// This represents Jan/1/20250- any timestamp before this in our db is certainly in seconds
		// When a timestamp is in milliseconds, in our app context, it will be always bigger than 1704067200000
		timestampThreshold = 2521872514
	)

	// Get all execution in the database by prefix. Refer to our doc.go and schema.go for the detail
	taskKeys, err := db.ListKeys("t:*")
	if err != nil {
		return 0, err
	}
	// Now we will iterate over all task keys and check if the timestamp is in seconds
	updates := make(map[string][]byte)
	totalUpdated := 0
	for _, key := range taskKeys {
		taskRawByte, err := db.GetKey([]byte(key))
		if err != nil {
			continue
		}

		task := model.NewTask()
		if err := task.FromStorageData(taskRawByte); err != nil {
			continue
		}
		
		if task.Task.StartAt > 0 && task.Task.StartAt < timestampThreshold {
			// Convert the timestamp to milliseconds
			task.Task.StartAt = task.Task.StartAt * 1000
		}
		if task.Task.ExpiredAt > 0 && task.Task.ExpiredAt < timestampThreshold {
			// Convert the timestamp to milliseconds
			task.Task.ExpiredAt = task.Task.ExpiredAt * 1000
		}
		if task.Task.CompletedAt > 0 && task.Task.CompletedAt < timestampThreshold {
			// Convert the timestamp to milliseconds
			task.Task.CompletedAt = task.Task.CompletedAt * 1000
		}
		if task.Task.LastRanAt > 0 && task.Task.LastRanAt < timestampThreshold {
			// Convert the timestamp to milliseconds
			task.Task.LastRanAt = task.Task.LastRanAt * 1000
		}

		//	Update the task in the database
		updates[key], err = task.ToJSON()
		if err != nil {
			return 0, err
		}
		totalUpdated++
	}

	// Now we will iterate over all history run keys and check if the timestamp is in seconds
	historyExecutionKeys, err := db.ListKeys("history:*")
	if err != nil {
		return 0, err
	}
	
	for _, key := range historyExecutionKeys {
		executionRawByte, err := db.GetKey([]byte(key))
		if err != nil {
			continue
		}

		exec := avsproto.Execution{}
		err = protojson.Unmarshal(executionRawByte, &exec)
		if err != nil {
			return 0, err
		}

		// Convert the timestamp to milliseconds for the execution 
		if exec.StartAt > 0 && exec.StartAt < timestampThreshold {
			exec.StartAt = exec.StartAt * 1000
		}
		if exec.EndAt > 0 && exec.EndAt < timestampThreshold {	
			// Convert the timestamp to milliseconds
			exec.EndAt = exec.EndAt * 1000
		}

		// Convert start/end of each step to milliseconds
		for i, step := range exec.Steps {			
			if step.StartAt > 0 && step.StartAt < timestampThreshold {
				// Convert the timestamp to milliseconds
				exec.Steps[i].StartAt = step.StartAt * 1000
			}
			if step.EndAt > 0 && step.EndAt < timestampThreshold {
				// Convert the timestamp to milliseconds
				exec.Steps[i].EndAt = step.EndAt * 1000
			}
		}

		if outputData := exec.GetTransferLog(); outputData != nil {
			if outputData.BlockTimestamp > 0 && outputData.BlockTimestamp < timestampThreshold {
				outputData.BlockTimestamp = outputData.BlockTimestamp * 1000
			}
		} else if outputData := exec.GetTime(); outputData != nil {
			if outputData.Epoch > 0 && outputData.Epoch < timestampThreshold {
				outputData.Epoch = outputData.Epoch * 1000
			}
		}

		updates[key], err = protojson.Marshal(&exec)
		if err != nil {
			return 0, err
		}
		totalUpdated++
	}

	// Write all updates to the database
	if err := db.BatchWrite(updates); err != nil {
		return 0, err
	}
	
	return totalUpdated, nil
}

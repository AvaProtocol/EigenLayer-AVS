/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/spf13/cobra"
)

var (
	statusCmd = &cobra.Command{
		Use:   "status",
		Short: "Display system status",
		Long:  `Display status information about active tasks in the database`,
		Run: func(cmd *cobra.Command, args []string) {
			// Use default database path
			dbPath := "./data/badger"
			fmt.Printf("📊 System Status Report\n")
			fmt.Printf("======================\n\n")
			fmt.Printf("💾 Using database path: %s\n\n", dbPath)

			// Initialize database
			db, err := storage.NewWithPath(dbPath)
			if err != nil {
				fmt.Printf("❌ Failed to initialize database: %v\n", err)
				fmt.Printf("   💡 Make sure the aggregator has been started at least once\n")
				os.Exit(1)
			}
			defer db.Close()

			// Check active tasks from database
			kvs, err := db.GetByPrefix(taskengine.TaskByStatusStoragePrefix(avsproto.TaskStatus_Enabled))
			if err != nil && err != badger.ErrKeyNotFound {
				fmt.Printf("❌ Failed to query active tasks: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("💾 Database Status:\n")
			fmt.Printf("   Active tasks in database: %d\n\n", len(kvs))

			if len(kvs) > 0 {
				fmt.Printf("📋 Active Tasks:\n")
				for i, item := range kvs {
					if i >= 10 {
						fmt.Printf("   ... and %d more tasks\n", len(kvs)-10)
						break
					}
					fmt.Printf("   %d. %s\n", i+1, string(item.Key))
				}
				fmt.Printf("\n")
			}

			fmt.Printf("💡 Troubleshooting:\n")
			if len(kvs) == 0 {
				fmt.Printf("   ❌ No active tasks found in database\n")
				fmt.Printf("   ✅ Create a task using the web UI or API\n")
				fmt.Printf("   ✅ Restart the aggregator to see task count logs\n")
			} else {
				fmt.Printf("   ✅ %d active tasks found in database\n", len(kvs))
				fmt.Printf("   ✅ Tasks should be sent to operators when they connect\n")
				fmt.Printf("   ✅ Restart the aggregator to see '🚀 Engine started successfully' with task count\n")
			}

			fmt.Printf("   📝 Check aggregator logs for 'total_tasks_in_memory' field\n")
			fmt.Printf("   📝 Check operator logs for 'Created new subscription' messages\n")
		},
	}
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

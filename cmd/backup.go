package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AvaProtocol/ap-avs/storage"
	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/spf13/cobra"
)

var (
	backupDir        string
	periodicInterval int
	dbPath           string
	logger           = logging.NewNoopLogger()

	backupCmd = &cobra.Command{
		Use:   "backup",
		Short: "Backup BadgerDB data",
		Long: `Backup BadgerDB data to a specified directory.

The backup command can run either as a one-time backup or as a periodic backup process.
Backups are stored in the format: /backup_dir/yy-mm-dd-hh-mm/
Use --db-path to specify the BadgerDB directory to backup.
Use --dir to specify where to store the backups.
Use --interval to enable periodic backups (value in minutes, 0 means one-time backup).`,
		Run: func(cmd *cobra.Command, args []string) {
			runBackup(dbPath, backupDir, periodicInterval)
		},
	}

	restoreCmd = &cobra.Command{
		Use:   "restore",
		Short: "Restore BadgerDB data from backup",
		Long: `Restore BadgerDB data from a backup file.

Use --db-path to specify the BadgerDB directory to restore to.
Use --file to specify the backup file to restore from.`,
		Run: func(cmd *cobra.Command, args []string) {
			runRestore(dbPath, restoreFile)
		},
	}

	restoreFile string
)

func runBackup(dbPath, backupDir string, intervalMinutes int) {
	fmt.Printf("Starting BadgerDB backup. DB path: %s, Backup directory: %s\n", dbPath, backupDir)

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		fmt.Printf("Failed to create backup directory: %v\n", err)
		os.Exit(1)
	}

	if intervalMinutes == 0 {
		if err := performBackup(dbPath, backupDir); err != nil {
			fmt.Printf("Backup failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("Setting up periodic backup every %d minutes\n", intervalMinutes)
	ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
	defer ticker.Stop()

	if err := performBackup(dbPath, backupDir); err != nil {
		fmt.Printf("Initial backup failed: %v\n", err)
		os.Exit(1)
	}

	for {
		select {
		case <-ticker.C:
			if err := performBackup(dbPath, backupDir); err != nil {
				fmt.Printf("Periodic backup failed: %v\n", err)
			}
		}
	}
}

func performBackup(dbPath, backupDir string) error {
	timestamp := time.Now().Format("06-01-02-15-04")
	backupPath := filepath.Join(backupDir, timestamp)
	
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		return fmt.Errorf("failed to create backup timestamp directory: %v", err)
	}

	db, err := storage.NewWithPath(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %v", err)
	}
	defer db.Close()

	backupFile := filepath.Join(backupPath, "badger.backup")
	f, err := os.Create(backupFile)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %v", err)
	}
	defer f.Close()

	fmt.Printf("Running backup to %s\n", backupFile)
	since := uint64(0) // Start from the beginning
	_, err = db.Backup(context.Background(), f, since)
	if err != nil {
		return fmt.Errorf("backup operation failed: %v", err)
	}

	fmt.Printf("Backup completed successfully to %s\n", backupFile)
	return nil
}

func runRestore(dbPath, restoreFile string) {
	fmt.Printf("Starting BadgerDB restore. DB path: %s, Restore file: %s\n", dbPath, restoreFile)

	f, err := os.Open(restoreFile)
	if err != nil {
		fmt.Printf("Failed to open backup file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	if err := os.MkdirAll(dbPath, 0755); err != nil {
		fmt.Printf("Failed to create DB directory: %v\n", err)
		os.Exit(1)
	}

	db, err := storage.NewWithPath(dbPath)
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	fmt.Printf("Running restore from %s\n", restoreFile)
	err = db.Load(context.Background(), f)
	if err != nil {
		fmt.Printf("Restore operation failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Restore completed successfully\n")
}

func init() {
	backupCmd.Flags().StringVar(&dbPath, "db-path", "", "Path to the BadgerDB directory (required)")
	backupCmd.Flags().StringVar(&backupDir, "dir", "./backup", "Directory to store backups")
	backupCmd.Flags().IntVar(&periodicInterval, "interval", 0, "Run backups periodically (minutes, 0 for one-time)")
	backupCmd.MarkFlagRequired("db-path")
	rootCmd.AddCommand(backupCmd)

	restoreCmd.Flags().StringVar(&dbPath, "db-path", "", "Path to the BadgerDB directory (required)")
	restoreCmd.Flags().StringVar(&restoreFile, "file", "", "Backup file to restore from (required)")
	restoreCmd.MarkFlagRequired("db-path")
	restoreCmd.MarkFlagRequired("file")
	rootCmd.AddCommand(restoreCmd)
}

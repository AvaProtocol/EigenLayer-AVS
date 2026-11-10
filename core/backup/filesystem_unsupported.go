//go:build !linux && !darwin

package backup

import (
	"fmt"
	"runtime"
)

// Keep operator/client/recovery binaries available on other release platforms.
// Do not silently claim durable backups where directory sync is unsupported.
var errBackupFilesystemUnsupported = fmt.Errorf("durable application backups require Linux or macOS; unsupported platform: %s", runtime.GOOS)

func availableBytes(string) (int64, error)      { return 0, errBackupFilesystemUnsupported }
func syncDirectory(string) error                { return errBackupFilesystemUnsupported }
func (s *Service) ensureBackupDirectory() error { return errBackupFilesystemUnsupported }

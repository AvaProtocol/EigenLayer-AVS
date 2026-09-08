package config

import (
	"testing"
	"time"
)

func TestResolveBackupDir(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dbPath, backupDir, want string
	}{
		{"/data/gateway", "", "/data/gateway_backup"},
		{"/data/gateway", "/data/gateway_backup", "/data/gateway_backup"},
		{"/data/gateway", "/data/gateway-backup", "/data/gateway-backup"},
		{"/data/gateway", "  /data/gateway_backup  ", "/data/gateway_backup"},
		{"", "", ""},
		{"", "/custom", "/custom"},
	}
	for _, c := range cases {
		got := resolveBackupDir(c.dbPath, c.backupDir)
		if got != c.want {
			t.Errorf("resolveBackupDir(%q, %q) = %q, want %q", c.dbPath, c.backupDir, got, c.want)
		}
	}
}

func TestResolveBackupInterval(t *testing.T) {
	t.Parallel()

	if got := resolveBackupInterval(0); got != 0 {
		t.Errorf("0 hours: got %v, want 0", got)
	}
	if got := resolveBackupInterval(-1); got != 0 {
		t.Errorf("negative: got %v, want 0", got)
	}
	if got := resolveBackupInterval(24); got != 24*time.Hour {
		t.Errorf("24 hours: got %v, want 24h", got)
	}
}

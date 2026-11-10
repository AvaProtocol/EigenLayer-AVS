package aggregator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/backup"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/migrations"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

// Exercise Start itself without constructing contract clients or contacting a
// real chain. Both failures must happen before the task engine/server starts.
func newBackupStartupAggregator(t *testing.T, cfg *config.Config) *Aggregator {
	t.Helper()
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode startup RPC request: %v", err)
			http.Error(w, "invalid RPC request", http.StatusBadRequest)
			return
		}
		if request.Method != "eth_chainId" {
			t.Errorf("startup continued past the backup guard: RPC method %q", request.Method)
			http.Error(w, "unexpected RPC method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "0x1"}); err != nil {
			t.Errorf("write startup RPC response: %v", err)
		}
	}))
	t.Cleanup(rpc.Close)
	cfg.EthHttpRpcUrl = rpc.URL
	agg := &Aggregator{
		logger:       logger.NewNoOpLogger(),
		config:       cfg,
		operatorPool: &OperatorPool{},
		status:       initStatus,
	}
	traceback := os.Getenv("GOTRACEBACK")
	previousLogger, previousRepanic := packageLogger, repanicOnPanic
	previousChainEnv := config.CurrentChainEnv
	t.Setenv("GOTRACEBACK", traceback)
	t.Cleanup(func() {
		debug.SetTraceback(traceback)
		SetPackageLogger(previousLogger)
		SetRepanicOnPanic(previousRepanic)
		config.CurrentChainEnv = previousChainEnv
		if agg.backup != nil {
			agg.backup.StopPeriodicBackup()
		}
		if agg.db != nil {
			require.NoError(t, agg.db.Close())
		}
		if agg.ethRpcClient != nil {
			agg.ethRpcClient.Close()
		}
	})
	return agg
}

func TestAggregatorStartupRejectsMigrationBackupFailure(t *testing.T) {
	root := t.TempDir()
	previous := filepath.Join(root, "26-01-01-00-00")
	require.NoError(t, os.Mkdir(previous, 0700))
	previous = filepath.Join(previous, "full-backup.db")
	require.NoError(t, os.WriteFile(previous, []byte("last recovery copy"), 0600))
	agg := newBackupStartupAggregator(t, &config.Config{
		IsGateway:               true,
		DbPath:                  t.TempDir(),
		BackupDir:               root,
		BackupRetentionCount:    1,
		BackupRetentionMaxBytes: 1,
		BackupMinFreeBytes:      1,
	})

	// Linux/macOS reject the deliberately impossible byte budget. Windows
	// rejects the unsupported filesystem before exporting or pruning anything.
	require.PanicsWithValue(t, "database migration failed - cannot continue", func() {
		_ = agg.Start(context.Background())
	})
	require.NotNil(t, agg.migrator)
	require.Nil(t, agg.engine)
	require.Nil(t, agg.chainRegistry)
	require.Nil(t, agg.rpcServer)
	require.Equal(t, initStatus, agg.status)
	markers, err := agg.db.GetByPrefix([]byte("migration:"))
	require.NoError(t, err)
	require.Empty(t, markers, "a failed backup must not let any migration run")
	data, err := os.ReadFile(previous)
	require.NoError(t, err)
	require.Equal(t, "last recovery copy", string(data))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, err = agg.backup.PerformBackup()
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		require.ErrorIs(t, err, backup.ErrBudgetExceeded)
	} else {
		require.ErrorContains(t, err, "unsupported platform: "+runtime.GOOS)
	}
}

func TestAggregatorStartupRejectsPeriodicBackupFailure(t *testing.T) {
	dbPath := t.TempDir()
	func() {
		db, err := storage.NewWithPath(dbPath)
		require.NoError(t, err)
		defer func() { require.NoError(t, db.Close()) }()
		// Skip migration backups so the failure reaches StartPeriodicBackup.
		for _, migration := range migrations.Migrations {
			require.NoError(t, db.Set([]byte("migration:"+migration.Name), []byte("already applied")))
		}
		chainMigration := migrations.NewChainScopedKeysMigration(1)
		require.NoError(t, db.Set([]byte("migration:"+chainMigration.Name), []byte("already applied")))
	}()
	backupPath := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(backupPath, []byte("keep this file"), 0600))
	agg := newBackupStartupAggregator(t, &config.Config{
		IsGateway:      true,
		DbPath:         dbPath,
		BackupDir:      backupPath,
		BackupInterval: time.Hour,
	})

	err := agg.Start(context.Background())
	require.ErrorContains(t, err, "periodic backup to "+backupPath)
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		require.ErrorContains(t, err, "unsupported platform: "+runtime.GOOS)
	}
	require.Nil(t, agg.engine)
	require.Nil(t, agg.chainRegistry)
	require.Nil(t, agg.rpcServer)
	require.Equal(t, initStatus, agg.status)
	data, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	require.Equal(t, "keep this file", string(data))
}

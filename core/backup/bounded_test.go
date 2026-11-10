package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

type backupSource struct {
	storage.Storage
	payload []byte
	failure error
}

func (b *backupSource) Backup(_ context.Context, w io.Writer, _ uint64) (uint64, error) {
	n, err := w.Write(b.payload)
	if err != nil {
		return 0, err
	}
	return uint64(n), b.failure
}

func testService(t *testing.T, db storage.Storage, options Options) *Service {
	t.Helper()
	s := NewService(testutil.GetLogger(), db, t.TempDir(), options)
	s.freeBytes = func(string) (int64, error) { return 1 << 40, nil }
	return s
}

func legacyBackup(t *testing.T, dir, stamp string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, stamp)
	require.NoError(t, os.Mkdir(path, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "full-backup.db"), data, 0600))
	return path
}

func TestBackupDefaultsAndInvalidOptions(t *testing.T) {
	s := testService(t, nil, Options{})
	require.Equal(t, Options{RetentionCount: 3, MaxBytes: 12 << 30, MinFreeBytes: 2 << 30}, s.options)
	for _, opts := range []Options{{RetentionCount: -1}, {MaxBytes: -1}, {MinFreeBytes: -1}} {
		s := testService(t, nil, opts)
		_, err := s.PerformBackup()
		require.Error(t, err)
		require.Error(t, s.StartPeriodicBackup(time.Hour))
	}
}

func TestCompressedBackupAndLegacyRestoreRoundTrip(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	key := []byte("roundtrip:binary")
	value := bytes.Repeat([]byte{0, 1, 2, 255}, 4096)
	require.NoError(t, db.Set(key, value))
	s := testService(t, db, Options{})
	compressed, err := s.PerformBackup()
	require.NoError(t, err)
	require.Equal(t, archiveName, filepath.Base(compressed))
	info, err := os.Stat(compressed)
	require.NoError(t, err)
	require.EqualValues(t, 0600, info.Mode().Perm())
	require.Less(t, info.Size(), int64(len(value)))
	// The legacy stream remains readable, and compression is detected even
	// when an operator renames an archive without retaining its extension.
	raw := filepath.Join(t.TempDir(), "legacy.db")
	f, err := os.Create(raw)
	require.NoError(t, err)
	_, err = db.Backup(context.Background(), f, 0)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	renamed := filepath.Join(t.TempDir(), "compressed-without-extension")
	data, err := os.ReadFile(compressed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(renamed, data, 0600))
	for _, path := range []string{raw, compressed, renamed} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			r, err := Open(path)
			require.NoError(t, err)
			defer r.Close()
			restored, err := storage.NewWithPath(t.TempDir())
			require.NoError(t, err)
			defer restored.Close()
			require.NoError(t, restored.Load(context.Background(), r))
			_, err = io.Copy(io.Discard, r)
			require.NoError(t, err)
			got, err := restored.GetKey(key)
			require.NoError(t, err)
			require.Equal(t, value, got)
		})
	}
}

type faultFile struct {
	*os.File
	writeErr, syncErr, closeErr error
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.File.Write(p)
}
func (f *faultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}
func (f *faultFile) Close() error { return errors.Join(f.File.Close(), f.closeErr) }

func TestFailedBackupNeverReplacesExistingCopy(t *testing.T) {
	failure := errors.New("injected backup failure")
	for _, stage := range []string{"source", "write", "sync", "close", "directory sync", "rename", "publication sync"} {
		t.Run(stage, func(t *testing.T) {
			source := &backupSource{payload: bytes.Repeat([]byte("backup"), 1024)}
			s := testService(t, source, Options{RetentionCount: 1})
			old := legacyBackup(t, s.backupDir, "26-01-01-00-00", []byte("last completed backup"))
			originalCreate := s.createFile
			s.createFile = func(path string) (archiveFile, error) {
				f, err := originalCreate(path)
				if err != nil {
					return nil, err
				}
				wrapped := &faultFile{File: f.(*os.File)}
				switch stage {
				case "write":
					wrapped.writeErr = failure
				case "sync":
					wrapped.syncErr = failure
				case "close":
					wrapped.closeErr = failure
				}
				return wrapped, nil
			}
			switch stage {
			case "source":
				source.failure = failure
			case "directory sync":
				s.syncDir = func(string) error { return failure }
			case "rename":
				s.rename = func(string, string) error { return failure }
			case "publication sync":
				s.syncDir = func(path string) error {
					if path == s.backupDir {
						return failure
					}
					return syncDirectory(path)
				}
			}
			path, err := s.PerformBackup()
			require.ErrorIs(t, err, failure)
			require.Empty(t, path)
			data, err := os.ReadFile(filepath.Join(old, "full-backup.db"))
			require.NoError(t, err)
			require.Equal(t, "last completed backup", string(data))
			entries, err := os.ReadDir(s.backupDir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "failed/partial backups must not count toward retention")
		})
	}
}

func TestBackupLimitsIncludeInFlightWritesAndGzipFooter(t *testing.T) {
	payload := make([]byte, 128<<10)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		payload []byte
		limit   int64
	}{
		{"incompressible stream", payload, 4096},
		{"gzip footer", nil, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t, &backupSource{payload: tc.payload}, Options{MaxBytes: tc.limit})
			_, err := s.PerformBackup()
			require.ErrorIs(t, err, ErrBudgetExceeded)
			entries, err := os.ReadDir(s.backupDir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestSpaceExhaustedDuringBackupPreservesLastCopy(t *testing.T) {
	payload := make([]byte, 128<<10)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	s := testService(t, &backupSource{payload: payload}, Options{MinFreeBytes: 1024})
	old := legacyBackup(t, s.backupDir, "26-01-01-00-00", []byte("preserve me"))
	calls := 0
	s.freeBytes = func(string) (int64, error) {
		calls++
		if calls >= 3 {
			return 1024, nil
		}
		return 1 << 30, nil
	}
	_, err = s.PerformBackup()
	require.ErrorIs(t, err, ErrInsufficientSpace)
	require.GreaterOrEqual(t, calls, 3, "space must be rechecked after writing starts")
	data, err := os.ReadFile(filepath.Join(old, "full-backup.db"))
	require.NoError(t, err)
	require.Equal(t, "preserve me", string(data))
	entries, err := os.ReadDir(s.backupDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestRealBadgerBackupReportsWriterBudgetFailure(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	defer db.Close()
	payload := make([]byte, 128<<10)
	_, err = rand.Read(payload)
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("incompressible"), payload))
	s := testService(t, db, Options{MaxBytes: 1024})
	_, err = s.PerformBackup()
	require.ErrorIs(t, err, ErrBudgetExceeded)
	entries, err := os.ReadDir(s.backupDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestBytePressurePrunesOlderCopiesButNeverLastOnFailure(t *testing.T) {
	s := testService(t, &backupSource{payload: bytes.Repeat([]byte("data"), 100)}, Options{RetentionCount: 10, MaxBytes: 150})
	older := legacyBackup(t, s.backupDir, "26-01-01-00-00", bytes.Repeat([]byte("a"), 100))
	newest := legacyBackup(t, s.backupDir, "26-01-02-00-00", bytes.Repeat([]byte("b"), 100))
	current, err := s.PerformBackup()
	require.NoError(t, err)
	_, err = os.Stat(older)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(newest)
	require.NoError(t, err)
	backups, err := s.listBackups()
	require.NoError(t, err)
	require.LessOrEqual(t, retainedBytes(backups), int64(150))
	// If even the newest existing copy exceeds a newly configured budget,
	// fail without deleting that copy or publishing an empty replacement.
	s.options.MaxBytes = 1
	_, err = s.PerformBackup()
	require.ErrorIs(t, err, ErrBudgetExceeded)
	_, err = os.Stat(current)
	require.NoError(t, err)
}

func TestRetentionProtectsNewBackupDespiteClockRollback(t *testing.T) {
	s := testService(t, &backupSource{payload: []byte("current backup")}, Options{RetentionCount: 1})
	future := legacyBackup(t, s.backupDir, "50-01-01-00-00", []byte("old copy with future date"))
	path, err := s.PerformBackup()
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.NoError(t, err)
	_, err = os.Stat(future)
	require.True(t, os.IsNotExist(err))
	r, err := Open(path)
	require.NoError(t, err)
	defer r.Close()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "current backup", string(data))
}

func TestRetentionIgnoresIncompleteUnknownAndSymlinkDirectories(t *testing.T) {
	s := testService(t, nil, Options{RetentionCount: 1})
	old := legacyBackup(t, s.backupDir, "26-01-01-00-00", []byte("old"))
	latest := legacyBackup(t, s.backupDir, "26-01-02-00-00", []byte("latest"))
	empty := legacyBackup(t, s.backupDir, "26-01-03-00-00", nil)
	unknown := legacyBackup(t, s.backupDir, "26-01-04-00-00", []byte("not owned"))
	require.NoError(t, os.WriteFile(filepath.Join(unknown, "customer-data"), []byte("keep"), 0600))
	partial := legacyBackup(t, s.backupDir, ".partial-20261110T010000.000000000Z-1234", []byte("partial"))
	outside := t.TempDir()
	link := filepath.Join(s.backupDir, "26-01-05-00-00")
	require.NoError(t, os.Symlink(outside, link))
	s.pruneOldBackups()
	_, err := os.Stat(old)
	require.True(t, os.IsNotExist(err))
	for _, path := range []string{latest, empty, unknown, partial, link, outside} {
		_, err := os.Lstat(path)
		require.NoError(t, err, "unexpected deletion: %s", path)
	}
}

type serializedSource struct {
	storage.Storage
	active  atomic.Int32
	overlap atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *serializedSource) Backup(_ context.Context, w io.Writer, _ uint64) (uint64, error) {
	if b.active.Add(1) != 1 {
		b.overlap.Store(true)
	}
	defer b.active.Add(-1)
	b.entered <- struct{}{}
	<-b.release
	_, err := w.Write([]byte("serialized"))
	return 0, err
}

func TestConcurrentBackupsSerializeAndNeverOverwrite(t *testing.T) {
	source := &serializedSource{entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := testService(t, source, Options{})
	results := make(chan string, 2)
	errors := make(chan error, 2)
	run := func() { path, err := s.PerformBackup(); results <- path; errors <- err }
	go run()
	select {
	case <-source.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first backup did not start")
	}
	go run()
	select {
	case <-source.entered:
		t.Fatal("backup writers overlapped")
	case <-time.After(50 * time.Millisecond):
	}
	close(source.release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-errors:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("backup did not finish")
		}
	}
	first, second := <-results, <-results
	require.NotEqual(t, first, second)
	require.False(t, source.overlap.Load())
	for _, path := range []string{first, second} {
		_, err := os.Stat(path)
		require.NoError(t, err)
	}
}

func TestOpenRejectsTruncatedAndCorruptGzip(t *testing.T) {
	var encoded bytes.Buffer
	gz := gzip.NewWriter(&encoded)
	_, err := gz.Write([]byte("verify full checksum"))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	corrupt := append([]byte(nil), encoded.Bytes()...)
	corrupt[len(corrupt)-8] ^= 0xff
	for i, data := range [][]byte{nil, {0x1f}, encoded.Bytes()[:5], encoded.Bytes()[:encoded.Len()-4], corrupt} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "backup.gz")
			require.NoError(t, os.WriteFile(path, data, 0600))
			r, err := Open(path)
			if err != nil {
				return
			}
			defer r.Close()
			_, err = io.Copy(io.Discard, r)
			require.Error(t, err, "truncated/corrupt archives must never be accepted")
		})
	}
}

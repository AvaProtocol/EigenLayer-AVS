package backup

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Open reads legacy raw Badger backups and gzip archives. .db explicitly selects
// raw and .gz selects gzip; other names use header detection, with a raw fallback
// for plausible Badger lengths when gzip-header validation fails. Use .db for
// raw streams whose length also mimics a complete valid gzip header. Consume to
// EOF to verify gzip's checksum/trailer; Close alone does not verify it.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(f)
	format := strings.ToLower(filepath.Ext(path))
	if format == ".db" {
		return &backupReader{Reader: r, file: f}, nil
	}
	header, err := r.Peek(2)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if header[0] != 0x1f || header[1] != 0x8b {
		if format == ".gz" {
			f.Close()
			return nil, fmt.Errorf("gzip backup is missing its gzip header")
		}
		return &backupReader{Reader: r, file: f}, nil
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		// A raw Badger batch starts with a little-endian uint64 length.
		// For example 35,615 starts 1f 8b but is not a gzip header. Rewind
		// before fallback, and require the length to fit the file so damaged
		// gzip headers cannot become enormous Badger allocation requests.
		var prefix [8]byte
		_, readErr := f.ReadAt(prefix[:], 0)
		info, statErr := f.Stat()
		if format != ".gz" && readErr == nil && statErr == nil && info.Size() >= 8 &&
			binary.LittleEndian.Uint64(prefix[:]) <= uint64(info.Size()-8) {
			if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
				f.Close()
				return nil, errors.Join(err, seekErr)
			}
			r.Reset(f)
			return &backupReader{Reader: r, file: f}, nil
		}
		f.Close()
		return nil, err
	}
	return &backupReader{Reader: gz, gzip: gz, file: f}, nil
}

type backupReader struct {
	io.Reader
	gzip *gzip.Reader
	file *os.File
}

func (r *backupReader) Close() error {
	var err error
	if r.gzip != nil {
		err = r.gzip.Close()
	}
	return errors.Join(err, r.file.Close())
}

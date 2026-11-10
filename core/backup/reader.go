package backup

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Open reads both legacy raw Badger backups and gzip archives, detecting the
// gzip header rather than requiring a filename extension. Consume to EOF to
// verify the gzip checksum/trailer; closing a gzip reader alone does not do so.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(f)
	header, err := r.Peek(2)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if header[0] != 0x1f || header[1] != 0x8b {
		if strings.HasSuffix(strings.ToLower(path), ".gz") {
			f.Close()
			return nil, fmt.Errorf("gzip backup is missing its gzip header")
		}
		return &backupReader{Reader: r, file: f}, nil
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
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

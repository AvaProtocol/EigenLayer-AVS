package storage

import (
	badger "github.com/dgraph-io/badger/v4"
)

type Config struct {
	Path string
}

type Storage interface {
	Setup() error
	Close() error
	BatchWrite(updates map[string][]byte) error
}

type BadgerStorage struct {
	config *Config
	db     *badger.DB
}

func New(c *Config) (Storage, error) {
	db, err := badger.Open(badger.DefaultOptions(c.Path))

	if err != nil {
		return nil, err
	}

	return &BadgerStorage{
		config: c,
		db:     db,
	}, nil
}

func (s *BadgerStorage) Setup() error {
	return nil
}

func (s *BadgerStorage) Close() error {
	return s.db.Close()
}

func (s *BadgerStorage) BatchWrite(updates map[string][]byte) error {
	txn := s.db.NewTransaction(true)
	for k, v := range updates {
		if err := txn.Set([]byte(k), v); err == badger.ErrTxnTooBig {
			_ = txn.Commit()
			txn = s.db.NewTransaction(true)
			_ = txn.Set([]byte(k), []byte(v))
		}
	}
	_ = txn.Commit()

	return nil
}

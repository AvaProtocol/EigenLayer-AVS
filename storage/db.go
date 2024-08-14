package storage

import (
	badger "github.com/dgraph-io/badger/v4"
)

type Config struct {
	Path string
}

type Sequence interface {
	Next() (uint64, error)
	Release() error
}

type Storage interface {
	Setup() error
	Close() error

	GetSequence(prefix []byte, inflightItem uint64) (Sequence, error)

	GetKey(prefix []byte) ([]byte, error)
	GetByPrefix(prefix []byte) ([]*KeyValueItem, error)
	GetKeyHasPrefix(prefix []byte) ([][]byte, error)
	FirstKVHasPrefix(prefix []byte) ([]byte, []byte, error)

	BatchWrite(updates map[string][]byte) error
	Move(src, dest []byte) error
	Set(key, value []byte) error
}

type KeyValueItem struct {
	Key   []byte
	Value []byte
}

type BadgerStorage struct {
	config *Config
	db     *badger.DB
}

func New(c *Config) (Storage, error) {
	opts := badger.DefaultOptions(c.Path)
	db, err := badger.Open(
		opts.WithSyncWrites(true),
	)

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

func (s *BadgerStorage) Set(key, value []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		err := txn.Set(key, value)
		return err
	})
}

// GetByPrefix return a list of key/value item whoser key prefix matches
func (s *BadgerStorage) GetByPrefix(prefix []byte) ([]*KeyValueItem, error) {
	var result []*KeyValueItem

	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := item.Key()
			err := item.Value(func(v []byte) error {
				result = append(result, &KeyValueItem{
					Key:   k,
					Value: v,
				})
				return nil
			})

			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return result, err
	}

	return result, nil
}

func (s *BadgerStorage) GetKeyHasPrefix(prefix []byte) ([][]byte, error) {
	var result [][]byte

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false

		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := item.Key()
			result = append(result, k)
		}
		return nil
	})

	if err != nil {
		return result, err
	}

	return result, nil
}

func (s *BadgerStorage) GetKey(key []byte) ([]byte, error) {
	var value []byte

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}

		err = item.Value(func(val []byte) error {
			value = append([]byte{}, val...)
			return nil
		})

		return err
	})

	return value, err
}

// Wrap badgerdb sequence
func (s *BadgerStorage) GetSequence(prefix []byte, inflightItem uint64) (Sequence, error) {
	return s.db.GetSequence(prefix, inflightItem)
}

func (s *BadgerStorage) FirstKVHasPrefix(prefix []byte) ([]byte, []byte, error) {
	var k []byte
	var v []byte

	err := s.db.View(func(txn *badger.Txn) error {
		itOpts := badger.DefaultIteratorOptions
		itOpts.PrefetchValues = true
		itOpts.PrefetchSize = 1
		it := txn.NewIterator(itOpts)

		// go to smallest key after prefix
		it.Seek(prefix)
		defer it.Close()
		// iteration done, no item found
		if !it.ValidForPrefix(prefix) {
			return nil
		}

		item := it.Item()

		k = item.KeyCopy(nil)

		var err error
		v, err = item.ValueCopy(nil)
		return err
	})

	if err == nil {
		return k, v, nil
	}

	return nil, nil, err
}

func (s *BadgerStorage) Move(src []byte, dest []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(src)
		if err != nil {
			return err
		}

		b, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}

		// key is found, we will delete from source, then set on target
		err = txn.Delete(src)
		if err != nil {
			return err
		}

		// create in Dest queue
		err = txn.Set(dest, b)
		return err
	})

}

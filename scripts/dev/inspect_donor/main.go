// Tiny one-off BadgerDB inspector. Opens a Badger directory read-only
// and prints keys matching a prefix. Used to confirm what migrate-hetzner
// wrote vs what the validator expects.
//
//	go run ./scripts/dev/inspect_donor <db_path> <prefix>
package main

import (
	"fmt"
	"log"
	"os"

	badger "github.com/dgraph-io/badger/v4"
)

func main() {
	if len(os.Args) < 3 {
		log.Fatalf("usage: %s <db_path> <prefix>", os.Args[0])
	}
	dbPath := os.Args[1]
	prefix := []byte(os.Args[2])

	opts := badger.DefaultOptions(dbPath).WithLogger(nil).WithReadOnly(true)
	db, err := badger.Open(opts)
	if err != nil {
		log.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()

	const maxKeys = 500
	// Keys-only scan — skip value prefetch so the iterator doesn't pull
	// payload bytes off disk for every match (the tool prints only keys).
	iterOpts := badger.DefaultIteratorOptions
	iterOpts.PrefetchValues = false
	count := 0
	err = db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(iterOpts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			if count >= maxKeys {
				fmt.Fprintf(os.Stderr, "(truncated at %d keys)\n", maxKeys)
				break
			}
			k := string(it.Item().KeyCopy(nil))
			fmt.Println(k)
			count++
		}
		return nil
	})
	if err != nil {
		log.Fatalf("iter: %v", err)
	}
	fmt.Fprintf(os.Stderr, "%d keys with prefix %q\n", count, string(prefix))
}

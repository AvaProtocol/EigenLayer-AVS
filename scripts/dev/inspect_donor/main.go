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

	count := 0
	err = db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			k := string(it.Item().KeyCopy(nil))
			fmt.Println(k)
			count++
			if count > 500 {
				fmt.Fprintln(os.Stderr, "(truncated at 500 keys)")
				break
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("iter: %v", err)
	}
	fmt.Fprintf(os.Stderr, "%d keys with prefix %q\n", count, string(prefix))
}

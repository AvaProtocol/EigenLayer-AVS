package taskengine

import (
	"os"

	"github.com/AvaProtocol/ap-avs/storage"
)

// Shortcut to initialize a storage at the given path, panic if we cannot create db
func TestMustDB() storage.Storage {
	dir, err := os.MkdirTemp("", "aptest")
	if err != nil {
		panic(err)
	}
	db, err := storage.NewWithPath(dir)
	if err != nil {
		panic(err)
	}
	return db
}

package testutil

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/ap-avs/storage"
)

func GetTestRPCURL() string {
	v := os.Getenv("RPC_URL")
	if v == "" {
		return "https://sepolia.drpc.org"
	}

	return v
}

func GetTestWsRPCURL() string {
	v := os.Getenv("WS_RPC_URL")
	if v == "" {
		return "wss://sepolia.drpc.org"
	}

	return v
}

func GetRpcClient() *ethclient.Client {
	client, err := ethclient.Dial(GetTestRPCURL())
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum client: %v", err)
	}

	return client
}

func GetEventForTx(txHash string, evtIndex uint64) (*types.Log, error) {
	client := GetRpcClient()

	receipt, err := client.TransactionReceipt(context.Background(), common.HexToHash(txHash))
	if err != nil {
		return nil, err
	}

	var event *types.Log
	for _, l := range receipt.Logs {
		if uint64(l.Index) == evtIndex {
			event = l
		}
	}

	if event == nil {
		return nil, fmt.Errorf("not found event")
	}
	return receipt.Logs[evtIndex], nil
}

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

func GetDefaultCache() *bigcache.BigCache {
	config := bigcache.Config{

		// number of shards (must be a power of 2)
		Shards: 1024,

		// time after which entry can be evicted
		LifeWindow: 10 * time.Minute,

		// Interval between removing expired entries (clean up).
		// If set to <= 0 then no action is performed.
		// Setting to < 1 second is counterproductive — bigcache has a one second resolution.
		CleanWindow: 5 * time.Minute,

		// rps * lifeWindow, used only in initial memory allocation
		MaxEntriesInWindow: 1000 * 10 * 60,

		// max entry size in bytes, used only in initial memory allocation
		MaxEntrySize: 500,

		// prints information about additional memory allocation
		Verbose: true,

		// cache will not allocate more memory than this limit, value in MB
		// if value is reached then the oldest entries can be overridden for the new ones
		// 0 value means no size limit
		HardMaxCacheSize: 8192,

		// callback fired when the oldest entry is removed because of its expiration time or no space left
		// for the new entry, or because delete was called. A bitmask representing the reason will be returned.
		// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
		OnRemove: nil,

		// OnRemoveWithReason is a callback fired when the oldest entry is removed because of its expiration time or no space left
		// for the new entry, or because delete was called. A constant representing the reason will be passed through.
		// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
		// Ignored if OnRemove is specified.
		OnRemoveWithReason: nil,
	}
	cache, err := bigcache.New(context.Background(), config)
	if err != nil {
		panic(fmt.Errorf("error get default cache for test"))
	}
	return cache

}

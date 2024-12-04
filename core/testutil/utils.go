package testutil

import (
	"context"
	"fmt"
	"log"
	"os"

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

func GetEventForTx(txHash string, evtIndex int) (*types.Log, error) {
	client := GetRpcClient()

	receipt, err := client.TransactionReceipt(context.Background(), common.HexToHash(txHash))
	if err != nil {
		return nil, err
	}

	var event *types.Log
	for _, l := range receipt.Logs {
		if uint64(l.Index) == triggerMark.LogIndex {
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

package aggregator

import (
	"math/big"
	"sync"
)

var (
	globalChainID     *big.Int
	globalChainIDLock sync.RWMutex
)

func SetGlobalChainID(chainID *big.Int) {
	globalChainIDLock.Lock()
	defer globalChainIDLock.Unlock()
	globalChainID = chainID
}

func GetGlobalChainID() *big.Int {
	globalChainIDLock.RLock()
	defer globalChainIDLock.RUnlock()
	return globalChainID
}

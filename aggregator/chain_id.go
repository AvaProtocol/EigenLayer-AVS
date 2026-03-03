package aggregator

import (
	"math/big"
	"sync"
)

var (
	eigenLayerChainID     *big.Int
	eigenLayerChainIDLock sync.RWMutex
)

func SetEigenLayerChainID(chainID *big.Int) {
	eigenLayerChainIDLock.Lock()
	defer eigenLayerChainIDLock.Unlock()
	eigenLayerChainID = chainID
}

func GetEigenLayerChainID() *big.Int {
	eigenLayerChainIDLock.RLock()
	defer eigenLayerChainIDLock.RUnlock()
	return eigenLayerChainID
}

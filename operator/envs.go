package operator

import "math/big"

// Populate configuration based on known env
// TODO: We can fetch this dynamically from aggregator so we can upgrade the
// config without the need to release operator
func (o *Operator) PopulateKnownConfigByChainID(chainID big.Int) error {

}

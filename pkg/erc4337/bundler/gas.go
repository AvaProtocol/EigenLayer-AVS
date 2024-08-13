package bundler

import "math/big"

type GasEstimation struct {
	PreVerificationGas   *big.Int
	VerificationGasLimit *big.Int
	CallGasLimit         *big.Int
	VerificationGas      *big.Int
}

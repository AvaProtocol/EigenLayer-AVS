package aa

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ErrLegacyFactory reports a factory other than the Modular Account v2 one.
// Wallets recorded under such a factory predate the EntryPoint v0.7 cutover:
// the gateway no longer derives, deploys or executes them.
var ErrLegacyFactory = errors.New("not the Modular Account v2 factory")

// RequireMAv2Factory returns ErrLegacyFactory for any factory but MA v2.
func RequireMAv2Factory(factory common.Address) error {
	if factory != MAv2FactoryAddress() {
		return fmt.Errorf("%w: %s", ErrLegacyFactory, factory.Hex())
	}
	return nil
}

// DeriveSenderAddress returns the counterfactual account address for
// (owner, salt) at factory. The factory comes from a stored wallet record or a
// request, so it is checked rather than assumed: the MA v2 address for the
// same (owner, salt) is a different account from a legacy wallet's.
func DeriveSenderAddress(conn *ethclient.Client, owner, factory common.Address, salt *big.Int) (*common.Address, error) {
	if err := RequireMAv2Factory(factory); err != nil {
		return nil, err
	}
	return GetSenderAddressMAv2(conn, owner, salt)
}

// DeriveInitCode returns the (factory, factoryData) pair that deploys the
// account for (owner, salt) at factory, under the same check as
// DeriveSenderAddress.
func DeriveInitCode(owner, factory common.Address, salt *big.Int) (common.Address, []byte, error) {
	if err := RequireMAv2Factory(factory); err != nil {
		return common.Address{}, nil, err
	}
	return GetInitCodeMAv2(owner, salt)
}

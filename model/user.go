package model

import (
	"github.com/ethereum/go-ethereum/common"
)

type User struct {
	Address             common.Address
	SmartAccountAddress *common.Address
}

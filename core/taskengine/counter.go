package taskengine

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

func ContractWriteCounterKey(owner common.Address) string {
	return fmt.Sprintf("contract_write_counter:%s", owner.Hex())
}

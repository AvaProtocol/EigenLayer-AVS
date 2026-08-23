package config

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestIsApprovedOperator(t *testing.T) {
	var nilCfg *Config
	if nilCfg.IsApprovedOperator("0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d") {
		t.Fatal("nil config must not approve anyone")
	}

	empty := &Config{}
	if !empty.IsApprovedOperator("0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d") {
		t.Fatal("empty list falls back to the hardcoded operators")
	}
	if empty.IsApprovedOperator("0x0000000000000000000000000000000000000001") {
		t.Fatal("empty list must not approve an unknown address")
	}

	listed := &Config{ApprovedOperators: []common.Address{common.HexToAddress("0x0000000000000000000000000000000000000001")}}
	if !listed.IsApprovedOperator("0x0000000000000000000000000000000000000001") {
		t.Fatal("configured operator must be approved")
	}
	if listed.IsApprovedOperator("0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d") {
		t.Fatal("hardcoded fallback must not apply once a list is configured")
	}
}

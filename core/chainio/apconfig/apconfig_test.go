package apconfig

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestAddressForChain(t *testing.T) {
	if AddressForChain(big.NewInt(1)) != MainnetAddress {
		t.Errorf("mainnet: got %s, want %s", AddressForChain(big.NewInt(1)).Hex(), MainnetAddress.Hex())
	}

	liveSepolia := common.HexToAddress("0xFf2E98967A8607F9Acd5CC9024cC5dE5DF0D60a3")
	staleSepolia := common.HexToAddress("0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb")
	if TestnetAddress != liveSepolia {
		t.Errorf("TestnetAddress %s, want the live Sepolia proxy %s", TestnetAddress.Hex(), liveSepolia.Hex())
	}
	if TestnetAddress == staleSepolia {
		t.Fatal("TestnetAddress must not be the empty 0xb8abbb proxy")
	}

	sepolia := AddressForChain(big.NewInt(11155111))
	if sepolia != liveSepolia {
		t.Errorf("sepolia: got %s, want %s", sepolia.Hex(), liveSepolia.Hex())
	}
}

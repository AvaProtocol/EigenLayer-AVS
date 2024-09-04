package signer

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	eip191Prefix = "\x19Ethereum Signed Message:\n"
)

func FromPrivateKeyHex(privateKeyHex string, chainID *big.Int) (*bind.TransactOpts, error) {
	if strings.HasPrefix(privateKeyHex, "0x") {
		privateKeyHex = privateKeyHex[2:]
	}
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, err
	}

	return bind.NewKeyedTransactorWithChainID(privateKey, chainID)
}

// Generate EIP191 signature
func SignMessage(key *ecdsa.PrivateKey, data []byte) ([]byte, error) {
	prefix := []byte(eip191Prefix + fmt.Sprint(len(data)))
	prefixedData := append(prefix, data...)
	hash := crypto.Keccak256Hash(prefixedData)
	sig, e := crypto.Sign(hash.Bytes(), key)
	// https://stackoverflow.com/questions/69762108/implementing-ethereum-personal-sign-eip-191-from-go-ethereum-gives-different-s
	sig[64] += 27

	return sig, e
}

func SignMessageAsHex(key *ecdsa.PrivateKey, data []byte) (string, error) {
	signature, e := SignMessage(key, data)
	if e == nil {
		return common.Bytes2Hex(signature), nil
	}

	return "", e
}

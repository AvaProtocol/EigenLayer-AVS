package macros

import (
	"context"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// QueryContract
func QueryContractRaw(
	ctx context.Context,
	client *ethclient.Client,
	contractAddress common.Address,
	data []byte,
) ([]byte, error) {
	// Prepare the call message
	msg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: data,
	}

	return client.CallContract(ctx, msg, nil)
}

// QueryContract
func QueryContract(
	client *ethclient.Client,
	contractAddress common.Address,
	contractABI string,
	method string,
	inputs ...any,
) ([]interface{}, error) {
	// Parse the ABI
	parsedABI, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		return nil, err
	}

	data, e := parsedABI.Pack(method, inputs...)
	if e != nil {
		return nil, e
	}

	// Perform the call
	output, err := QueryContractRaw(context.Background(), client, contractAddress, data)
	if err != nil {
		return nil, err
	}

	// Unpack the output
	return parsedABI.Unpack(method, output)
}

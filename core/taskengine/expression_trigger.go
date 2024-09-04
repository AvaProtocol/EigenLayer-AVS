package taskengine

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethmath "github.com/ethereum/go-ethereum/common/math"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// A generic function to query any contract. The method andcontractABI is
// necessary so we can unpack the result
func readContractData(contractAddress string, data string, method string, contractABI string) []any {
	parsedABI, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		log.Println("read contract data parse abi error", err)
		return nil
	}

	// Perform the call
	output, err := QueryContractRaw(
		context.Background(),
		rpcConn,
		common.HexToAddress(contractAddress),
		common.FromHex(data))
	if err != nil {
		log.Println("read contract data error", err)
		return nil
	}

	// Unpack the output
	result, err := parsedABI.Unpack(method, output)
	if err != nil {
		log.Println("unpack contract result error", err)
		return nil
	}

	return result
}

// QueryContract
//const taskCondition = `cmp(chainlinkPrice("0x694AA1769357215DE4FAC081bf1f309aDC325306"), parseUnit("262199799820", 8)) > 1`

func chainlinkPrice(tokenPair string) *big.Int {
	output, err := QueryContract(
		rpcConn,
		// https://docs.chain.link/data-feeds/price-feeds/addresses?network=ethereum&page=1&search=et#sepolia-testnet
		// ETH-USD pair on sepolia
		common.HexToAddress(tokenPair),
		chainlinkABI,
		"latestRoundData",
	)

	if err != nil {
		panic(fmt.Errorf("Error when querying contract through rpc. contract: %s. error: %w", tokenPair, err))
	}

	// TODO: Check round and answer to prevent the case where chainlink down we
	// may got outdated data
	return output[1].(*big.Int)
}

func bigCmp(a *big.Int, b *big.Int) (r int) {
	return a.Cmp(b)
}

func parseUnit(val string, decimal uint) *big.Int {
	b, ok := ethmath.ParseBig256(val)
	if !ok {
		panic(fmt.Errorf("Parse error: %s", val))
	}

	r := big.NewInt(0)
	return r.Div(b, big.NewInt(int64(decimal)))
}

func toBigInt(val string) *big.Int {
	// parse either string or hex
	b, ok := ethmath.ParseBig256(val)
	if !ok {
		return nil
	}

	return b
}

var (
	exprEnv = map[string]any{
		"readContractData": readContractData,
		"priceChainlink":   chainlinkPrice,
		"bigCmp":           bigCmp,
		"parseUnit":        parseUnit,
		"toBigInt":         toBigInt,
	}
)

func CompileExpression(rawExp string) (*vm.Program, error) {
	return expr.Compile(rawExp, expr.Env(exprEnv))
}

func RunExpressionQuery(exprCode string) (bool, error) {
	program, err := expr.Compile(exprCode, expr.Env(exprEnv), expr.AsBool())

	if err != nil {
		return false, err
	}

	result, err := expr.Run(program, exprEnv)
	if err != nil {
		log.Println("error when evaluting", err)
		return false, nil
	}

	return result.(bool), err
}

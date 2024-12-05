package macros

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

var (
	rpcConn *ethclient.Client
)

func SetRpc(rpcURL string) {
	if conn, err := ethclient.Dial(rpcURL); err == nil {
		rpcConn = conn
	} else {
		panic(err)
	}
}

// A generic function to query any contract. The method andcontractABI is
// necessary so we can unpack the result
func readContractData(contractAddress string, data string, method string, contractABI string) []any {
	parsedABI, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		return nil
	}

	// Perform the call
	output, err := QueryContractRaw(
		context.Background(),
		rpcConn,
		common.HexToAddress(contractAddress),
		common.FromHex(data))
	if err != nil {
		return nil
	}

	// Unpack the output
	result, err := parsedABI.Unpack(method, output)
	if err != nil {
		return nil
	}

	return result
}

// QueryContract
//const taskCondition = `cmp(chainlinkPrice("0x694AA1769357215DE4FAC081bf1f309aDC325306"), parseUnit("262199799820", 8)) > 1`

func chainlinkLatestRoundData(tokenPair string) *big.Int {
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

func chainlinkLatestAnswer(tokenPair string) *big.Int {
	output, err := QueryContract(
		rpcConn,
		// https://docs.chain.link/data-feeds/price-feeds/addresses?network=ethereum&page=1&search=et#sepolia-testnet
		// ETH-USD pair on sepolia
		common.HexToAddress(tokenPair),
		chainlinkABI,
		"latestAnswer",
	)

	if err != nil {
		panic(fmt.Errorf("Error when querying contract through rpc. contract: %s. error: %w", tokenPair, err))
	}

	return output[0].(*big.Int)
}

func BigCmp(a *big.Int, b *big.Int) (r int) {
	return a.Cmp(b)
}

func BigGt(a *big.Int, b *big.Int) bool {
	return a.Cmp(b) > 0
}

func BigLt(a *big.Int, b *big.Int) bool {
	return a.Cmp(b) < 0
}

func ParseUnit(val string, decimal uint) *big.Int {
	b, ok := ethmath.ParseBig256(val)
	if !ok {
		panic(fmt.Errorf("Parse error: %s", val))
	}

	r := big.NewInt(0)
	return r.Div(b, big.NewInt(int64(decimal)))
}

func ToBigInt(val string) *big.Int {
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

		"priceChainlink":           chainlinkLatestAnswer,
		"chainlinkPrice":           chainlinkLatestAnswer,
		"latestRoundDataChainlink": chainlinkLatestRoundData,

		"bigCmp":    BigCmp,
		"bigGt":     BigGt,
		"bigLt":     BigLt,
		"parseUnit": ParseUnit,
		"toBigInt":  ToBigInt,
	}
)

func GetEnvs(extra map[string]any) map[string]interface{} {
	envs := map[string]any{}

	for k, v := range exprEnv {
		envs[k] = v
	}

	for k, v := range extra {
		envs[k] = v
	}

	return envs
}

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
		return false, nil
	}

	return result.(bool), err
}

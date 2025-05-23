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
	resty "github.com/go-resty/resty/v2"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

var (
	rpcConn *ethclient.Client
)

type Builtin struct {
}

func SetRpc(rpcURL string) {
	if conn, err := ethclient.Dial(rpcURL); err == nil {
		rpcConn = conn
	} else {
		panic(fmt.Errorf("panic connect to rpc url %s, error %w", rpcURL, err))
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
	if len(output) < 2 || output[1] == nil {
		return big.NewInt(0)
	}
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

	if len(output) == 0 || output[0] == nil {
		return big.NewInt(0)
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

func (bi *Builtin) ToBigInt(val string) *big.Int {
	return ToBigInt(val)
}

func (bi *Builtin) ChainlinkLatestRoundData(tokenPair string) *big.Int {
	return chainlinkLatestRoundData(tokenPair)
}
func (bi *Builtin) ChainlinkLatestAnswer(tokenPair string) *big.Int {
	return chainlinkLatestAnswer(tokenPair)
}

func (bi *Builtin) BigCmp(a *big.Int, b *big.Int) (r int) {
	return BigCmp(a, b)
}

func (bi *Builtin) BigGt(a *big.Int, b *big.Int) bool {
	return BigGt(a, b)
}

func (bi *Builtin) BigLt(a *big.Int, b *big.Int) bool {
	return BigLt(a, b)
}

func (bi *Builtin) ParseUnit(val string, decimal uint) *big.Int {
	return ParseUnit(val, decimal)
}

var (
	exprEnv = map[string]any{
		// bind and simular JS fetch api
		"fetch": Fetch,

		// macro to do IO from JS
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
	MacroFuncs = []string{
		"fetch",
		"readContractData",
		"priceChainlink",
		"chainlinkPrice",
		"latestRoundDataChainlink",
		"bigCmp",
		"bigGt",
		"bigLt",
		"parseUnit",
		"toBigInt",
	}
)

// FetchResponse mimics the JS fetch Response object
type FetchResponse struct {
	Status     int
	StatusText string
	Body       string
	Headers    map[string][]string
}

// FetchOptions allows specifying method, headers, and body
type FetchOptions struct {
	Method  string
	Headers map[string]string
	Body    interface{}
}

// Fetch mimics the JS fetch function using Resty
func Fetch(url string) *FetchResponse {
	options := FetchOptions{}

	client := resty.New()
	// Create request
	request := client.R()

	// Set headers
	if options.Headers != nil {
		request.SetHeaders(options.Headers)
	}

	// Set body
	if options.Body != nil {
		request.SetBody(options.Body)
	}

	// Send request based on method
	var resp *resty.Response
	var err error
	switch options.Method {
	case "POST":
		resp, err = request.Post(url)
	case "PUT":
		resp, err = request.Put(url)
	case "DELETE":
		resp, err = request.Delete(url)
	default:
		resp, err = request.Get(url) // Default to GET
	}

	// Handle errors
	if err != nil {
		return nil
	}

	// Build FetchResponse
	return &FetchResponse{
		Status:     resp.StatusCode(),
		StatusText: resp.Status(),
		Body:       string(resp.Body()),
		Headers:    resp.Header(),
	}
}

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
		return false, err
	}

	boolResult, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("expression result is not a boolean")
	}

	return boolResult, nil
}

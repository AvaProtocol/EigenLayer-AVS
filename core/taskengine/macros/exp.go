package macros

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

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
	if rpcConn == nil {
		return nil
	}
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
//const taskCondition = `cmp(chainlinkPrice("0x694AA1769357215DE4FAC081bf1f309aDC325306"), parseUnit("2621.99", 8)) > 1`

// chainlinkDefaultMaxAge bounds how stale a feed's updatedAt may be before its
// answer is rejected. Chainlink heartbeats vary by feed (ETH/USD ~1h, some feeds
// up to 24h), so the default is deliberately loose to avoid rejecting a healthy
// slow feed. Callers on a faster feed should pass an explicit max age in seconds.
const chainlinkDefaultMaxAge = 24 * time.Hour

// chainlinkRound is the decoded latestRoundData tuple:
// (roundId, answer, startedAt, updatedAt, answeredInRound).
type chainlinkRound struct {
	roundID         *big.Int
	answer          *big.Int
	updatedAt       *big.Int
	answeredInRound *big.Int
}

// validateChainlinkRound returns the round's answer, or an error if the round is
// incomplete, reports a non-positive price, or is older than maxAge relative to
// now. Keeping the checks separate from the RPC call makes them unit-testable
// without a live feed. See https://docs.chain.link/data-feeds/api-reference.
func validateChainlinkRound(r chainlinkRound, maxAge time.Duration, now time.Time) (*big.Int, error) {
	if r.answer == nil || r.updatedAt == nil {
		return nil, fmt.Errorf("incomplete round data")
	}
	if r.answer.Sign() <= 0 {
		return nil, fmt.Errorf("invalid price %s: must be > 0", r.answer)
	}
	// updatedAt == 0 means the round has not been answered yet.
	if r.updatedAt.Sign() == 0 {
		return nil, fmt.Errorf("round not complete: updatedAt is 0")
	}
	// answeredInRound < roundId means the answer was carried over from an earlier
	// round — a stalled feed on legacy aggregators.
	if r.answeredInRound != nil && r.roundID != nil && r.answeredInRound.Cmp(r.roundID) < 0 {
		return nil, fmt.Errorf("stale round: answeredInRound %s < roundId %s", r.answeredInRound, r.roundID)
	}
	updatedAt := time.Unix(r.updatedAt.Int64(), 0)
	if age := now.Sub(updatedAt); age > maxAge {
		return nil, fmt.Errorf("stale price: last updated %s ago, exceeds max age %s", age.Truncate(time.Second), maxAge)
	}
	return r.answer, nil
}

// chainlinkMaxAge resolves an optional caller-supplied max age (seconds) to a
// duration, falling back to chainlinkDefaultMaxAge.
func chainlinkMaxAge(maxAgeSeconds []int) time.Duration {
	if len(maxAgeSeconds) > 0 && maxAgeSeconds[0] > 0 {
		return time.Duration(maxAgeSeconds[0]) * time.Second
	}
	return chainlinkDefaultMaxAge
}

// bigFromABI safely extracts a *big.Int from an ABI-decoded value.
func bigFromABI(v any) *big.Int {
	b, _ := v.(*big.Int)
	return b
}

// chainlinkLatestRoundData reads a Chainlink feed's latest round and returns its
// price, or an error if the feed is unreachable or the round is stale or invalid,
// so a bad feed fails the caller loudly instead of silently evaluating against
// outdated or zero data. See the note on exprEnv for why macros return errors
// rather than panicking. An optional second argument overrides the staleness
// window (in seconds).
func chainlinkLatestRoundData(tokenPair string, maxAgeSeconds ...int) (*big.Int, error) {
	if rpcConn == nil {
		return nil, fmt.Errorf("chainlink feed %s: rpc client is not configured", tokenPair)
	}
	output, err := QueryContract(
		rpcConn,
		common.HexToAddress(tokenPair),
		chainlinkABI,
		"latestRoundData",
	)
	if err != nil {
		return nil, fmt.Errorf("Error when querying contract through rpc. contract: %s. error: %w", tokenPair, err)
	}
	if len(output) < 5 {
		return nil, fmt.Errorf("chainlink feed %s: unexpected latestRoundData output (%d fields)", tokenPair, len(output))
	}

	answer, err := validateChainlinkRound(chainlinkRound{
		roundID:         bigFromABI(output[0]),
		answer:          bigFromABI(output[1]),
		updatedAt:       bigFromABI(output[3]),
		answeredInRound: bigFromABI(output[4]),
	}, chainlinkMaxAge(maxAgeSeconds), time.Now())
	if err != nil {
		return nil, fmt.Errorf("chainlink feed %s: %w", tokenPair, err)
	}
	return answer, nil
}

// bigOperands rejects a nil operand up front: goja passes a JS null or undefined
// as a nil *big.Int, and (*big.Int).Cmp dereferences both sides, so without this
// check bigCmp(null, x) would panic instead of failing.
func bigOperands(a *big.Int, b *big.Int) error {
	if a == nil || b == nil {
		return fmt.Errorf("big.Int comparison: nil operand (got null or undefined)")
	}
	return nil
}

func BigCmp(a *big.Int, b *big.Int) (int, error) {
	if err := bigOperands(a, b); err != nil {
		return 0, err
	}
	return a.Cmp(b), nil
}

func BigGt(a *big.Int, b *big.Int) (bool, error) {
	if err := bigOperands(a, b); err != nil {
		return false, err
	}
	return a.Cmp(b) > 0, nil
}

func BigLt(a *big.Int, b *big.Int) (bool, error) {
	if err := bigOperands(a, b); err != nil {
		return false, err
	}
	return a.Cmp(b) < 0, nil
}

// parseUnitRe matches a non-negative decimal number with an optional fractional
// part (e.g. "2621", "2621.99", "2621."). Validating the shape up front rejects
// signs, a leading "+" in the fraction, hex, and other malformed input that
// big.Int.SetString would otherwise silently accept. A bare ".5" (empty whole
// part) is intentionally rejected; task thresholds are written with a leading 0.
var parseUnitRe = regexp.MustCompile(`^[0-9]+(\.[0-9]*)?$`)

// parseUnitMaxDecimals caps 10^decimals so a user-supplied `decimal` can't force
// the aggregator/operator to materialize a huge integer. 10^78 ≈ 2^259, beyond
// anything on-chain needs (uint256 tops out near 10^77).
const parseUnitMaxDecimals = 78

// ParseUnit converts a non-negative decimal string into a fixed-point integer
// scaled by 10^decimals, matching the ethers.js parseUnits(value, decimals)
// convention. It accepts a fractional component (e.g. ParseUnit("2621.99", 8) ==
// 262199000000), which is what task-condition expressions need to build a price
// threshold to compare against chainlinkPrice(). Input that is not a non-negative
// decimal, a fraction with more digits than `decimals` (over-precision), or
// `decimals` beyond the on-chain range is rejected with an error (see the note on
// exprEnv for why macros return errors rather than panicking).
func ParseUnit(val string, decimal uint) (*big.Int, error) {
	if !parseUnitRe.MatchString(val) {
		return nil, fmt.Errorf("Parse error: %q is not a non-negative decimal number", val)
	}
	if decimal > parseUnitMaxDecimals {
		return nil, fmt.Errorf("Parse error: decimals %d out of range (max %d)", decimal, parseUnitMaxDecimals)
	}

	parts := strings.SplitN(val, ".", 2)
	whole, _ := new(big.Int).SetString(parts[0], 10) // regex guarantees a valid non-negative integer

	scale := new(big.Int).Exp(big.NewInt(10), new(big.Int).SetUint64(uint64(decimal)), nil)
	result := new(big.Int).Mul(whole, scale)

	// Add the fractional component, right-padded to `decimals` digits.
	if len(parts) == 2 && parts[1] != "" {
		frac := parts[1]
		if uint(len(frac)) > decimal {
			return nil, fmt.Errorf("Parse error: fractional part %q exceeds %d decimals in %q", frac, decimal, val)
		}
		fracVal, _ := new(big.Int).SetString(frac+strings.Repeat("0", int(decimal)-len(frac)), 10)
		result.Add(result, fracVal)
	}

	return result, nil
}

// ToBigInt parses a decimal or 0x-prefixed hex integer (up to 256 bits). It
// returns an error rather than a nil *big.Int for input it cannot parse: goja
// maps *big.Int to a JS BigInt by copying it (new(big.Int).Set), so a nil result
// panics inside the runtime instead of reaching the script as null.
func ToBigInt(val string) (*big.Int, error) {
	b, ok := ethmath.ParseBig256(val)
	if !ok {
		return nil, fmt.Errorf("toBigInt: %q is not a decimal or 0x-prefixed hex integer of at most 256 bits", val)
	}
	return b, nil
}

func (bi *Builtin) ToBigInt(val string) (*big.Int, error) {
	return ToBigInt(val)
}

func (bi *Builtin) ChainlinkLatestRoundData(tokenPair string, maxAgeSeconds ...int) (*big.Int, error) {
	return chainlinkLatestRoundData(tokenPair, maxAgeSeconds...)
}

func (bi *Builtin) BigCmp(a *big.Int, b *big.Int) (int, error) {
	return BigCmp(a, b)
}

func (bi *Builtin) BigGt(a *big.Int, b *big.Int) (bool, error) {
	return BigGt(a, b)
}

func (bi *Builtin) BigLt(a *big.Int, b *big.Int) (bool, error) {
	return BigLt(a, b)
}

func (bi *Builtin) ParseUnit(val string, decimal uint) (*big.Int, error) {
	return ParseUnit(val, decimal)
}

var (
	// exprEnv is the macro table registered into the goja VM for CustomCode
	// nodes, and into the expr VM through CompileExpression/RunExpressionQuery
	// (no production callers in this repo today; kept for expression tests). A
	// macro that can fail must return a trailing error, never panic: expr
	// reports a returned error as an evaluation failure and goja throws it as a
	// JS GoError that the script's try/catch can handle (uncaught, it fails the
	// node). A Go panic, by contrast, is not a JS exception — goja re-panics it
	// out of RunString past the script's try/catch, and the whole request dies
	// in the HTTP recover middleware with a 500.
	exprEnv = map[string]any{
		// bind and simular JS fetch api
		"fetch": Fetch,

		// macro to do IO from JS
		"readContractData": readContractData,

		// priceChainlink / chainlinkPrice historically used the deprecated
		// latestAnswer (no round or timestamp, so staleness was undetectable).
		// They now route through latestRoundData, which validates freshness.
		"priceChainlink":           chainlinkLatestRoundData,
		"chainlinkPrice":           chainlinkLatestRoundData,
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

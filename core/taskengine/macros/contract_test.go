package macros

import (
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/expr-lang/expr"
)

type answer struct {
	RoundId         big.Int
	Answer          big.Int
	StartedAt       uint64
	UpdatedAt       uint64
	AnsweredInRound big.Int
}

func TestQueryContract(t *testing.T) {
	rpc := os.Getenv("RPC_URL")
	if rpc == "" {
		// Use known Sepolia endpoint if available via env ETH_RPC_URL
		rpc = os.Getenv("ETH_RPC_URL")
	}
	if rpc == "" {
		t.Skip("Skipping TestQueryContract: RPC_URL/ETH_RPC_URL not set for Sepolia")
	}
	conn, err := ethclient.Dial(rpc)
	if err != nil {
		t.Skipf("Skipping TestQueryContract: failed to dial RPC %s: %v", rpc, err)
	}

	r, err := QueryContract(
		conn,
		// https://docs.chain.link/data-feeds/price-feeds/addresses?network=ethereum&page=1&search=et#sepolia-testnet
		// ETH-USD pair on sepolia
		common.HexToAddress("0x694AA1769357215DE4FAC081bf1f309aDC325306"),
		chainlinkABI,
		"latestRoundData",
	)

	if err != nil {
		t.Errorf("contract query error: %v", err)
	}

	t.Logf("contract query result: %v", r)
}

func TestExpression(t *testing.T) {
	rpc := os.Getenv("RPC_URL")
	if rpc == "" {
		rpc = os.Getenv("ETH_RPC_URL")
	}
	if rpc == "" {
		t.Skip("Skipping TestExpression: RPC_URL/ETH_RPC_URL not set for Sepolia")
	}
	SetRpc(rpc)

	p, e := CompileExpression(`priceChainlink("0x694AA1769357215DE4FAC081bf1f309aDC325306")`)
	if e != nil {
		t.Errorf("Compile expression error: %v", e)
	}

	r, e := expr.Run(p, exprEnv)
	if e != nil {
		t.Errorf("Run expr error: %v %v", e, r)
	}

	if r.(*big.Int).Cmp(big.NewInt(10)) <= 0 {
		t.Errorf("Invalid result data: %v", r)
	}

	t.Logf("Exp Run Result: %v", r.(*big.Int))

	match, e := RunExpressionQuery(`
		bigCmp(
		  priceChainlink("0x694AA1769357215DE4FAC081bf1f309aDC325306"),
		  toBigInt("2000")
		) > 0
	`)
	if e != nil {
		t.Errorf("Run expr error: %v %v", e, r)
	}
	if !match {
		t.Error("Evaluate error. Expected: true, received: false")
	}

	match, e = RunExpressionQuery(`
		bigCmp(
		  priceChainlink("0x694AA1769357215DE4FAC081bf1f309aDC325306"),
		  toBigInt("9262391230023")
		) > 0
	`)
	if e != nil {
		t.Errorf("Run expr error: %v %v", e, r)
	}
	if match {
		t.Error("Evaluate error. Expected: false, got: true")
	}
}

func TestExpressionDynamic(t *testing.T) {
	rpc := os.Getenv("RPC_URL")
	if rpc == "" {
		rpc = os.Getenv("ETH_RPC_URL")
	}
	if rpc == "" {
		t.Skip("Skipping TestExpressionDynamic: RPC_URL/ETH_RPC_URL not set for Sepolia")
	}
	SetRpc(rpc)

	// https://sepolia.etherscan.io/address/0x9aCb42Ac07C72cFc29Cd95d9DEaC807E93ada1F6#code
	match, e := RunExpressionQuery(`
		bigCmp(
		  readContractData(
		    "0x9aCb42Ac07C72cFc29Cd95d9DEaC807E93ada1F6",
			"0x0a79309b000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a",
			"retrieve",
			'[{"inputs":[{"internalType":"address","name":"addr","type":"address"}],"name":"retrieve","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]'
		  )[0],
		  toBigInt("2000")
		) > 0
	`)
	if e != nil {
		t.Errorf("Run expr error: %v %v", e, match)
	}
	if !match {
		t.Error("Evaluate error. Expected: true, received: false")
	}
}

func TestExpressionPanicWonCrash(t *testing.T) {
	rpcConn = nil
	p, e := CompileExpression(`priceChainlink("0x694AA1769357215DE4FAC081bf1f309aDC325306")`)
	if e != nil {
		t.Errorf("Compile expression error: %v", e)
	}

	r, e := expr.Run(p, exprEnv)
	if e == nil || r != nil {
		t.Errorf("Evaluate wrong. Expected: nil, got: %v", r)
	}

	t.Logf("Successfully recovered from VM crash")
}

package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeeLedger_GetOutstandingBalance_Empty(t *testing.T) {
	db := testutil.TestMustDB()

	logger, _ := sdklogging.NewZapLogger(sdklogging.Development)
	ledger := NewFeeLedger(db, logger)

	owner := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	balance, err := ledger.GetOutstandingBalance(owner)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), balance, "Empty ledger should return zero balance")
}

func TestFeeLedger_RecordValueFee(t *testing.T) {
	db := testutil.TestMustDB()

	logger, _ := sdklogging.NewZapLogger(sdklogging.Development)
	ledger := NewFeeLedger(db, logger)

	owner := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	record := &FeeRecord{
		ExecutionID:    "exec_001",
		TaskID:         "task_001",
		Owner:          owner.Hex(),
		Tier:           avsproto.ExecutionTier_EXECUTION_TIER_1.String(),
		TierPercentage: "0.03",
		TxValueWei:     "1000000000000000000", // 1 ETH
		FeeAmountWei:   "300000000000000",     // 0.0003 ETH (0.03%)
		Timestamp:      1700000000000,
		ChainID:        11155111,
	}

	err := ledger.RecordValueFee(record)
	require.NoError(t, err)

	// Check outstanding balance
	balance, err := ledger.GetOutstandingBalance(owner)
	require.NoError(t, err)

	expectedFee, _ := new(big.Int).SetString("300000000000000", 10)
	assert.Equal(t, expectedFee, balance, "Outstanding balance should equal the recorded fee")
}

func TestFeeLedger_RecordMultipleFees(t *testing.T) {
	db := testutil.TestMustDB()

	logger, _ := sdklogging.NewZapLogger(sdklogging.Development)
	ledger := NewFeeLedger(db, logger)

	owner := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// Record first fee
	err := ledger.RecordValueFee(&FeeRecord{
		ExecutionID:  "exec_001",
		TaskID:       "task_001",
		Owner:        owner.Hex(),
		FeeAmountWei: "100000000000000", // 0.0001 ETH
	})
	require.NoError(t, err)

	// Record second fee
	err = ledger.RecordValueFee(&FeeRecord{
		ExecutionID:  "exec_002",
		TaskID:       "task_001",
		Owner:        owner.Hex(),
		FeeAmountWei: "200000000000000", // 0.0002 ETH
	})
	require.NoError(t, err)

	// Balance should be sum of both
	balance, err := ledger.GetOutstandingBalance(owner)
	require.NoError(t, err)

	expectedTotal, _ := new(big.Int).SetString("300000000000000", 10)
	assert.Equal(t, expectedTotal, balance, "Outstanding balance should be sum of all fees")
}

func TestFeeLedger_CheckCreditLimit(t *testing.T) {
	db := testutil.TestMustDB()

	logger, _ := sdklogging.NewZapLogger(sdklogging.Development)
	ledger := NewFeeLedger(db, logger)

	owner := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// Credit limit: 500000000000000 wei (0.0005 ETH)
	creditLimit, _ := new(big.Int).SetString("500000000000000", 10)

	// No fees yet — within limit
	withinLimit, outstanding, err := ledger.CheckCreditLimit(owner, creditLimit)
	require.NoError(t, err)
	assert.True(t, withinLimit, "Should be within limit when no fees owed")
	assert.Equal(t, big.NewInt(0), outstanding)

	// Record fee that's under the limit
	err = ledger.RecordValueFee(&FeeRecord{
		ExecutionID:  "exec_001",
		Owner:        owner.Hex(),
		FeeAmountWei: "300000000000000", // 0.0003 ETH — under 0.0005 limit
	})
	require.NoError(t, err)

	withinLimit, outstanding, err = ledger.CheckCreditLimit(owner, creditLimit)
	require.NoError(t, err)
	assert.True(t, withinLimit, "Should be within limit when fees < credit limit")

	// Record another fee that pushes over the limit
	err = ledger.RecordValueFee(&FeeRecord{
		ExecutionID:  "exec_002",
		Owner:        owner.Hex(),
		FeeAmountWei: "300000000000000", // Total now 0.0006 ETH — over 0.0005 limit
	})
	require.NoError(t, err)

	withinLimit, outstanding, err = ledger.CheckCreditLimit(owner, creditLimit)
	require.NoError(t, err)
	assert.False(t, withinLimit, "Should exceed credit limit")

	expectedOutstanding, _ := new(big.Int).SetString("600000000000000", 10)
	assert.Equal(t, expectedOutstanding, outstanding)
}

func TestFeeLedger_SeparateOwners(t *testing.T) {
	db := testutil.TestMustDB()

	logger, _ := sdklogging.NewZapLogger(sdklogging.Development)
	ledger := NewFeeLedger(db, logger)

	owner1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	owner2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Record fee for owner1
	err := ledger.RecordValueFee(&FeeRecord{
		ExecutionID:  "exec_001",
		Owner:        owner1.Hex(),
		FeeAmountWei: "100000000000000",
	})
	require.NoError(t, err)

	// Record fee for owner2
	err = ledger.RecordValueFee(&FeeRecord{
		ExecutionID:  "exec_002",
		Owner:        owner2.Hex(),
		FeeAmountWei: "200000000000000",
	})
	require.NoError(t, err)

	// Balances should be separate
	balance1, err := ledger.GetOutstandingBalance(owner1)
	require.NoError(t, err)
	expected1, _ := new(big.Int).SetString("100000000000000", 10)
	assert.Equal(t, expected1, balance1)

	balance2, err := ledger.GetOutstandingBalance(owner2)
	require.NoError(t, err)
	expected2, _ := new(big.Int).SetString("200000000000000", 10)
	assert.Equal(t, expected2, balance2)
}

func TestConvertUSDToWei(t *testing.T) {
	priceService := &mockPriceService{} // Returns $2500 for Sepolia

	weiAmount, err := ConvertUSDToWei(0.02, priceService, 11155111)
	require.NoError(t, err)

	// $0.02 at $2500/ETH = 0.000008 ETH ≈ 8000000000000 Wei
	// Allow ±1 wei for floating point precision
	expectedWei, _ := new(big.Int).SetString("8000000000000", 10)
	diff := new(big.Int).Sub(expectedWei, weiAmount)
	diff.Abs(diff)
	assert.True(t, diff.Cmp(big.NewInt(1)) <= 0,
		"$0.02 at $2500/ETH should be ~8000000000000 Wei, got %s", weiAmount.String())
}

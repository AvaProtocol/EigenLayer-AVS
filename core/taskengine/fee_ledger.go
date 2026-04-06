package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
)

// FeeLedger tracks outstanding value fees per user in BadgerDB.
// Atomic fees (execution_fee + COGS) are collected in the UserOp.
// Value fees (% of tx value) are post-paid and tracked here.
type FeeLedger struct {
	db     storage.Storage
	logger sdklogging.Logger
	mu     sync.Mutex // Guards read-then-write sequences in RecordValueFee
}

// FeeLedgerEntry stores the running balance for a user's outstanding value fees.
type FeeLedgerEntry struct {
	// OutstandingWei is the total unpaid value fee balance (positive = owes money).
	// Stored as string for big.Int precision safety.
	OutstandingWei string `json:"outstanding_wei"`

	// LastUpdated is the timestamp of the last balance change (unix millis).
	LastUpdated int64 `json:"last_updated"`

	// TotalAccruedWei is the lifetime total of value fees charged (for reporting).
	TotalAccruedWei string `json:"total_accrued_wei"`

	// RecordCount is the number of fee records created.
	RecordCount int64 `json:"record_count"`
}

// FeeRecord is an individual value fee charge for audit trail.
type FeeRecord struct {
	ExecutionID    string `json:"execution_id"`
	TaskID         string `json:"task_id"`
	Owner          string `json:"owner"`
	Tier           string `json:"tier"`            // "EXECUTION_TIER_1", etc.
	TierPercentage string `json:"tier_percentage"` // "0.03"
	TxValueWei     string `json:"tx_value_wei"`    // Actual transaction value
	FeeAmountWei   string `json:"fee_amount_wei"`  // Computed fee
	Timestamp      int64  `json:"timestamp"`       // Unix millis
	ChainID        int64  `json:"chain_id"`
}

// NewFeeLedger creates a new fee ledger backed by the given storage.
func NewFeeLedger(db storage.Storage, logger sdklogging.Logger) *FeeLedger {
	return &FeeLedger{
		db:     db,
		logger: logger,
	}
}

// GetOutstandingBalance returns the current outstanding value fee balance for an owner.
// Returns zero if no ledger entry exists.
func (fl *FeeLedger) GetOutstandingBalance(owner common.Address) (*big.Int, error) {
	entry, err := fl.getLedgerEntry(owner)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return big.NewInt(0), nil
	}

	outstanding, ok := new(big.Int).SetString(entry.OutstandingWei, 10)
	if !ok {
		return nil, fmt.Errorf("failed to parse outstanding balance: %s", entry.OutstandingWei)
	}
	return outstanding, nil
}

// CheckCreditLimit returns whether the owner is within their credit limit.
// Returns (withinLimit, outstandingBalance, error).
func (fl *FeeLedger) CheckCreditLimit(owner common.Address, creditLimitWei *big.Int) (bool, *big.Int, error) {
	if creditLimitWei == nil {
		return true, big.NewInt(0), nil // No limit configured — always within limit
	}

	outstanding, err := fl.GetOutstandingBalance(owner)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get outstanding balance: %w", err)
	}

	// Outstanding > creditLimit means the user owes too much
	withinLimit := outstanding.Cmp(creditLimitWei) <= 0
	return withinLimit, outstanding, nil
}

// RecordValueFee records a value fee after successful execution.
// The mutex serializes the read-check-write sequence to prevent double-recording
// when concurrent calls arrive with the same ExecutionID.
func (fl *FeeLedger) RecordValueFee(record *FeeRecord) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	owner := common.HexToAddress(record.Owner)

	// Idempotency check: skip if this execution's fee was already recorded
	existingRecord, _ := fl.db.GetKey(FeeRecordKey(owner, record.ExecutionID))
	if existingRecord != nil {
		fl.logger.Debug("Fee record already exists, skipping", "execution_id", record.ExecutionID)
		return nil
	}

	feeAmount, ok := new(big.Int).SetString(record.FeeAmountWei, 10)
	if !ok {
		return fmt.Errorf("invalid fee amount: %s", record.FeeAmountWei)
	}

	// Get or create ledger entry
	entry, err := fl.getLedgerEntry(owner)
	if err != nil {
		return fmt.Errorf("failed to get ledger entry: %w", err)
	}
	if entry == nil {
		entry = &FeeLedgerEntry{
			OutstandingWei:  "0",
			TotalAccruedWei: "0",
			RecordCount:     0,
		}
	}

	// Update outstanding balance
	outstanding, _ := new(big.Int).SetString(entry.OutstandingWei, 10)
	if outstanding == nil {
		outstanding = big.NewInt(0)
	}
	outstanding.Add(outstanding, feeAmount)
	entry.OutstandingWei = outstanding.String()

	// Update total accrued
	totalAccrued, _ := new(big.Int).SetString(entry.TotalAccruedWei, 10)
	if totalAccrued == nil {
		totalAccrued = big.NewInt(0)
	}
	totalAccrued.Add(totalAccrued, feeAmount)
	entry.TotalAccruedWei = totalAccrued.String()

	entry.RecordCount++
	entry.LastUpdated = time.Now().UnixMilli()

	// Batch write: ledger entry + fee record
	ledgerData, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal ledger entry: %w", err)
	}

	recordData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal fee record: %w", err)
	}

	updates := map[string][]byte{
		string(FeeLedgerKey(owner)):                     ledgerData,
		string(FeeRecordKey(owner, record.ExecutionID)): recordData,
	}

	if err := fl.db.BatchWrite(updates); err != nil {
		return fmt.Errorf("failed to write fee ledger: %w", err)
	}

	fl.logger.Info("Recorded value fee",
		"owner", owner.Hex(),
		"execution_id", record.ExecutionID,
		"fee_wei", record.FeeAmountWei,
		"outstanding_wei", entry.OutstandingWei)

	return nil
}

// getLedgerEntry reads the ledger entry for an owner. Returns nil if not found.
func (fl *FeeLedger) getLedgerEntry(owner common.Address) (*FeeLedgerEntry, error) {
	data, err := fl.db.GetKey(FeeLedgerKey(owner))
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read fee ledger for %s: %w", owner.Hex(), err)
	}

	var entry FeeLedgerEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal fee ledger for %s: %w", owner.Hex(), err)
	}
	return &entry, nil
}

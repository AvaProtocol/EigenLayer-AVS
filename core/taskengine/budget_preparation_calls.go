package taskengine

import (
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum/common"
)

// Shared by prepare and recovery: the stored review, not the operation itself,
// defines the exact reset -> retirement -> install calls that may be signed.
func budgetPreparationCalls(wallet common.Address, resets []BudgetApprovalReset, retirements []*model.SessionPolicy, install []byte) ([]aa.Call, error) {
	if len(install) == 0 || len(resets)+len(retirements)+1 > ownerBatchMaxCalls {
		return nil, budgetInputError("the complete atomic budget batch must include an install and at most %d calls", ownerBatchMaxCalls)
	}
	calls := make([]aa.Call, 0, len(resets)+len(retirements)+1)
	for _, row := range resets {
		amount, err := parseBudgetAmount(row.Amount)
		if err != nil || amount.Sign() <= 0 || row.Token == (common.Address{}) || row.Spender == (common.Address{}) {
			return nil, fmt.Errorf("invalid approval reset in budget review")
		}
		data := append(common.FromHex(selectorApprove), common.LeftPadBytes(row.Spender.Bytes(), 32)...)
		data = append(data, make([]byte, 32)...)
		calls = append(calls, aa.Call{Target: row.Token, Value: big.NewInt(0), Data: data})
	}
	for _, retired := range retirements {
		if retired == nil || retired.Runner == nil || *retired.Runner != wallet || retired.Grant == nil {
			return nil, fmt.Errorf("invalid retirement in budget review")
		}
		uninstall, err := aa.SessionSignerUninstallFromInstall(retired.EntityID, retired.Grant.InstallCall)
		if err != nil {
			return nil, err
		}
		calls = append(calls, aa.Call{Target: wallet, Value: big.NewInt(0), Data: uninstall})
	}
	return append(calls, aa.Call{Target: wallet, Value: big.NewInt(0), Data: install}), nil
}

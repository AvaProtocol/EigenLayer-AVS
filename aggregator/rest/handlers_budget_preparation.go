package rest

import (
	"encoding/json"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

func (s *Server) GetWalletBudgetPreparation(ctx echo.Context, address generated.EthereumAddress, id generated.Ulid, params generated.GetWalletBudgetPreparationParams) error {
	user, wallet, err := s.policyAuth(ctx, OpGetWalletBudgetPreparation, address)
	if err != nil {
		return err
	}
	chainID := policyChainID(ctx, params.ChainId)
	record, err := s.engine.GetBudgetPolicyPreparation(user, wallet, chainID, id)
	if err != nil {
		return mapPolicyError(err)
	}
	out, err := budgetPreparationToAPI(record)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (s *Server) SubmitWalletBudgetPreparation(ctx echo.Context, address generated.EthereumAddress, _ generated.Ulid) error {
	if _, _, err := s.policyAuth(ctx, OpSubmitWalletBudgetPreparation, address); err != nil {
		return err
	}
	var body struct {
		Signature string `json:"signature"`
	}
	if err := decodeOwnerExecutionBody(ctx, &body); err != nil {
		return badRequest("POLICIES_BAD_BODY", "Invalid request body", err.Error())
	}
	if len(common.FromHex(body.Signature)) != 65 {
		return badRequest("POLICIES_BAD_SIGNATURE", "Invalid signature", "signature must be 65 bytes.")
	}
	// No signature is verified, stored or relayed until holds, reconciliation
	// and activation exist. In particular this never delegates to /executions.
	return mapPolicyError(&taskengine.BudgetPolicyError{Code: taskengine.BudgetHandoffRequiredCode, Detail: "Budget submission is not enabled until durable holds and activation are implemented."})
}

func budgetDiscoveryFromAPI(raw *generated.BudgetApprovalDiscovery) (*taskengine.BudgetApprovalDiscovery, error) {
	if raw == nil {
		return nil, nil
	}
	refused := func() (*taskengine.BudgetApprovalDiscovery, error) {
		return nil, &taskengine.BudgetPolicyError{Code: taskengine.BudgetDiscoveryRequiredCode, Detail: "Invalid approval discovery address."}
	}
	if !common.IsHexAddress(raw.Owner) || !common.IsHexAddress(raw.Wallet) {
		return refused()
	}
	out := &taskengine.BudgetApprovalDiscovery{Owner: common.HexToAddress(raw.Owner), Wallet: common.HexToAddress(raw.Wallet), ChainID: raw.ChainId, Source: string(raw.Source), FetchedAt: raw.FetchedAt, Complete: raw.Complete, Signature: common.FromHex(raw.Signature)}
	for _, target := range raw.AllowedContracts {
		if !common.IsHexAddress(target) {
			return refused()
		}
		out.AllowedContracts = append(out.AllowedContracts, common.HexToAddress(target))
	}
	for _, pair := range raw.Candidates {
		if !common.IsHexAddress(pair.Token) || !common.IsHexAddress(pair.Spender) {
			return refused()
		}
		out.Candidates = append(out.Candidates, taskengine.BudgetApprovalPair{Token: common.HexToAddress(pair.Token), Spender: common.HexToAddress(pair.Spender)})
	}
	return out, nil
}

func budgetPreparationToAPI(p *taskengine.BudgetPolicyPreparation) (generated.PreparedPolicy, error) {
	op := (*userop.UserOperationV07)(p.Operation)
	data, err := json.Marshal(op)
	if err != nil {
		return generated.PreparedPolicy{}, err
	}
	var operation generated.OwnerUserOperationV07
	if err := json.Unmarshal(data, &operation); err != nil {
		return generated.PreparedPolicy{}, err
	}
	id := p.ID
	hash := p.UserOpHash.Hex()
	entry := preset.EntryPointV07().Hex()
	method := generated.PolicySigningSigningMethod("personal_sign")
	policy := p.Policy
	budget := walletBudgetToAPI(p.Budget)
	resets := make([]generated.BudgetApprovalReset, 0, len(p.ApprovalResets))
	for _, row := range p.ApprovalResets {
		resets = append(resets, generated.BudgetApprovalReset{Token: row.Token.Hex(), Spender: row.Spender.Hex(), Amount: row.Amount})
	}
	out := generated.PreparedPolicy{PolicyId: policy.ID, ChainId: policy.ChainID, EntityId: int64(policy.EntityID), SessionSigner: policy.SessionSigner.Hex(), Deadline: int64(policy.Grant.Deadline), ValidUntil: policy.ValidUntil,
		PreparationId: &id, Budget: &budget, ApprovalResets: &resets, UnmeteredPermissions: &budget.UnmeteredPermissions,
		Signing: &generated.PolicySigning{Kind: generated.PolicySigningKind("owner_operation"), PreparationId: &id, UserOp: &operation, UserOpHash: &hash, MaxFeeWei: &p.MaxFeeWei, ExpiresAt: &p.ExpiresAt, SigningMethod: &method, EntryPoint: &entry}}
	// Reuse the ordinary row/change mapping without creating typed data or a
	// deferred digest. The stored operation is the sole signing payload.
	applySkillPrepare(&out, &taskengine.PreparedSessionGrant{Policy: policy, SkillChanges: &p.Draft.Changes, EchoPermissions: &p.Draft.Permissions})
	return out, nil
}

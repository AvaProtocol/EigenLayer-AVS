package taskengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestPolicyInputAndDeadlineFailuresKeepPublicValidationReasons(t *testing.T) {
	for _, name := range []string{"expiry range", "expired signature", "invalid policy ID", "empty agent label", "invalid cap", "malformed signature", "short signature"} {
		t.Run(name, func(t *testing.T) {
			n, _, _, owner, wallet := newPolicyTestEngine(t)
			in := SessionPolicyInput{Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Test", Permissions: testPermissions()}
			var err error
			var text string
			switch name {
			case "expiry range":
				in.ExpiresInSeconds = -1
				_, err = n.PrepareSessionPolicy(&model.User{Address: owner}, in)
				text = fmt.Sprintf("expiresInSeconds must be between 0 and %d", MaxSessionExpiresInSeconds)
			case "expired signature":
				deadline := uint64(time.Now().Add(-time.Minute).Unix())
				_, _, err = n.SubmitSessionPolicy(&model.User{Address: owner}, in, model.GenerateID(), 1, deadline, nil)
				text = fmt.Sprintf("the signing window (deadline %d) has expired; prepare the grant again", deadline)
			case "invalid policy ID":
				_, _, err = n.SubmitSessionPolicy(&model.User{Address: owner}, in, "invalid", 1, 0, nil)
				text = `policy id "invalid" is not a ULID`
			case "empty agent label":
				in.AgentLabel = ""
				_, err = n.PrepareSessionPolicy(&model.User{Address: owner}, in)
				text = "agent label is required"
			case "invalid cap":
				in.Permissions.SpendCap.Amount = "not an amount"
				_, err = n.PrepareSessionPolicy(&model.User{Address: owner}, in)
			case "malformed signature", "short signature":
				prepared, prepErr := n.PrepareSessionPolicy(&model.User{Address: owner}, in)
				require.NoError(t, prepErr)
				signature := make([]byte, 65)
				if name == "short signature" {
					signature = signature[:64]
				}
				_, _, err = n.SubmitSessionPolicy(&model.User{Address: owner}, in, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signature)
			}
			var validation *PolicyValidationError
			require.ErrorAs(t, err, &validation)
			if text != "" {
				require.Equal(t, text, validation.Error())
			}
		})
	}
}

func TestPolicyRecipientRPCFailureIsNotPublicValidation(t *testing.T) {
	n, _, _, owner, wallet := newPolicyTestEngine(t)
	recipient := common.HexToAddress("0x1234")
	cause := errors.New("private native-recipient RPC error")
	reads := 0
	_, err := n.PrepareSessionPolicyWithContext(context.Background(), &model.User{Address: owner}, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Test",
		Permissions: SessionPermissions{NativeRecipients: []*common.Address{&recipient}, NativeSpendCap: &model.NativeSpendCap{Amount: "1000"}, ValidUntilMs: time.Now().Add(time.Hour).UnixMilli(), CodeAt: func(common.Address) ([]byte, error) { reads++; return nil, cause }},
	})
	require.ErrorIs(t, err, cause)
	var validation *PolicyValidationError
	require.False(t, errors.As(err, &validation))
	require.Equal(t, 1, reads, "structural validation must not read the chain")
}

func TestMissingPolicySignerIsNotPublicValidation(t *testing.T) {
	n, _, _, owner, wallet := newPolicyTestEngine(t)
	n.config.SessionSignerPrivateKey = nil
	_, err := n.PrepareSessionPolicy(&model.User{Address: owner}, SessionPolicyInput{Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Test", Permissions: testPermissions()})
	require.ErrorContains(t, err, "session_signer_private_key")
	var validation *PolicyValidationError
	require.False(t, errors.As(err, &validation))
}

type budgetHoldReadFailure struct {
	storage.Storage
	cause error
}

func (s budgetHoldReadFailure) GetByPrefix([]byte) ([]*storage.KeyValueItem, error) {
	return nil, s.cause
}

func TestBudgetHoldStorageFailureIsPrivateAndPreservesCause(t *testing.T) {
	cause := errors.New("private storage failure")
	_, err := budgetHoldRows(budgetHoldReadFailure{cause: cause}, testPolicyChain, common.Address{}, common.Address{})
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.ErrorIs(t, err, cause)
	_, err = budgetHoldRows(nil, testPolicyChain, common.Address{}, common.Address{})
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
}

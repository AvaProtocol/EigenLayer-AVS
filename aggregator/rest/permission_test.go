package rest

import (
	"crypto/ed25519"
	"net/http"
	"testing"
)

// expectedPermissionLevels is the authorization policy matrix. Keep in sync
// with permissionMap and api/openapi.yaml security overrides.
var expectedPermissionLevels = map[string]Level{
	OpGetHealth:     LevelPublic,
	OpAuthExchange:  LevelPublic,
	OpListOperators: LevelPublic,

	OpGetToken: LevelPartnerRead,

	OpListWallets:  LevelPartnerWalletPreview,
	OpCreateWallet: LevelPartnerWalletPreview,

	OpSimulateWorkflow: LevelUser,
	OpRunNode:          LevelUser,
	OpRunTrigger:       LevelUser,

	OpCreateWorkflow:            LevelUser,
	OpListWorkflows:             LevelUser,
	OpGetWorkflow:               LevelUser,
	OpCancelWorkflow:            LevelUser,
	OpPauseWorkflow:             LevelUser,
	OpResumeWorkflow:            LevelUser,
	OpTriggerWorkflow:           LevelUser,
	OpEstimateWorkflowFees:      LevelUser,
	OpCountWorkflows:            LevelUser,
	OpListExecutions:            LevelUser,
	OpListExecutionsForWorkflow: LevelUser,
	OpGetExecution:              LevelUser,
	OpGetExecutionStatus:        LevelUser,
	OpSignalExecution:           LevelUser,
	OpStreamExecution:           LevelUser,
	OpCountExecutions:           LevelUser,
	OpExecutionStats:            LevelUser,
	OpListSecrets:               LevelUser,
	OpPutSecret:                 LevelUser,
	OpDeleteSecret:              LevelUser,
	OpUpdateWallet:              LevelUser,
	OpWithdrawWallet:            LevelUser,
	OpGetWalletNonce:            LevelUser,

	OpPrepareWalletPolicy: LevelUserRefusePartner,
	OpSubmitWalletPolicy:  LevelUserRefusePartner,
	OpListWalletPolicies:  LevelUserRefusePartner,
	OpGetWalletPolicy:     LevelUserRefusePartner,
	OpRevokeWalletPolicy:  LevelUserRefusePartner,
}

func TestPermissionMap_CoversAllOperationsAndLevels(t *testing.T) {
	for op, want := range expectedPermissionLevels {
		got, ok := permissionMap[op]
		if !ok {
			t.Errorf("permissionMap missing operation %q", op)
			continue
		}
		if got != want {
			t.Errorf("permissionMap[%q] = %q, want %q", op, got, want)
		}
	}
	if len(permissionMap) != len(expectedPermissionLevels) {
		t.Errorf("permissionMap has %d entries, expected %d", len(permissionMap), len(expectedPermissionLevels))
		for op := range permissionMap {
			if _, ok := expectedPermissionLevels[op]; !ok {
				t.Errorf("permissionMap has unexpected key %q", op)
			}
		}
	}
}

func TestEnsurePermission_UnmappedOp(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)
	_, err := s.ensurePermission(ctxWithAssertion(""), "notARealOperation")
	assertHTTPStatus(t, err, http.StatusInternalServerError)
}

func TestEnsurePermission_PartnerRead(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)

	t.Run("partner ok without user JWT", func(t *testing.T) {
		p, err := s.ensurePermission(ctxWithAssertion(signAssertion(t, priv, validClaims())), OpGetToken)
		if err != nil {
			t.Fatalf("expected success: %v", err)
		}
		if p.Level != LevelPartnerRead || p.PartnerID != "studio" {
			t.Fatalf("unexpected principal: %+v", p)
		}
		if p.User == nil || p.User.Address.Hex() != testPartnerSubject {
			t.Fatalf("expected user from sub, got %+v", p.User)
		}
	})

	t.Run("user JWT alone rejected", func(t *testing.T) {
		_, err := s.ensurePermission(ctxWithUser(testPartnerSubject), OpGetToken)
		assertHTTPStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("no credential rejected", func(t *testing.T) {
		_, err := s.ensurePermission(ctxWithAssertion(""), OpGetToken)
		assertHTTPStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("simulate scope not enough for read", func(t *testing.T) {
		sSim := newPartnerServer(t, pub, []string{"simulate"}, partnerStatusActive)
		c := validClaims()
		c["scope"] = "simulate"
		_, err := sSim.ensurePermission(ctxWithAssertion(signAssertion(t, priv, c)), OpGetToken)
		assertHTTPStatus(t, err, http.StatusForbidden)
	})
}

func TestEnsurePermission_SimulateRequiresUserJWT(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)

	t.Run("partner alone rejected", func(t *testing.T) {
		_, err := s.ensurePermission(ctxWithAssertion(signAssertion(t, priv, validClaims())), OpSimulateWorkflow)
		assertHTTPStatus(t, err, http.StatusUnauthorized)
		_, err = s.ensurePermission(ctxWithAssertion(signAssertion(t, priv, validClaims())), OpRunNode)
		assertHTTPStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("user JWT ok", func(t *testing.T) {
		p, err := s.ensurePermission(ctxWithUser(testPartnerSubject), OpSimulateWorkflow)
		if err != nil {
			t.Fatalf("expected success: %v", err)
		}
		if p.User.Address.Hex() != testPartnerSubject {
			t.Fatalf("got user %s", p.User.Address.Hex())
		}
	})
}

func TestEnsurePermission_WalletPreview(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)

	t.Run("partner with EOA sub", func(t *testing.T) {
		p, err := s.ensurePermission(ctxWithAssertion(signAssertion(t, priv, validClaims())), OpCreateWallet)
		if err != nil {
			t.Fatalf("expected success: %v", err)
		}
		if p.PartnerID != "studio" {
			t.Fatalf("expected partner id, got %q", p.PartnerID)
		}
	})

	t.Run("user JWT", func(t *testing.T) {
		const subject = "0x2222222222222222222222222222222222222222"
		p, err := s.ensurePermission(ctxWithUser(subject), OpListWallets)
		if err != nil {
			t.Fatalf("expected success: %v", err)
		}
		if p.User.Address.Hex() != subject {
			t.Fatalf("got %s", p.User.Address.Hex())
		}
	})

	t.Run("empty JWT subject not partner fallthrough", func(t *testing.T) {
		_, err := s.ensurePermission(ctxWithUser(""), OpListWallets)
		assertHTTPStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("zero address sub rejected", func(t *testing.T) {
		c := validClaims()
		c["sub"] = "0x0000000000000000000000000000000000000000"
		_, err := s.ensurePermission(ctxWithAssertion(signAssertion(t, priv, c)), OpListWallets)
		assertHTTPStatus(t, err, http.StatusBadRequest)
	})
}

func TestEnsurePermission_UserRefusePartner(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)

	// Partner header + user JWT still refused (categorical).
	ctx := ctxWithUser(testPartnerSubject)
	ctx.Request().Header.Set(partnerAssertionHeader, signAssertion(t, priv, validClaims()))
	_, err := s.ensurePermission(ctx, OpPrepareWalletPolicy)
	assertHTTPStatus(t, err, http.StatusForbidden)

	p, err := s.ensurePermission(ctxWithUser(testPartnerSubject), OpPrepareWalletPolicy)
	if err != nil {
		t.Fatalf("user-only should succeed: %v", err)
	}
	if p.Level != LevelUserRefusePartner {
		t.Fatalf("level %s", p.Level)
	}
}

func TestEnsurePermission_Public(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)
	p, err := s.ensurePermission(ctxWithAssertion(""), OpGetHealth)
	if err != nil {
		t.Fatalf("public must succeed: %v", err)
	}
	if p.Level != LevelPublic || p.User != nil {
		t.Fatalf("unexpected principal %+v", p)
	}
}

package rest

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// The /policies surface end to end at the HTTP layer: the guard, the
// prepare → sign → submit round trip (including proof that the returned
// typed data hashes to the returned digest — what the wallet signs is what
// submit verifies), and the secret-grade handling of grant material.

const policyTestChain = int64(11155111)

type policyTestRig struct {
	server   *Server
	ownerKey *ecdsa.PrivateKey
	owner    common.Address
	wallet   common.Address
}

func newPolicyRig(t *testing.T) *policyTestRig {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	cfg := testutil.GetAggregatorConfig()
	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	cfg.SmartWallet.ControllerPrivateKey = controllerKey
	cfg.SmartWallet.ChainID = policyTestChain
	engine := taskengine.New(db, cfg, nil, testutil.GetLogger())

	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	ownerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	wallet := common.HexToAddress("0x00000000000000000000000000000000000000b2")
	factory := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	require.NoError(t, taskengine.StoreWallet(db, policyTestChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &wallet, Factory: &factory, Salt: big.NewInt(0),
	}))

	return &policyTestRig{
		server:   &Server{engine: engine, logger: logger, config: cfg},
		ownerKey: ownerKey,
		owner:    owner,
		wallet:   wallet,
	}
}

// call builds an authed request, runs the handler, and returns the recorder.
func (r *policyTestRig) call(t *testing.T, body any, handler func(echo.Context) error, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wallets/x/policies", &buf)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: r.owner.Hex(), ChainID: policyTestChain})

	if err := handler(ctx); err != nil {
		// Match production behavior enough to assert on: structured errors
		// carry their own status.
		var httpErr *restmw.HTTPError
		if ok := asHTTPError(err, &httpErr); ok {
			rec.Code = httpErr.Status
			rec.Body.Reset()
			raw, _ := json.Marshal(httpErr)
			rec.Body.Write(raw)
		} else {
			t.Fatalf("handler returned an unstructured error: %v", err)
		}
	}
	return rec
}

func asHTTPError(err error, target **restmw.HTTPError) bool {
	for e := err; e != nil; {
		if he, ok := e.(*restmw.HTTPError); ok {
			*target = he
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func prepareBody(chainID int64) generated.PreparePolicyRequest {
	token := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	router := "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
	return generated.PreparePolicyRequest{
		ChainId:    generated.ChainId(chainID),
		AgentLabel: "TradingBot",
		AllowedActions: []generated.AllowedAction{
			{Target: generated.EthereumAddress(router), Selectors: []string{"0x04e45aaf"}},
			{Target: generated.EthereumAddress(token), Selectors: []string{"0x095ea7b3"}},
		},
		Erc20SpendCap:    generated.Erc20SpendCap{Token: generated.EthereumAddress(token), Amount: "500000000"},
		ExpiresInSeconds: int64((30 * 24 * time.Hour).Seconds()),
	}
}

func TestPoliciesRefusePartnerAssertionsEverywhere(t *testing.T) {
	rig := newPolicyRig(t)
	address := generated.EthereumAddress(rig.wallet.Hex())
	assertion := map[string]string{partnerAssertionHeader: "any-value"}

	handlers := map[string]func(echo.Context) error{
		"prepare": func(c echo.Context) error { return rig.server.PrepareWalletPolicy(c, address) },
		"submit":  func(c echo.Context) error { return rig.server.SubmitWalletPolicy(c, address) },
		"list": func(c echo.Context) error {
			return rig.server.ListWalletPolicies(c, address, generated.ListWalletPoliciesParams{})
		},
		"get": func(c echo.Context) error {
			return rig.server.GetWalletPolicy(c, address, "01JG2FE5MDVKBPHEG0PEYSDKAC", generated.GetWalletPolicyParams{})
		},
		"revoke": func(c echo.Context) error {
			return rig.server.RevokeWalletPolicy(c, address, "01JG2FE5MDVKBPHEG0PEYSDKAC", generated.RevokeWalletPolicyParams{})
		},
	}
	for name, h := range handlers {
		rec := rig.call(t, nil, h, assertion)
		require.Equal(t, http.StatusForbidden, rec.Code, "%s must refuse partner assertions", name)
		require.Contains(t, rec.Body.String(), "PARTNER_DELEGATION_UNSUPPORTED", "%s refusal must be explicit", name)
	}
}

func TestPoliciesPrepareSignSubmitOverHTTP(t *testing.T) {
	rig := newPolicyRig(t)
	address := generated.EthereumAddress(rig.wallet.Hex())

	// Prepare.
	rec := rig.call(t, prepareBody(policyTestChain), func(c echo.Context) error {
		return rig.server.PrepareWalletPolicy(c, address)
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var prepared generated.PreparedPolicy
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prepared))
	require.NotEmpty(t, prepared.PolicyId)
	require.EqualValues(t, 1, prepared.EntityId)

	// The returned typed data must hash to the returned digest — what the
	// owner's wallet displays and signs is exactly what submit verifies.
	rawTyped, err := json.Marshal(prepared.TypedData)
	require.NoError(t, err)
	var typed apitypes.TypedData
	require.NoError(t, json.Unmarshal(rawTyped, &typed))
	walletDigest, _, err := apitypes.TypedDataAndHash(typed)
	require.NoError(t, err)
	require.Equal(t, prepared.Digest, fmt.Sprintf("0x%x", walletDigest),
		"typed data and digest disagree; the wallet would sign something submit rejects")

	// Sign as the owner's wallet would.
	sig, err := crypto.Sign(walletDigest, rig.ownerKey)
	require.NoError(t, err)
	sig[64] += 27

	// Submit the echo.
	body := prepareBody(policyTestChain)
	submit := generated.SubmitPolicyRequest{
		ChainId:        body.ChainId,
		PolicyId:       prepared.PolicyId,
		EntityId:       prepared.EntityId,
		Deadline:       prepared.Deadline,
		ValidUntil:     prepared.ValidUntil,
		AgentLabel:     body.AgentLabel,
		AllowedActions: body.AllowedActions,
		Erc20SpendCap:  body.Erc20SpendCap,
		Signature:      fmt.Sprintf("0x%x", sig),
	}
	rec = rig.call(t, submit, func(c echo.Context) error {
		return rig.server.SubmitWalletPolicy(c, address)
	}, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var policy generated.SessionPolicy
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &policy))
	require.Equal(t, "pending", string(policy.Status))
	require.Equal(t, "500000000", policy.Erc20SpendCap.Amount)

	// Grant material is secret-grade: neither the submit response nor the
	// list may carry calldata, the carrier nonce, or the owner's signature.
	for _, marker := range []string{"install_call", "owner_signature", "carrier_nonce", "installCall", "ownerSignature"} {
		require.NotContains(t, rec.Body.String(), marker, "grant material leaked")
	}

	rec = rig.call(t, nil, func(c echo.Context) error {
		return rig.server.ListWalletPolicies(c, address, generated.ListWalletPoliciesParams{})
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), string(prepared.PolicyId))
	require.NotContains(t, rec.Body.String(), "ownerSignature")

	// Revoke: pending → deleted outright, no on-chain cleanup.
	rec = rig.call(t, nil, func(c echo.Context) error {
		return rig.server.RevokeWalletPolicy(c, address, prepared.PolicyId, generated.RevokeWalletPolicyParams{})
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var revoked generated.RevokePolicyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &revoked))
	require.Equal(t, "deleted", string(revoked.Status))
	require.False(t, revoked.OnChainCleanupRequired)
}

func TestPoliciesSubmitRejectsTamperedCapOverHTTP(t *testing.T) {
	rig := newPolicyRig(t)
	address := generated.EthereumAddress(rig.wallet.Hex())

	rec := rig.call(t, prepareBody(policyTestChain), func(c echo.Context) error {
		return rig.server.PrepareWalletPolicy(c, address)
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var prepared generated.PreparedPolicy
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prepared))

	sig, err := crypto.Sign(common.HexToHash(prepared.Digest).Bytes(), rig.ownerKey)
	require.NoError(t, err)
	sig[64] += 27

	body := prepareBody(policyTestChain)
	body.Erc20SpendCap.Amount = "999999999999" // not what the owner signed
	submit := generated.SubmitPolicyRequest{
		ChainId: body.ChainId, PolicyId: prepared.PolicyId, EntityId: prepared.EntityId,
		Deadline: prepared.Deadline, ValidUntil: prepared.ValidUntil,
		AgentLabel: body.AgentLabel, AllowedActions: body.AllowedActions,
		Erc20SpendCap: body.Erc20SpendCap, Signature: fmt.Sprintf("0x%x", sig),
	}
	rec = rig.call(t, submit, func(c echo.Context) error {
		return rig.server.SubmitWalletPolicy(c, address)
	}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.True(t, strings.Contains(rec.Body.String(), "POLICIES_BAD_SIGNATURE"),
		"a tampered echo must surface as a signature mismatch, got: %s", rec.Body.String())
}

// go-ethereum's TypedDataDomain has no `omitempty`, so a naive marshal emits
// name, version and salt as empty strings. EIP-712 verifiers read the domain
// through its TYPE, so the digest is unaffected and this looks harmless — but
// ethers v6 validates the object and rejects `salt: ""` outright with
// "invalid BytesLike value", which fails the grant before it is ever signed.
// Found by running the SDK's grant flow against a live gateway.
func TestTypedDataDomainCarriesOnlyDeclaredFields(t *testing.T) {
	rig := newPolicyRig(t)
	rec := rig.call(t, prepareBody(policyTestChain), func(c echo.Context) error {
		return rig.server.PrepareWalletPolicy(c, generated.EthereumAddress(rig.wallet.Hex()))
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var prepared generated.PreparedPolicy
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prepared))
	domain, ok := prepared.TypedData["domain"].(map[string]interface{})
	require.True(t, ok, "domain should be an object")

	for _, undeclared := range []string{"name", "version", "salt"} {
		require.NotContains(t, domain, undeclared,
			"domain carries %q, which EIP712Domain does not declare; strict clients reject it", undeclared)
	}
	for _, declared := range []string{"chainId", "verifyingContract"} {
		require.Contains(t, domain, declared)
	}
}

// grantOverHTTP runs prepare → sign → submit and returns the decoded response.
func (r *policyTestRig) grantOverHTTP(t *testing.T) generated.SubmitPolicyResponse {
	t.Helper()
	address := generated.EthereumAddress(r.wallet.Hex())

	rec := r.call(t, prepareBody(policyTestChain), func(c echo.Context) error {
		return r.server.PrepareWalletPolicy(c, address)
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var prepared generated.PreparedPolicy
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prepared))

	digest := common.FromHex(prepared.Digest)
	sig, err := crypto.Sign(digest, r.ownerKey)
	require.NoError(t, err)
	sig[64] += 27

	body := prepareBody(policyTestChain)
	rec = r.call(t, generated.SubmitPolicyRequest{
		ChainId:        body.ChainId,
		PolicyId:       prepared.PolicyId,
		EntityId:       prepared.EntityId,
		Deadline:       prepared.Deadline,
		ValidUntil:     prepared.ValidUntil,
		AgentLabel:     body.AgentLabel,
		AllowedActions: body.AllowedActions,
		Erc20SpendCap:  body.Erc20SpendCap,
		Signature:      fmt.Sprintf("0x%x", sig),
	}, func(c echo.Context) error {
		return r.server.SubmitWalletPolicy(c, address)
	}, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var submitted generated.SubmitPolicyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &submitted))
	return submitted
}

// Re-granting over HTTP replaces, and says what it replaced. The client does
// not have to revoke first, and does not have to ask for replacement.
func TestPoliciesSubmitReplacesThePreviousGrantOverHTTP(t *testing.T) {
	rig := newPolicyRig(t)
	address := generated.EthereumAddress(rig.wallet.Hex())

	first := rig.grantOverHTTP(t)
	require.NotNil(t, first.SupersededPolicyIds)
	require.Empty(t, *first.SupersededPolicyIds, "a first grant replaces nothing, and says so with an empty array")

	second := rig.grantOverHTTP(t)
	require.NotNil(t, second.SupersededPolicyIds)
	require.Equal(t, []generated.Ulid{first.Id}, *second.SupersededPolicyIds)

	// The list shows both records — one usable, one revoked for audit.
	rec := rig.call(t, nil, func(c echo.Context) error {
		return rig.server.ListWalletPolicies(c, address, generated.ListWalletPoliciesParams{})
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var list generated.SessionPolicyList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 2)

	status := map[generated.Ulid]string{}
	for _, item := range list.Items {
		status[item.Id] = string(item.Status)
	}
	require.Equal(t, "revoked", status[first.Id], "the replaced grant is retained, not deleted")
	require.Equal(t, "pending", status[second.Id])

	// Replacement must not leak grant material either.
	for _, marker := range []string{"install_call", "owner_signature", "carrier_nonce", "installCall", "ownerSignature"} {
		require.NotContains(t, rec.Body.String(), marker, "grant material leaked")
	}
}

// SubmitPolicyResponse is `allOf: [SessionPolicy, {supersededPolicyIds}]`, and
// oapi-codegen flattens that into a separate struct that submitPolicyToAPI has
// to fill in by hand. If a field is ever added to SessionPolicy and not copied
// across, the submit response would silently start omitting it.
func TestSubmitPolicyResponseCarriesEverySessionPolicyField(t *testing.T) {
	token := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	router := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")
	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

	// Every optional field populated, so none can pass by being absent in both.
	policy := &model.SessionPolicy{
		ID: "01legacygrantaaaaaaaaaaaaa", Owner: &owner, Runner: &wallet,
		ChainID: policyTestChain, EntityID: 7, SessionSigner: &signer,
		AgentLabel: "TradingBot", Justification: "because",
		AllowedActions: []model.AllowedAction{
			{Target: &router, Selectors: []string{"0x04e45aaf"}},
			{Target: &token, Selectors: []string{"0x095ea7b3"}},
		},
		ERC20SpendCap: &model.ERC20SpendCap{Token: &token, Amount: "500000000", GrantedCap: "500000000"},
		ValidUntil:    1785541743000,
		Status:        model.SessionPolicyPending,
		CreatedAt:     1785441743000,
	}

	asPolicy, err := json.Marshal(policyToAPI(policy))
	require.NoError(t, err)
	asSubmit, err := json.Marshal(submitPolicyToAPI(policy, []string{"01replacedgrantaaaaaaaaaa"}))
	require.NoError(t, err)

	var policyFields, submitFields map[string]any
	require.NoError(t, json.Unmarshal(asPolicy, &policyFields))
	require.NoError(t, json.Unmarshal(asSubmit, &submitFields))

	require.NotEmpty(t, policyFields)
	for name, want := range policyFields {
		got, ok := submitFields[name]
		require.True(t, ok, "submit response drops SessionPolicy field %q — copy it in submitPolicyToAPI", name)
		require.Equal(t, want, got, "submit response disagrees with the policy on %q", name)
	}
	require.Equal(t, []any{"01replacedgrantaaaaaaaaaa"}, submitFields["supersededPolicyIds"])
}

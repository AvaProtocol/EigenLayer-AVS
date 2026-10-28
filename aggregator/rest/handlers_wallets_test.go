package rest

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// GET /wallets is per chain, because the records are. These pin the routing:
// ?chainId decides, the JWT aud decides when it is absent, and neither chain
// can see the other's wallets.

const (
	walletTestChain  = int64(11155111)
	walletOtherChain = int64(84532)
)

type walletTestRig struct {
	server *Server
	db     storage.Storage
	owner  common.Address
	// onDefault and onOther are stored on walletTestChain and
	// walletOtherChain respectively — one per chain, so any response
	// identifies the chain it was listed from.
	onDefault common.Address
	onOther   common.Address
}

func newWalletRig(t *testing.T) *walletTestRig {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	cfg := testutil.GetAggregatorConfig()
	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	cfg.SmartWallet.ControllerPrivateKey = controllerKey
	cfg.SmartWallet.ChainID = walletTestChain
	otherChainWallet := *cfg.SmartWallet
	otherChainWallet.ChainID = walletOtherChain
	cfg.IsGateway = true
	cfg.Chains = []*config.ChainConfig{
		{ChainID: walletTestChain, Name: "sepolia", SmartWallet: cfg.SmartWallet},
		{ChainID: walletOtherChain, Name: "base-sepolia", SmartWallet: &otherChainWallet},
	}
	engine := taskengine.New(db, cfg, nil, testutil.GetLogger())

	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	ownerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	factory := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	onDefault := common.HexToAddress("0x00000000000000000000000000000000000000d1")
	onOther := common.HexToAddress("0x00000000000000000000000000000000000000e1")
	require.NoError(t, taskengine.StoreWallet(db, walletTestChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &onDefault, Factory: &factory, Salt: big.NewInt(100),
	}))
	require.NoError(t, taskengine.StoreWallet(db, walletOtherChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &onOther, Factory: &factory, Salt: big.NewInt(200),
	}))

	return &walletTestRig{
		server:    &Server{engine: engine, logger: logger, config: cfg},
		db:        db,
		owner:     owner,
		onDefault: onDefault,
		onOther:   onOther,
	}
}

// list runs GET /wallets with the given JWT audience and ?chainId, and
// returns the set of addresses it came back with.
func (r *walletTestRig) list(t *testing.T, audChainID int64, chainIDQuery *int64) map[string]bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wallets", nil)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: r.owner.Hex(), ChainID: audChainID})

	var params generated.ListWalletsParams
	if chainIDQuery != nil {
		q := generated.ChainIdQuery(*chainIDQuery)
		params.ChainId = &q
	}
	require.NoError(t, r.server.ListWallets(ctx, params))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var out generated.WalletList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	got := map[string]bool{}
	for _, w := range out.Data {
		got[strings.ToLower(string(w.Address))] = true
	}
	return got
}

func TestListWalletsUsesQueryChainIdOverJwtAud(t *testing.T) {
	rig := newWalletRig(t)
	onDefault := strings.ToLower(rig.onDefault.Hex())
	onOther := strings.ToLower(rig.onOther.Hex())

	// No query: the JWT aud decides, so this is the aud chain's listing.
	byAud := rig.list(t, walletTestChain, nil)
	require.True(t, byAud[onDefault], "the aud chain's wallet must be listed")
	require.False(t, byAud[onOther], "another chain's wallet must not leak in")

	// The query overrides the aud — the Studio exit case: one token, both
	// chains, no second signature.
	other := walletOtherChain
	byQuery := rig.list(t, walletTestChain, &other)
	require.True(t, byQuery[onOther], "?chainId must reach the named chain's wallets")
	require.False(t, byQuery[onDefault], "?chainId must not also return the aud chain's wallets")

	// And it works in the other direction, so the query is genuinely
	// deciding rather than the aud happening to agree.
	def := walletTestChain
	backAgain := rig.list(t, walletOtherChain, &def)
	require.True(t, backAgain[onDefault])
	require.False(t, backAgain[onOther])
}

func TestUpdateWalletHidesEOA7702NotSaltZeroDerived(t *testing.T) {
	rig := newWalletRig(t)
	factory := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	saltZero := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	require.NoError(t, taskengine.StoreWallet(rig.db, walletTestChain, rig.owner, &model.SmartWallet{
		Owner: &rig.owner, Address: &saltZero, Factory: &factory, Salt: big.NewInt(0),
	}))
	require.NoError(t, taskengine.StoreEOA7702Wallet(rig.db, walletTestChain, rig.owner, config.SMA7702Delegate()))

	body := `{"isHidden":true}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/wallets/"+rig.owner.Hex(), strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.SetPath("/api/v1/wallets/:address")
	ctx.SetParamNames("address")
	ctx.SetParamValues(rig.owner.Hex())
	ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: rig.owner.Hex(), ChainID: walletTestChain})

	require.NoError(t, rig.server.UpdateWallet(ctx, generated.EthereumAddress(rig.owner.Hex())))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out generated.Wallet
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, strings.ToLower(rig.owner.Hex()), strings.ToLower(string(out.Address)),
		"PATCH must return the EOA, not the salt-0 derived runner")
	require.NotNil(t, out.Kind)
	require.Equal(t, generated.WalletKind("eoa_7702"), *out.Kind)
	require.NotNil(t, out.IsHidden)
	require.True(t, *out.IsHidden)

	eoaRow, err := taskengine.GetWallet(rig.db, walletTestChain, rig.owner, rig.owner.Hex())
	require.NoError(t, err)
	require.True(t, eoaRow.IsHidden)
	derived, err := taskengine.GetWallet(rig.db, walletTestChain, rig.owner, saltZero.Hex())
	require.NoError(t, err)
	require.False(t, derived.IsHidden, "hiding the EOA must not hide the salt-0 derived runner")
}

// nonceKeyRecorder answers GetEntryPointNonce the way EntryPoint v0.7 does,
// key<<64 | sequence, and records the key it was asked for.
type nonceKeyRecorder struct {
	taskengine.ChainStateReader
	key *big.Int
}

func (r *nonceKeyRecorder) GetEntryPointNonce(_ context.Context, _ common.Address, key *big.Int) (*big.Int, error) {
	r.key = new(big.Int).Set(key)
	return new(big.Int).Or(new(big.Int).Lsh(key, 64), big.NewInt(7)), nil
}

// registerNonceReaders gives each rig chain its own recorder, so a test can
// tell which chain was read. Nothing else in this package registers readers,
// so cleanup clears the registry and restores what was there.
func registerNonceReaders(t *testing.T) map[int64]*nonceKeyRecorder {
	t.Helper()
	readers := map[int64]*nonceKeyRecorder{}
	prev := map[uint64]taskengine.ChainStateReader{}
	for _, chainID := range []int64{walletTestChain, walletOtherChain} {
		readers[chainID] = &nonceKeyRecorder{}
		prev[uint64(chainID)] = taskengine.GetChainStateReaderForChain(uint64(chainID))
		taskengine.RegisterChainStateReader(uint64(chainID), readers[chainID])
	}
	t.Cleanup(func() {
		taskengine.ClearChainStateReaderRegistry()
		for chainID, previous := range prev {
			taskengine.RegisterChainStateReader(chainID, previous)
		}
	})
	return readers
}

// getNonce runs GET /wallets/{address}:getNonce with the given JWT audience
// and ?chainId.
func (r *walletTestRig) getNonce(t *testing.T, wallet common.Address, audChainID int64, chainIDQuery *int64) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wallets/"+wallet.Hex()+":getNonce", nil)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: r.owner.Hex(), ChainID: audChainID})

	var params generated.GetWalletNonceParams
	if chainIDQuery != nil {
		q := generated.ChainIdQuery(*chainIDQuery)
		params.ChainId = &q
	}
	return rec, r.server.GetWalletNonce(ctx, generated.EthereumAddress(wallet.Hex()), params)
}

func requireWalletNotFound(t *testing.T, err error) {
	t.Helper()
	var httpErr *restmw.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusNotFound, httpErr.Status)
}

// The owner signs through the fallback signer, which Modular Account v2
// selects with nonce key 0x1. Key 0 selects no validation, so a nonce read
// there builds an operation the account will not validate.
func TestGetWalletNonceReadsOwnerValidationKey(t *testing.T) {
	rig := newWalletRig(t)
	reader := registerNonceReaders(t)[walletTestChain]

	rec, err := rig.getNonce(t, rig.onDefault, walletTestChain, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, reader.key)
	require.Equal(t, "1", reader.key.String(), "must read the fallback signer's nonce key")

	var out generated.NonceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "18446744073709551623", out.Nonce, "the full nonce, 1<<64 | 7, goes in userOp.nonce as is")
}

// The ownership check reads the record on the chain whose nonce is read, not
// the gateway's default chain.
func TestGetWalletNonceChecksOwnershipOnTheReadChain(t *testing.T) {
	rig := newWalletRig(t)
	registerNonceReaders(t)
	other := walletOtherChain
	def := walletTestChain

	// Registered only on the other chain: readable there via ?chainId,
	// though the aud is the default chain.
	rec, err := rig.getNonce(t, rig.onOther, walletTestChain, &other)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// And via the aud alone.
	_, err = rig.getNonce(t, rig.onOther, walletOtherChain, nil)
	require.NoError(t, err)

	// A default-chain record does not open another chain's nonce.
	_, err = rig.getNonce(t, rig.onDefault, walletTestChain, &other)
	requireWalletNotFound(t, err)

	// Nor the reverse.
	_, err = rig.getNonce(t, rig.onOther, walletOtherChain, &def)
	requireWalletNotFound(t, err)
}

// An unconfigured chain id resolves to the default chain, as in GET /wallets,
// for both the ownership check and the nonce read.
func TestGetWalletNonceUnconfiguredChainUsesDefault(t *testing.T) {
	rig := newWalletRig(t)
	readers := registerNonceReaders(t)
	unconfigured := int64(999999)

	rec, err := rig.getNonce(t, rig.onDefault, walletTestChain, &unconfigured)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, readers[walletTestChain].key, "the nonce must be read on the default chain")
	require.Nil(t, readers[walletOtherChain].key)

	// The same holds for an unconfigured aud.
	_, err = rig.getNonce(t, rig.onDefault, unconfigured, nil)
	require.NoError(t, err)

	// The default chain holds no record of the other chain's wallet.
	_, err = rig.getNonce(t, rig.onOther, walletTestChain, &unconfigured)
	requireWalletNotFound(t, err)
}

// A delegated recipient's refusal keeps its own code on withdraw, and a code
// read the chain did not answer stays the gRPC Unavailable it was.
func TestMapWithdrawErrorKeepsRecipientCodes(t *testing.T) {
	for _, code := range []string{
		taskengine.SessionPolicyRecipientNotAcknowledgedCode,
		taskengine.SessionPolicyRecipientDelegateChangedCode,
		taskengine.SessionPolicyRecipientNotEOACode,
	} {
		mapped := mapWithdrawError(status.Error(codes.InvalidArgument, code+": native recipient 0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557"))
		var httpErr *restmw.HTTPError
		require.ErrorAs(t, mapped, &httpErr, code)
		require.Equal(t, http.StatusBadRequest, httpErr.Status)
		require.Equal(t, code, httpErr.Code)
	}

	unreadable := status.Error(codes.Unavailable, taskengine.SessionPolicyRecipientCodeUnreadableCode+": cannot read code at native recipient 0xR: rpc timeout")
	require.Equal(t, unreadable, mapWithdrawError(unreadable))
}

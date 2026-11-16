package rest

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func inventoryContext(owner common.Address, chainID int64, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wallets", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: owner.Hex(), ChainID: chainID})
	return ctx, rec
}

func TestAllocateWalletThroughMountedHTTPRouter(t *testing.T) {
	rig := newWalletRig(t)
	rig.owner = common.HexToAddress("0x7777777777777777777777777777777777777777")
	rig.server.config.JwtSecret = []byte("wallet-routing-test-secret")
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &jwt.RegisteredClaims{
		Issuer: auth.Issuer, Subject: rig.owner.Hex(),
		Audience: jwt.ClaimStrings{"11155111"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(rig.server.config.JwtSecret)
	require.NoError(t, err)
	e := echo.New()
	rig.server.Mount(e)
	call := func(authenticated, key bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/wallets:allocate", strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if authenticated {
			req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
		}
		if key {
			req.Header.Set("Idempotency-Key", "mounted-allocation")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusUnauthorized, call(false, true).Code)
	require.Equal(t, http.StatusBadRequest, call(true, false).Code)
	first := call(true, true)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	var wallet generated.Wallet
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &wallet))
	require.NotNil(t, wallet.WalletId)
	require.Equal(t, "0", wallet.Salt)
	replay := call(true, true)
	require.Equal(t, http.StatusCreated, replay.Code)
	var replayed generated.Wallet
	require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &replayed))
	require.Equal(t, wallet.WalletId, replayed.WalletId)
}
func TestGlobalWalletInventoryHideCreateRestoreHTTP(t *testing.T) {
	rig := newWalletRig(t)
	rig.owner = common.HexToAddress("0x4444444444444444444444444444444444444444")
	rig.server.config.WalletInventory = &config.WalletInventoryConfig{MaxActiveWalletsPerOwner: 3}
	var zero string
	for _, salt := range []string{"0", "1", "2"} {
		ctx, rec := inventoryContext(rig.owner, walletTestChain, `{"salt":"`+salt+`"}`)
		require.NoError(t, rig.server.CreateWallet(ctx))
		require.Equal(t, http.StatusCreated, rec.Code)
		var w generated.Wallet
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &w))
		require.NotNil(t, w.WalletId)
		if salt == "0" {
			zero = string(w.Address)
		}
	}
	ctx, _ := inventoryContext(rig.owner, walletOtherChain, `{"salt":"4"}`)
	err := rig.server.CreateWallet(ctx)
	var quota *restmw.HTTPError
	require.ErrorAs(t, err, &quota)
	require.Equal(t, "WALLETS_LIMIT_REACHED", quota.Code)
	ctx, _ = inventoryContext(rig.owner, walletOtherChain, `{"isHidden":true}`)
	require.NoError(t, rig.server.UpdateWallet(ctx, generated.EthereumAddress(zero)))
	ctx, rec := inventoryContext(rig.owner, walletOtherChain, `{"salt":"4"}`)
	require.NoError(t, rig.server.CreateWallet(ctx))
	require.Equal(t, http.StatusCreated, rec.Code)
	ctx, _ = inventoryContext(rig.owner, walletTestChain, `{"isHidden":false}`)
	err = rig.server.UpdateWallet(ctx, generated.EthereumAddress(zero))
	require.ErrorAs(t, err, &quota)
	for _, chain := range []int64{walletTestChain, walletOtherChain} {
		ctx, rec = inventoryContext(rig.owner, chain, "")
		require.NoError(t, rig.server.ListWallets(ctx, generated.ListWalletsParams{}))
		var list generated.WalletList
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		require.Len(t, list.Data, 4)
		require.NotNil(t, list.Usage)
		require.Equal(t, 3, list.Usage.ActiveCount)
		require.Equal(t, 3, list.Usage.ActiveLimit)
		require.True(t, *list.Data[0].IsHidden)
	}
}
func TestAllocateWalletOwnerAuthAndReplayHTTP(t *testing.T) {
	rig := newWalletRig(t)
	rig.owner = common.HexToAddress("0x5555555555555555555555555555555555555555")
	rig.server.config.WalletInventory = &config.WalletInventoryConfig{MaxActiveWalletsPerOwner: 3}
	params := generated.AllocateWalletParams{IdempotencyKey: "wallet-button-1"}
	ctx, rec := inventoryContext(rig.owner, walletTestChain, `{}`)
	require.NoError(t, rig.server.AllocateWallet(ctx, params))
	var first generated.Wallet
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	require.Equal(t, "0", first.Salt)
	ctx, _ = inventoryContext(rig.owner, walletTestChain, `{"isHidden":true}`)
	require.NoError(t, rig.server.UpdateWallet(ctx, first.Address))
	ctx, rec = inventoryContext(rig.owner, walletOtherChain, `{}`)
	require.NoError(t, rig.server.AllocateWallet(ctx, params))
	var replay generated.Wallet
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &replay))
	require.Equal(t, first.Address, replay.Address)
	require.True(t, *replay.IsHidden)
	ctx, _ = inventoryContext(rig.owner, walletTestChain, `{}`)
	ctx.Set("auth.user", nil)
	require.Error(t, rig.server.AllocateWallet(ctx, params))
	// A known global identity resolves its exact salt on an unused local network.
	stored, err := rig.server.engine.StoredWallet(walletOtherChain, rig.owner, string(first.Address))
	require.NoError(t, err)
	require.Equal(t, "0", stored.Salt.String())
	_, err = rig.server.engine.ListWallets(&model.User{Address: rig.owner, ChainID: walletOtherChain}, &avsproto.ListWalletReq{})
	require.NoError(t, err)
	require.Equal(t, LevelUser, permissionMap[OpAllocateWallet])
}

func TestMigratedWalletIdentityConflictHTTP(t *testing.T) {
	rig := newWalletRig(t)
	owner := common.HexToAddress("0x6666666666666666666666666666666666666666")
	factory := aa.MAv2FactoryAddress()
	for i, chain := range []int64{walletTestChain, walletOtherChain} {
		address := common.BigToAddress(big.NewInt(int64(100 + i)))
		wallet := &model.SmartWallet{Owner: &owner, Address: &address, Factory: &factory, Salt: big.NewInt(8)}
		raw, err := wallet.ToJSON()
		require.NoError(t, err)
		require.NoError(t, rig.db.Set([]byte(taskengine.WalletStorageKey(chain, owner, address.Hex())), raw))
	}
	_, err := walletinventory.Migrate(rig.db)
	require.NoError(t, err)
	ctx, rec := inventoryContext(owner, walletTestChain, "")
	require.NoError(t, rig.server.ListWallets(ctx, generated.ListWalletsParams{}))
	var list generated.WalletList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Data, 2)
	require.NotNil(t, list.Data[0].WalletId)
	require.NotNil(t, list.Data[1].WalletId)
	require.NotEqual(t, *list.Data[0].WalletId, *list.Data[1].WalletId)
	ctx, _ = inventoryContext(owner, walletOtherChain, `{"salt":"9"}`)
	err = rig.server.CreateWallet(ctx)
	var conflict *restmw.HTTPError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, http.StatusConflict, conflict.Status)
	require.Equal(t, "WALLET_IDENTITY_CONFLICT", conflict.Code)
	ctx, _ = inventoryContext(owner, walletTestChain, `{}`)
	err = rig.server.AllocateWallet(ctx, generated.AllocateWalletParams{IdempotencyKey: "conflict"})
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, http.StatusConflict, conflict.Status)
	require.Equal(t, "WALLET_IDENTITY_CONFLICT", conflict.Code)
}

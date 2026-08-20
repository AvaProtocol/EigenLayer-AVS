package rest

import (
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

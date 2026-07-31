package aggregator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

const (
	testPolicyID = "bf905871-55d7-4197-a020-000000000000"
	testSecret   = "s3cr3t-webhook-token"
)

func newWebhookAggregator(t *testing.T) (*Aggregator, func()) {
	t.Helper()
	db := testutil.TestMustDB()
	agg := &Aggregator{
		logger: testutil.GetLogger(),
		db:     db,
		config: &config.Config{
			FeeRates: &config.FeeRatesConfig{CreditLimitUSD: 0},
		},
	}
	return agg, func() { db.Close() }
}

// post drives the handler directly so the test covers the decision logic
// rather than Echo's routing.
func post(t *testing.T, agg *Aggregator, cfg gasManagerWebhookConfig, body string) (int, bool) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, gasManagerWebhookPath, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := agg.handleGasManagerWebhook(e.NewContext(req, rec), cfg); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	var resp gasManagerWebhookResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return rec.Code, resp.Approved
}

func bodyFor(sender string, chainID int64, policyID, webhookData string) string {
	return fmt.Sprintf(
		`{"userOperation":{"sender":%q},"policyId":%q,"chainId":%d,"webhookData":%q}`,
		sender, policyID, chainID, webhookData)
}

// storeWallet writes a wallet record so the reverse lookup can find it.
func storeWallet(t *testing.T, agg *Aggregator, chainID int64, owner, wallet common.Address) {
	t.Helper()
	key := taskengine.WalletStorageKey(chainID, owner, wallet.Hex())
	if err := agg.db.Set([]byte(key), []byte("{}")); err != nil {
		t.Fatalf("seeding wallet: %v", err)
	}
}

// Every rejection path must answer 200 with approved:false. A non-200 would
// also deny at the platform level, but then a deliberate refusal is
// indistinguishable from a crash.
func TestGasManagerWebhook_DeniesAndAlwaysAnswers200(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	storeWallet(t, agg, 11155111, owner, wallet)

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID, Secret: testSecret}

	tests := []struct {
		name string
		body string
	}{
		{"malformed body", `{not json`},
		{"wrong policy id", bodyFor(wallet.Hex(), 11155111, "someone-elses-policy", testSecret)},
		{"empty policy id", bodyFor(wallet.Hex(), 11155111, "", testSecret)},
		{"wrong webhook secret", bodyFor(wallet.Hex(), 11155111, testPolicyID, "guessed")},
		{"missing webhook secret", bodyFor(wallet.Hex(), 11155111, testPolicyID, "")},
		{"sender not an address", bodyFor("not-an-address", 11155111, testPolicyID, testSecret)},
		{"sender empty", bodyFor("", 11155111, testPolicyID, testSecret)},
		{"unknown smart wallet", bodyFor("0x000000000000000000000000000000000000dEaD", 11155111, testPolicyID, testSecret)},
		{"known wallet but wrong chain", bodyFor(wallet.Hex(), 8453, testPolicyID, testSecret)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, approved := post(t, agg, cfg, tt.body)
			if status != http.StatusOK {
				t.Errorf("status = %d, want 200 (a non-200 hides the reason from logs)", status)
			}
			if approved {
				t.Error("approved = true, want false — this path must fail closed")
			}
		})
	}
}

func TestGasManagerWebhook_ApprovesKnownWalletWithinLimit(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	storeWallet(t, agg, 11155111, owner, wallet)

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID, Secret: testSecret}
	status, approved := post(t, agg, cfg, bodyFor(wallet.Hex(), 11155111, testPolicyID, testSecret))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !approved {
		t.Error("approved = false, want true for a known wallet with no outstanding fees")
	}
}

// An empty configured secret disables the check, so the webhook can be brought
// up before the sponsorship caller is taught to send webhookData. It must not
// accidentally start accepting *any* secret when one IS configured — covered
// by the deny table above.
func TestGasManagerWebhook_EmptySecretSkipsTheCheck(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832fea1a2d56091c48493c788")
	wallet := common.HexToAddress("0x71c8f4d7d5291edcb3a081802e7efb2788bd232e")
	storeWallet(t, agg, 11155111, owner, wallet)

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID, Secret: ""}
	_, approved := post(t, agg, cfg, bodyFor(wallet.Hex(), 11155111, testPolicyID, "anything at all"))
	if !approved {
		t.Error("approved = false; an empty configured secret should skip the comparison")
	}
}

// Storage faults must deny rather than pass through.
func TestGasManagerWebhook_DeniesWhenStorageUnavailable(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()
	agg.db = nil

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID}
	status, approved := post(t, agg, cfg,
		bodyFor("0x981e18d5aade83620a6bd21990b5da0c797e1e5b", 11155111, testPolicyID, ""))
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if approved {
		t.Error("approved = true with no storage; must fail closed")
	}
}

// A USD credit limit needs the price service to convert to wei. Without it we
// cannot judge the limit, and guessing would mean sponsoring an owner who may
// be far past it.
func TestGasManagerWebhook_DeniesWhenUSDLimitCannotBePriced(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()
	agg.config.FeeRates.CreditLimitUSD = 20 // > 0 requires the price service
	agg.priceService = nil

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	storeWallet(t, agg, 11155111, owner, wallet)

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID}
	_, approved := post(t, agg, cfg, bodyFor(wallet.Hex(), 11155111, testPolicyID, ""))
	if approved {
		t.Error("approved = true without a way to price the credit limit; must fail closed")
	}
}

func TestOwnerOfSmartWallet(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	other := common.HexToAddress("0xc60e71bd0f2e6d8832fea1a2d56091c48493c788")
	storeWallet(t, agg, 11155111, owner, wallet)
	storeWallet(t, agg, 11155111, other, common.HexToAddress("0x71c8f4d7d5291edcb3a081802e7efb2788bd232e"))
	// Same wallet address on another chain, different owner — CREATE2 makes
	// this genuinely possible, and the lookup must not cross chains.
	storeWallet(t, agg, 8453, other, wallet)

	t.Run("resolves owner on the requested chain", func(t *testing.T) {
		got, found, err := ownerOfSmartWallet(agg.db, 11155111, wallet)
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		if got != owner {
			t.Errorf("owner = %s, want %s", got.Hex(), owner.Hex())
		}
	})

	t.Run("same wallet on another chain resolves to that chain's owner", func(t *testing.T) {
		got, found, err := ownerOfSmartWallet(agg.db, 8453, wallet)
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		if got != other {
			t.Errorf("owner = %s, want %s — lookup leaked across chains", got.Hex(), other.Hex())
		}
	})

	t.Run("unknown wallet is not found", func(t *testing.T) {
		_, found, err := ownerOfSmartWallet(agg.db, 11155111,
			common.HexToAddress("0x000000000000000000000000000000000000dEaD"))
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if found {
			t.Error("found = true for a wallet that was never stored")
		}
	})

	t.Run("case-insensitive on the sender address", func(t *testing.T) {
		_, found, err := ownerOfSmartWallet(agg.db, 11155111,
			common.HexToAddress(strings.ToUpper(strings.TrimPrefix(wallet.Hex(), "0x"))))
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if !found {
			t.Error("found = false; sender casing should not matter")
		}
	})
}

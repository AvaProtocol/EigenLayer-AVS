package aggregator

import (
	"encoding/json"
	"fmt"
	"math/big"
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

const testChainID = int64(11155111)

// newWebhookAggregator builds an aggregator with a real chain context.
//
// The chain id matters more than it looks: ownerWithinCreditLimit iterates
// knownFeeChainIDs(), so an aggregator with none would skip the ledger read
// entirely and every test would pass without ever exercising the credit gate.
func newWebhookAggregator(t *testing.T) (*Aggregator, func()) {
	t.Helper()
	db := testutil.TestMustDB()
	agg := &Aggregator{
		logger:  testutil.GetLogger(),
		db:      db,
		chainID: big.NewInt(testChainID),
		config: &config.Config{
			FeeRates: &config.FeeRatesConfig{CreditLimitUSD: 0},
		},
	}
	return agg, func() { db.Close() }
}

// Guards the harness itself. If this regresses to an empty list, every
// approval assertion below becomes vacuous — the ledger would never be read.
func TestWebhookHarnessHasChainContext(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()
	if got := agg.knownFeeChainIDs(); len(got) == 0 {
		t.Fatal("harness has no known chains — credit-limit assertions would not exercise the ledger")
	}
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

// The behaviour the whole feature exists for: an owner carrying outstanding
// value fees above their credit limit must not get sponsored gas. Seeds a real
// FeeLedger record rather than stubbing, so this exercises the same
// CheckCreditLimit path production takes.
func TestGasManagerWebhook_DeniesOwnerOverCreditLimit(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	storeWallet(t, agg, testChainID, owner, wallet)

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID}
	body := bodyFor(wallet.Hex(), testChainID, testPolicyID, "")

	// Clean ledger → approved. Establishes that the denial below is caused by
	// the fee, not by some unrelated rejection earlier in the handler.
	if _, approved := post(t, agg, cfg, body); !approved {
		t.Fatal("precondition: expected approval with an empty ledger")
	}

	ledger := taskengine.NewFeeLedger(agg.db, agg.logger)
	if err := ledger.RecordValueFee(&taskengine.FeeRecord{
		ExecutionID:    "exec-over-limit",
		TaskID:         "task-over-limit",
		Owner:          owner.Hex(),
		Tier:           "EXECUTION_TIER_1",
		TierPercentage: "0.03",
		TxValueWei:     "1000000000000000000",
		FeeAmountWei:   "300000000000000", // outstanding > 0, limit is 0
		Timestamp:      1,
		ChainID:        testChainID,
	}); err != nil {
		t.Fatalf("seeding fee record: %v", err)
	}

	status, approved := post(t, agg, cfg, body)
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if approved {
		t.Error("approved = true for an owner over their credit limit — the credit gate is not being enforced")
	}
}

// Fees accrue per execution chain, so an owner over their limit on one chain
// must not be able to draw sponsorship by requesting on another.
func TestGasManagerWebhook_CreditLimitIsNotBypassableCrossChain(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()
	otherChain := int64(8453)
	agg.config.Chains = []*config.ChainConfig{
		{ChainID: otherChain, SmartWallet: &config.SmartWalletConfig{ChainID: otherChain}},
	}

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	storeWallet(t, agg, testChainID, owner, wallet)

	// Debt sits on the OTHER chain; the request comes in on testChainID.
	ledger := taskengine.NewFeeLedger(agg.db, agg.logger)
	if err := ledger.RecordValueFee(&taskengine.FeeRecord{
		ExecutionID: "exec-other-chain", TaskID: "task-other-chain",
		Owner: owner.Hex(), Tier: "EXECUTION_TIER_1", TierPercentage: "0.03",
		TxValueWei: "1000000000000000000", FeeAmountWei: "300000000000000",
		Timestamp: 1, ChainID: otherChain,
	}); err != nil {
		t.Fatalf("seeding fee record: %v", err)
	}

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID}
	_, approved := post(t, agg, cfg, bodyFor(wallet.Hex(), testChainID, testPolicyID, ""))
	if approved {
		t.Error("approved = true — debt on another chain was not counted, so the limit is bypassable by switching chains")
	}
}

// An aggregator with no chain context cannot vouch for anyone's balance. The
// loop over known chains would otherwise be a no-op and approve unconditionally.
func TestGasManagerWebhook_DeniesWithNoChainContext(t *testing.T) {
	agg, cleanup := newWebhookAggregator(t)
	defer cleanup()
	agg.chainID = nil
	agg.config.Chains = nil

	owner := common.HexToAddress("0x72d841f43241957b558097a5110a8ed68c6fd88c")
	wallet := common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	storeWallet(t, agg, testChainID, owner, wallet)

	cfg := gasManagerWebhookConfig{PolicyID: testPolicyID}
	_, approved := post(t, agg, cfg, bodyFor(wallet.Hex(), testChainID, testPolicyID, ""))
	if approved {
		t.Error("approved = true with no known chains; must fail closed")
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

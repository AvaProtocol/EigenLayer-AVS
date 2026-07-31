package aggregator

import (
	"crypto/subtle"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Alchemy Gas Manager "custom rules" webhook.
//
// Gas Manager POSTs here before sponsoring a UserOp; our answer decides
// whether the treasury pays for it. This is the enforcement point for the
// FeeLedger credit limit — without it FeeLedger is retrospective accounting
// and an owner past their limit keeps getting sponsored.
//
// Two properties this handler must hold, both easy to break by accident:
//
//  1. It is mounted OUTSIDE the /api/v1 group. That group requires a JWT and
//     applies the shared rate limiter. Alchemy sends neither a token nor a
//     predictable request rate, so mounting it there would return 401/429 —
//     which Gas Manager reads as "deny", silently disabling sponsorship for
//     every user. The failure is global and looks like a chain problem.
//
//  2. It fails CLOSED. Every error path returns approved=false. This is the
//     opposite of the executor's pre-execution credit check, which logs and
//     proceeds on a ledger error: there, failing open costs one unmetered
//     execution; here it would hand out unlimited sponsored gas for as long as
//     the fault lasts.
//
// The response is always HTTP 200 carrying an explicit {"approved": bool}. A
// non-200 also denies (the policy has "Sponsor on error or timeout" off), but
// then our deliberate denial is indistinguishable from a crash in the logs.

const gasManagerWebhookPath = "/webhooks/gas-manager"

// gasManagerWebhookRequest is Alchemy's request body. userOperation's shape
// varies by EntryPoint version, so only `sender` is decoded — it is the one
// field present in both v0.6 and v0.7 and the only one this decision needs.
type gasManagerWebhookRequest struct {
	UserOperation struct {
		Sender string `json:"sender"`
	} `json:"userOperation"`
	PolicyID    string `json:"policyId"`
	ChainID     int64  `json:"chainId"`
	WebhookData string `json:"webhookData"`
}

type gasManagerWebhookResponse struct {
	Approved bool `json:"approved"`
}

// gasManagerWebhookConfig carries the deployment-supplied settings. Both come
// from the gateway config (`gas_manager_policy_id`, `gas_manager_webhook_secret`),
// which resolves them from YAML with an environment fallback — the same path
// `moralis_api_key` and `alchemy_api_key` take, so a deployment configures
// them in one place rather than two.
type gasManagerWebhookConfig struct {
	// PolicyID is the Gas Manager policy this gateway answers for. A request
	// quoting any other policy is refused: it means either a misconfigured
	// dashboard or someone else's policy pointed at our endpoint.
	PolicyID string
	// Secret, when set, must appear as the request's webhookData. Defence in
	// depth — the endpoint is unauthenticated by necessity (see above), and
	// this keeps it from being a freely queryable oracle over ledger state.
	// Empty disables the check so the webhook can be brought up before the
	// sponsorship caller is taught to send it.
	Secret string
}

func (agg *Aggregator) gasManagerWebhookConfig() gasManagerWebhookConfig {
	if agg.config == nil {
		return gasManagerWebhookConfig{}
	}
	return gasManagerWebhookConfig{
		PolicyID: strings.TrimSpace(agg.config.GasManagerPolicyID),
		Secret:   strings.TrimSpace(agg.config.GasManagerWebhookSecret),
	}
}

// ownerOfSmartWallet resolves a smart-wallet address back to its owner EOA.
//
// Wallet records are keyed `w:<chain>:<owner>:<wallet>` — owner first — so
// there is no direct reverse index. This scans the chain's key space and
// matches on the trailing segment, using the key-only iterator so values are
// never loaded. Linear in wallets-per-chain, which is fine at current scale
// (hundreds); if that stops being true, add a `wowner:<chain>:<wallet>`
// index rather than making this smarter.
func ownerOfSmartWallet(db storage.Storage, chainID int64, wallet common.Address) (common.Address, bool, error) {
	prefix := []byte(fmt.Sprintf("w:%d:", chainID))
	want := strings.ToLower(wallet.Hex())

	var owner common.Address
	var found bool
	err := db.IterateKeysOnly(prefix, func(key []byte) error {
		// w : chain : owner : wallet
		parts := strings.Split(string(key), ":")
		if len(parts) != 4 {
			return nil
		}
		if parts[3] != want {
			return nil
		}
		owner = common.HexToAddress(parts[2])
		found = true
		return nil
	})
	if err != nil {
		return common.Address{}, false, err
	}
	return owner, found, nil
}

// registerGasManagerWebhook mounts the webhook on the unauthenticated router.
func (agg *Aggregator) registerGasManagerWebhook(e *echo.Echo) {
	cfg := agg.gasManagerWebhookConfig()
	if cfg.PolicyID == "" {
		// Without a policy id every request would be refused, which is worse
		// than not serving the route: an operator who set the URL in the
		// dashboard would see all sponsorship denied with no clue why.
		agg.logger.Warn("gas manager webhook not mounted: gas_manager_policy_id is unset (env ALCHEMY_GAS_POLICY_ID)",
			"path", gasManagerWebhookPath)
		return
	}
	if cfg.Secret == "" {
		agg.logger.Warn("gas manager webhook mounted without gas_manager_webhook_secret (env GAS_MANAGER_WEBHOOK_SECRET); endpoint is unauthenticated beyond the policy id check",
			"path", gasManagerWebhookPath)
	}
	e.POST(gasManagerWebhookPath, func(c echo.Context) error {
		return agg.handleGasManagerWebhook(c, cfg)
	})
	agg.logger.Info("gas manager webhook mounted", "path", gasManagerWebhookPath)
}

// deny logs why sponsorship was refused and returns the 200/false body.
// Reasons are logged rather than returned: the caller is Alchemy, and telling
// an arbitrary POSTer *why* it was refused leaks ledger and config state.
func (agg *Aggregator) deny(c echo.Context, reason string, kv ...interface{}) error {
	agg.logger.Warn("gas sponsorship denied: "+reason, kv...)
	return c.JSON(http.StatusOK, gasManagerWebhookResponse{Approved: false})
}

func (agg *Aggregator) handleGasManagerWebhook(c echo.Context, cfg gasManagerWebhookConfig) error {
	var req gasManagerWebhookRequest
	if err := c.Bind(&req); err != nil {
		return agg.deny(c, "malformed request body", "error", err)
	}

	if subtle.ConstantTimeCompare([]byte(req.PolicyID), []byte(cfg.PolicyID)) != 1 {
		return agg.deny(c, "unexpected policy id", "policy_id", req.PolicyID)
	}
	if cfg.Secret != "" &&
		subtle.ConstantTimeCompare([]byte(req.WebhookData), []byte(cfg.Secret)) != 1 {
		return agg.deny(c, "webhookData did not match the configured secret")
	}

	sender := strings.TrimSpace(req.UserOperation.Sender)
	if !common.IsHexAddress(sender) {
		return agg.deny(c, "userOperation.sender is not an address", "sender", sender)
	}
	wallet := common.HexToAddress(sender)

	if agg.db == nil {
		return agg.deny(c, "storage unavailable")
	}

	owner, found, err := ownerOfSmartWallet(agg.db, req.ChainID, wallet)
	if err != nil {
		return agg.deny(c, "owner lookup failed", "wallet", wallet.Hex(), "error", err)
	}
	if !found {
		// An unknown sender is not ours to sponsor. This is the check that
		// stops the policy from funding wallets created outside this gateway.
		return agg.deny(c, "sender is not a known smart wallet",
			"wallet", wallet.Hex(), "chain_id", req.ChainID)
	}

	withinLimit, outstanding, err := agg.ownerWithinCreditLimit(owner)
	if err != nil {
		return agg.deny(c, "credit check failed", "owner", owner.Hex(), "error", err)
	}
	if !withinLimit {
		return agg.deny(c, "owner is over their credit limit",
			"owner", owner.Hex(), "outstanding_wei", outstanding.String())
	}

	return c.JSON(http.StatusOK, gasManagerWebhookResponse{Approved: true})
}

// ownerWithinCreditLimit checks the owner's outstanding value fees against the
// configured limit on EVERY known chain, not just the one the UserOp targets.
// Fees accrue per execution chain (`fl:<chain>:<owner>`), so gating only the
// requesting chain would let an owner who is over their limit on Base keep
// drawing sponsorship on Ethereum.
func (agg *Aggregator) ownerWithinCreditLimit(owner common.Address) (bool, *big.Int, error) {
	if agg.config == nil || agg.config.FeeRates == nil {
		return false, big.NewInt(0), fmt.Errorf("fee configuration unavailable")
	}
	ledger := taskengine.NewFeeLedger(agg.db, agg.logger)

	// A zero limit means "block on any outstanding balance". USD limits need
	// the price service to convert; when that is unavailable we cannot make a
	// safe judgement, so we fail closed rather than guessing.
	creditLimitWei := big.NewInt(0)
	if limitUSD := agg.config.FeeRates.CreditLimitUSD; limitUSD > 0 {
		if agg.priceService == nil {
			return false, big.NewInt(0), fmt.Errorf("price service unavailable for USD credit limit")
		}
		converted, err := taskengine.ConvertUSDToWei(limitUSD, agg.priceService, agg.chainIDInt64())
		if err != nil {
			return false, big.NewInt(0), fmt.Errorf("converting credit limit to wei: %w", err)
		}
		creditLimitWei = converted
	}

	worstOutstanding := big.NewInt(0)
	for _, chainID := range agg.knownFeeChainIDs() {
		within, outstanding, err := ledger.CheckCreditLimit(chainID, owner, creditLimitWei)
		if err != nil {
			return false, worstOutstanding, fmt.Errorf("chain %d: %w", chainID, err)
		}
		if outstanding != nil && outstanding.Cmp(worstOutstanding) > 0 {
			worstOutstanding = outstanding
		}
		if !within {
			return false, outstanding, nil
		}
	}
	return true, worstOutstanding, nil
}

// chainIDInt64 returns the gateway's default chain, used only to price the USD
// credit limit.
func (agg *Aggregator) chainIDInt64() int64 {
	if agg.chainID != nil {
		return agg.chainID.Int64()
	}
	return 0
}

// knownFeeChainIDs lists every chain this gateway serves, so the credit check
// cannot be bypassed by switching chains.
func (agg *Aggregator) knownFeeChainIDs() []int64 {
	seen := map[int64]struct{}{}
	out := []int64{}
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	add(agg.chainIDInt64())
	if agg.config != nil {
		for _, chain := range agg.config.Chains {
			add(chain.SmartWallet.ChainID)
		}
	}
	return out
}

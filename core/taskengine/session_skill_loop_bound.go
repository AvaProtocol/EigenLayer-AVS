package taskengine

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Compiled Split Incoming Payments, On-Demand Batch Transfer and Batch send
// to wallets loops do not iterate a settings list. {{value}} stays unbound,
// and the generic walk would mark the transfer unresolved. These shapes are
// recognized from the stored protobuf only: the split source is scanned
// against the generator's grammar, and each batch source must equal the
// shipped code1 in studio templates/on-demand-batch-transfer-telegram.json or
// templates/on-demand-batch-send.json. A Studio edit that is not copied here
// stays unresolved. The code is not executed.

const batchFundingSource = `// Multi-token aware funding check. For each transfer row we draw down the
// available balance of that row's token. Rows are funded in order, so two
// rows on the same token share its balance; a MAX row claims whatever
// remains of its token. Each row is tagged ` + "`fundable`" + ` — the Filter node keeps
// only fundable rows and the Loop sends those. balance1 returns balances for
// every token in settings.transfers (tokenIds is {{settings.transfers}}).
const remaining = {};
for (const b of balance1.data) {
  if (b && b.tokenAddress) remaining[b.tokenAddress.toLowerCase()] = BigInt(b.balance || '0');
}

let fundedCount = 0;
const transfers = settings.transfers.map((t) => {
  const addr = (t && t.token_amount && t.token_amount.address ? t.token_amount.address : '').toLowerCase();
  const have = remaining[addr] !== undefined ? remaining[addr] : BigInt(0);
  const amount = t && t.token_amount ? t.token_amount.amount : undefined;
  let fundable = false;
  if (amount === 'MAX') {
    if (have > BigInt(0)) { fundable = true; remaining[addr] = BigInt(0); }
  } else {
    const need = BigInt(amount || '0');
    if (have >= need) { fundable = true; remaining[addr] = have - need; }
  }
  if (fundable) fundedCount++;
  return { ...t, fundable };
});

return { transfers, fundedCount, skippedCount: transfers.length - fundedCount };`

// batchSendFundingSource is code1 of studio templates/on-demand-batch-send.json
// and templates/recurring-batch-send.json, which share it. It funds the rows
// of settings.transfers in order and splits the funded ones in two, because a
// loop compiles one runner: ERC-20 rows to {{code1.data.tokenSends}} and ETH
// rows to {{code1.data.ethSends}}.
const batchSendFundingSource = `// Funds each transfer in order from the smart wallet's balances. Rows on
// the same token share its balance. A funded row goes to the ETH loop or
// the token loop: a loop sends every row with one runner, so ETH and
// ERC-20 rows can't share one. skippedTokens names each token a skipped
// row needed, once, so the run can say which one ran short.
const NATIVE = '0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee';
const remaining = {};
for (const b of balance1.data) {
  if (!b) continue;
  const addr = typeof b.tokenAddress === 'string' ? b.tokenAddress.toLowerCase() : '';
  const key = b.nativeToken === true || addr === '' || addr === NATIVE ? NATIVE : addr;
  remaining[key] = BigInt(b.balance || '0');
}

let fundedCount = 0;
const tokenSends = [];
const ethSends = [];
const skippedTokens = [];
for (const t of settings.transfers) {
  const addr = (t && t.token_amount && t.token_amount.address ? t.token_amount.address : '').toLowerCase();
  const raw = t && t.token_amount ? String(t.token_amount.amount || '') : '';
  const need = /^[0-9]+$/.test(raw) ? BigInt(raw) : BigInt(0);
  const have = remaining[addr] !== undefined ? remaining[addr] : BigInt(0);
  if (addr && need > BigInt(0) && have >= need) {
    remaining[addr] = have - need;
    fundedCount++;
    (addr === NATIVE ? ethSends : tokenSends).push(t);
  } else if (addr && !skippedTokens.includes(addr)) {
    skippedTokens.push(addr);
  }
}

return { tokenSends, ethSends, fundedCount, skippedCount: settings.transfers.length - fundedCount, skippedTokens };`

// The two outputs of batchSendFundingSource a Batch send to wallets loop reads.
const (
	batchSendTokens = "tokenSends"
	batchSendEth    = "ethSends"
)

var (
	nodeOutputRef    = regexp.MustCompile(`^\{\{([A-Za-z_][A-Za-z0-9_]*)\.data\}\}$`)
	nodeTransfersRef = regexp.MustCompile(`^\{\{([A-Za-z_][A-Za-z0-9_]*)\.data\.transfers\}\}$`)
	nodeSendsRef     = regexp.MustCompile(`^\{\{([A-Za-z_][A-Za-z0-9_]*)\.data\.(` + batchSendTokens + `|` + batchSendEth + `)\}\}$`)
	identRE          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	batchDigits      = regexp.MustCompile(`^[0-9]+$`)
	ethTokenSentinel = common.HexToAddress("0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE")
)

type splitExpr struct {
	amount    *big.Int
	unbounded bool
}

type splitRow struct {
	token     string
	amount    *big.Int
	unbounded bool
}

// boundNodeOutputLoop recognizes one compiled template. ok with an empty
// site list means the shape matched and the loop moves no funds. ok false
// leaves the caller on the unresolved visit.
func boundNodeOutputLoop(nodes []*avsproto.TaskNode, settings map[string]any, trigger *avsproto.TaskTrigger, loop *avsproto.LoopNode) ([]fundSite, bool) {
	if loop == nil {
		return nil, false
	}
	if sites, ok := boundSplitLoop(nodes, trigger, loop); ok {
		return sites, true
	}
	if sites, ok := boundBatchSendLoop(nodes, settings, loop); ok {
		return sites, true
	}
	return boundBatchLoop(nodes, settings, loop)
}

func boundSplitLoop(nodes []*avsproto.TaskNode, trigger *avsproto.TaskTrigger, loop *avsproto.LoopNode) ([]fundSite, bool) {
	cfg := loop.GetConfig()
	if cfg == nil {
		return nil, false
	}
	ref := nodeOutputRef.FindStringSubmatch(strings.TrimSpace(cfg.GetInputVariable()))
	if ref == nil {
		return nil, false
	}
	codeNode := nodeByName(nodes, ref[1])
	if codeNode == nil || codeNode.GetCustomCode() == nil || codeNode.GetCustomCode().GetConfig() == nil {
		return nil, false
	}
	code := codeNode.GetCustomCode()
	iter, ok := loopIter(cfg.GetIterVal())
	if !ok {
		return nil, false
	}
	chain, ok := loopTransferRunner(loop,
		"{{"+iter+".tokenAddress}}",
		"{{"+iter+".recipient}}",
		"{{"+iter+".amount}}",
	)
	if !ok {
		return nil, false
	}
	triggerName, rows, ok := parseSplitProgram(code.GetConfig().GetSource())
	if !ok {
		return nil, false
	}
	// The header names the event trigger even when every amount is fixed.
	// An empty or sentinel address list would watch contracts the template
	// does not name, so it does not match.
	if _, ok := eventQueryTokens(trigger, triggerName); !ok {
		return nil, false
	}
	if len(rows) == 0 {
		return nil, true
	}
	spec := rows[0].token
	unbounded := false
	sum := new(big.Int)
	// One percentage or rest row makes the token a ceiling. The fixed
	// rows' sum is not kept as a floor and is not added to the owner's
	// cap: the deposit is unknown, and that cap is the whole allowance.
	for _, row := range rows {
		if row.token != spec {
			return nil, false
		}
		if row.unbounded {
			unbounded = true
			continue
		}
		if row.amount != nil && row.amount.Sign() > 0 {
			sum.Add(sum, row.amount)
		}
	}
	tokens, ok := splitTargetTokens(spec, triggerName, trigger)
	if !ok {
		return nil, false
	}
	sites := make([]fundSite, 0, len(tokens))
	for _, token := range tokens {
		if unbounded {
			sites = append(sites, transferSite(chain, token, nil, true))
			continue
		}
		if sum.Sign() > 0 {
			sites = append(sites, transferSite(chain, token, new(big.Int).Set(sum), false))
		}
	}
	return sites, true
}

func boundBatchLoop(nodes []*avsproto.TaskNode, settings map[string]any, loop *avsproto.LoopNode) ([]fundSite, bool) {
	cfg := loop.GetConfig()
	if cfg == nil {
		return nil, false
	}
	ref := nodeOutputRef.FindStringSubmatch(strings.TrimSpace(cfg.GetInputVariable()))
	if ref == nil {
		return nil, false
	}
	filterNode := nodeByName(nodes, ref[1])
	if filterNode == nil || filterNode.GetFilter() == nil || filterNode.GetFilter().GetConfig() == nil {
		return nil, false
	}
	fcfg := filterNode.GetFilter().GetConfig()
	if strings.TrimSpace(fcfg.GetExpression()) != "value.fundable === true" {
		return nil, false
	}
	codeRef := nodeTransfersRef.FindStringSubmatch(strings.TrimSpace(fcfg.GetInputVariable()))
	if codeRef == nil {
		return nil, false
	}
	codeNode := nodeByName(nodes, codeRef[1])
	if codeNode == nil || codeNode.GetCustomCode() == nil || codeNode.GetCustomCode().GetConfig() == nil {
		return nil, false
	}
	if normalizeJS(codeNode.GetCustomCode().GetConfig().GetSource()) != normalizeJS(batchFundingSource) {
		return nil, false
	}
	iter, ok := loopIter(cfg.GetIterVal())
	if !ok {
		return nil, false
	}
	chain, ok := loopTransferRunner(loop,
		"{{"+iter+".token_amount.address}}",
		"{{"+iter+".recipient}}",
		"{{"+iter+".token_amount.amount}}",
	)
	if !ok {
		return nil, false
	}
	totals, ok := batchTransferTotals(settings)
	if !ok {
		return nil, false
	}
	return tokenTotalSites(chain, totals), true
}

// tokenTotalSites is one transfer site per token, in address order, sized
// to the total of that token's rows.
func tokenTotalSites(chain int64, totals map[common.Address]*big.Int) []fundSite {
	tokens := make([]common.Address, 0, len(totals))
	for token, total := range totals {
		if total != nil && total.Sign() > 0 {
			tokens = append(tokens, token)
		}
	}
	sort.Slice(tokens, func(i, j int) bool {
		return bytes.Compare(tokens[i].Bytes(), tokens[j].Bytes()) < 0
	})
	sites := make([]fundSite, 0, len(tokens))
	for _, token := range tokens {
		sites = append(sites, transferSite(chain, token, new(big.Int).Set(totals[token]), false))
	}
	return sites
}

// boundBatchSendLoop recognizes either loop of the compiled Batch send to
// wallets graph. Each loop is sized from every row of its kind in
// settings.transfers: the most one run can send, since code1 may skip rows
// the wallet can't fund but never adds one. The ERC-20 loop gets one transfer
// site per token. The ETH loop gets one native site per row, so every
// recipient is listed on the grant and each send counts as one UserOp.
func boundBatchSendLoop(nodes []*avsproto.TaskNode, settings map[string]any, loop *avsproto.LoopNode) ([]fundSite, bool) {
	cfg := loop.GetConfig()
	if cfg == nil {
		return nil, false
	}
	ref := nodeSendsRef.FindStringSubmatch(strings.TrimSpace(cfg.GetInputVariable()))
	if ref == nil {
		return nil, false
	}
	codeNode := nodeByName(nodes, ref[1])
	if codeNode == nil || codeNode.GetCustomCode() == nil || codeNode.GetCustomCode().GetConfig() == nil {
		return nil, false
	}
	if normalizeJS(codeNode.GetCustomCode().GetConfig().GetSource()) != normalizeJS(batchSendFundingSource) {
		return nil, false
	}
	iter, ok := loopIter(cfg.GetIterVal())
	if !ok {
		return nil, false
	}
	plan, ok := batchSendRows(settings)
	if !ok {
		return nil, false
	}
	switch ref[2] {
	case batchSendTokens:
		chain, ok := loopTransferRunner(loop,
			"{{"+iter+".token_amount.address}}",
			"{{"+iter+".recipient}}",
			"{{"+iter+".token_amount.amount}}",
		)
		if !ok {
			return nil, false
		}
		return tokenTotalSites(chain, plan.tokenTotals), true
	case batchSendEth:
		chain, ok := loopEthRunner(loop, "{{"+iter+".recipient}}", "{{"+iter+".token_amount.amount}}")
		if !ok {
			return nil, false
		}
		sites := make([]fundSite, 0, len(plan.eth))
		for _, row := range plan.eth {
			sites = append(sites, fundSite{
				chain: chain, kind: fundEth,
				recipient: row.recipient, recipientOK: true,
				amount: new(big.Int).Set(row.amount), amountOK: true,
				loopMult: 1, userOp: 1,
			})
		}
		return sites, true
	default:
		return nil, false
	}
}

// loopEthRunner matches a loop whose runner is a native transfer of exactly
// recipientExpr and amountExpr, and returns its chain.
func loopEthRunner(loop *avsproto.LoopNode, recipientExpr, amountExpr string) (int64, bool) {
	et := loop.GetEthTransfer()
	if et == nil || et.GetConfig() == nil {
		return 0, false
	}
	cfg := et.GetConfig()
	if strings.TrimSpace(cfg.GetDestination()) != recipientExpr || strings.TrimSpace(cfg.GetAmount()) != amountExpr {
		return 0, false
	}
	return cfg.GetChainId(), true
}

type batchSendEthRow struct {
	recipient common.Address
	amount    *big.Int
}

// batchSendPlan is settings.transfers read for sizing: a total per ERC-20
// token, and each ETH row with its recipient.
type batchSendPlan struct {
	tokenTotals map[common.Address]*big.Int
	eth         []batchSendEthRow
}

// batchSendRows reads settings.transfers the way code1 splits it. An ETH row
// is the 0xeeee sentinel and needs a concrete recipient. A row code1 never
// sends (amount zero) adds nothing. Any row that can't be read leaves the
// whole loop unrecognized, as batchTransferTotals does.
func batchSendRows(settings map[string]any) (batchSendPlan, bool) {
	plan := batchSendPlan{tokenTotals: map[common.Address]*big.Int{}}
	if settings == nil {
		return plan, false
	}
	list, ok := settings["transfers"].([]any)
	if !ok {
		return plan, false
	}
	for _, item := range list {
		row, ok := item.(map[string]any)
		if !ok {
			return plan, false
		}
		tokenAmount, ok := row["token_amount"].(map[string]any)
		if !ok {
			return plan, false
		}
		addrText, ok := scalarString(tokenAmount["address"])
		if !ok || !common.IsHexAddress(strings.TrimSpace(addrText)) {
			return plan, false
		}
		amountRaw, ok := tokenAmount["amount"]
		if !ok {
			return plan, false
		}
		amount, ok := batchAmount(amountRaw)
		if !ok {
			return plan, false
		}
		if common.HexToAddress(strings.TrimSpace(addrText)) == ethTokenSentinel {
			recipientText, ok := scalarString(row["recipient"])
			if !ok || !common.IsHexAddress(strings.TrimSpace(recipientText)) {
				return plan, false
			}
			recipient := common.HexToAddress(strings.TrimSpace(recipientText))
			if recipient == (common.Address{}) {
				return plan, false
			}
			if amount.Sign() > 0 {
				plan.eth = append(plan.eth, batchSendEthRow{recipient: recipient, amount: amount})
			}
			continue
		}
		token, ok := concreteToken(addrText)
		if !ok {
			return plan, false
		}
		if amount.Sign() > 0 {
			if plan.tokenTotals[token] == nil {
				plan.tokenTotals[token] = big.NewInt(0)
			}
			plan.tokenTotals[token].Add(plan.tokenTotals[token], amount)
		}
	}
	return plan, true
}

func transferSite(chain int64, token common.Address, amount *big.Int, ceiling bool) fundSite {
	site := fundSite{
		chain: chain, kind: fundCall,
		target: token, targetOK: true,
		selector: selectorTransfer, selectorOK: true,
		ceiling: ceiling, loopMult: 1,
		// The row total is one cap, not one UserOp per run.
		opsUnknown: true,
	}
	if amount != nil && amount.Sign() > 0 && !ceiling {
		site.amount = amount
		site.amountOK = true
	}
	return site
}

func loopIter(raw string) (string, bool) {
	iter := strings.TrimSpace(raw)
	if iter == "" {
		iter = "value"
	}
	if !identRE.MatchString(iter) {
		return "", false
	}
	return iter, true
}

func loopTransferRunner(loop *avsproto.LoopNode, contractExpr, recipientExpr, amountExpr string) (int64, bool) {
	cw := loop.GetContractWrite()
	if cw == nil || cw.GetConfig() == nil {
		return 0, false
	}
	cfg := cw.GetConfig()
	if strings.TrimSpace(cfg.GetValue()) != "" {
		return 0, false
	}
	if strings.TrimSpace(cfg.GetContractAddress()) != contractExpr {
		return 0, false
	}
	calls := cfg.GetMethodCalls()
	if len(calls) != 1 || calls[0] == nil {
		return 0, false
	}
	mc := calls[0]
	if strings.TrimSpace(mc.GetCallData()) != "" {
		return 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(mc.GetMethodName()), "transfer") {
		return 0, false
	}
	params := mc.GetMethodParams()
	if len(params) != 2 || strings.TrimSpace(params[0]) != recipientExpr || strings.TrimSpace(params[1]) != amountExpr {
		return 0, false
	}
	return cfg.GetChainId(), true
}

func nodeByName(nodes []*avsproto.TaskNode, name string) *avsproto.TaskNode {
	if name == "" {
		return nil
	}
	var found *avsproto.TaskNode
	for _, node := range nodes {
		if node == nil || node.GetName() != name {
			continue
		}
		if found != nil {
			return nil
		}
		found = node
	}
	return found
}

func concreteToken(raw string) (common.Address, bool) {
	raw = strings.TrimSpace(raw)
	if !common.IsHexAddress(raw) {
		return common.Address{}, false
	}
	addr := common.HexToAddress(raw)
	if addr == (common.Address{}) || addr == ethTokenSentinel {
		return common.Address{}, false
	}
	return addr, true
}

// eventQueryTokens is the union of a split's event-trigger query addresses.
// One run spends the token that arrived, so each address is a separate cap,
// not a share of one total. An empty list would watch every contract.
func eventQueryTokens(trigger *avsproto.TaskTrigger, triggerName string) ([]common.Address, bool) {
	if trigger == nil || trigger.GetName() != triggerName || trigger.GetEvent() == nil || trigger.GetEvent().GetConfig() == nil {
		return nil, false
	}
	queries := trigger.GetEvent().GetConfig().GetQueries()
	if len(queries) == 0 {
		return nil, false
	}
	seen := map[common.Address]struct{}{}
	var out []common.Address
	for _, query := range queries {
		if query == nil || len(query.GetAddresses()) == 0 {
			return nil, false
		}
		for _, raw := range query.GetAddresses() {
			addr, ok := concreteToken(raw)
			if !ok {
				return nil, false
			}
			if _, ok := seen[addr]; ok {
				continue
			}
			seen[addr] = struct{}{}
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func splitTargetTokens(spec, triggerName string, trigger *avsproto.TaskTrigger) ([]common.Address, bool) {
	watched, ok := eventQueryTokens(trigger, triggerName)
	if !ok {
		return nil, false
	}
	if spec == "{{"+triggerName+".data.contractAddress}}" {
		return watched, true
	}
	one, ok := concreteToken(spec)
	if !ok {
		return nil, false
	}
	return []common.Address{one}, true
}

func batchTransferTotals(settings map[string]any) (map[common.Address]*big.Int, bool) {
	if settings == nil {
		return nil, false
	}
	raw, ok := settings["transfers"]
	if !ok || raw == nil {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	totals := map[common.Address]*big.Int{}
	for _, item := range list {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		tokenAmount, ok := row["token_amount"].(map[string]any)
		if !ok {
			return nil, false
		}
		addrRaw, ok := tokenAmount["address"]
		if !ok {
			return nil, false
		}
		addrText, ok := scalarString(addrRaw)
		if !ok {
			return nil, false
		}
		token, ok := concreteToken(addrText)
		if !ok {
			return nil, false
		}
		amountRaw, ok := tokenAmount["amount"]
		if !ok {
			return nil, false
		}
		amount, ok := batchAmount(amountRaw)
		if !ok {
			return nil, false
		}
		if amount != nil && amount.Sign() > 0 {
			total := totals[token]
			if total == nil {
				total = big.NewInt(0)
				totals[token] = total
			}
			total.Add(total, amount)
		}
	}
	return totals, true
}

// batchAmount accepts a non-negative integer the contract call can encode:
// a digit string, or a JSON number inside the 53-bit mantissa. The exact
// string MAX is what code1 treats as "spend the rest", but transfer's
// uint256 argument cannot encode it, so that row stays unrecognized.
// "max", a blank, whitespace, and any other non-integer do too.
func batchAmount(v any) (amount *big.Int, ok bool) {
	if text, isString := v.(string); isString {
		if !batchDigits.MatchString(text) {
			return nil, false
		}
		n, parsed := new(big.Int).SetString(text, 10)
		if !parsed || n.Sign() < 0 {
			return nil, false
		}
		return n, true
	}
	text, parsed := scalarString(v)
	if !parsed || text == "" || strings.HasPrefix(text, "-") {
		return nil, false
	}
	n, parsedInt := new(big.Int).SetString(text, 10)
	if !parsedInt || n.Sign() < 0 {
		return nil, false
	}
	return n, true
}

func normalizeJS(source string) string {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	return strings.TrimSpace(source)
}

func parseSplitProgram(source string) (triggerName string, rows []splitRow, ok bool) {
	source = normalizeJS(source)
	name, i, ok := parseSplitHeader(source)
	if !ok {
		return "", nil, false
	}
	rows, _, ok = parseSplitRows(source, i)
	if !ok {
		return "", nil, false
	}
	return name, rows, true
}

func parseSplitHeader(s string) (string, int, bool) {
	i := skipSpace(s, 0)
	const prefix = "const input = BigInt("
	if !strings.HasPrefix(s[i:], prefix) {
		return "", 0, false
	}
	i += len(prefix)
	i = skipSpace(s, i)
	if i+1 >= len(s) || s[i] != '{' || s[i+1] != '{' {
		return "", 0, false
	}
	i += 2
	start := i
	if i >= len(s) || !isIdentStart(s[i]) {
		return "", 0, false
	}
	i++
	for i < len(s) && isIdentCont(s[i]) {
		i++
	}
	name := s[start:i]
	const suffix = ".data.value}}"
	if !strings.HasPrefix(s[i:], suffix) {
		return "", 0, false
	}
	i += len(suffix)
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != ')' {
		return "", 0, false
	}
	i++
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != ';' {
		return "", 0, false
	}
	return name, i + 1, true
}

func parseSplitRows(s string, i int) ([]splitRow, int, bool) {
	i = skipSpace(s, i)
	if !hasIdent(s, i, "return") {
		return nil, i, false
	}
	i = skipSpace(s, i+len("return"))
	if i >= len(s) || s[i] != '[' {
		return nil, i, false
	}
	i++
	i = skipSpace(s, i)
	if i < len(s) && s[i] == ']' {
		return finishSplitList(s, i+1, nil)
	}
	var rows []splitRow
	for {
		row, next, ok := parseSplitRow(s, i)
		if !ok {
			return nil, i, false
		}
		rows = append(rows, row)
		i = skipSpace(s, next)
		if i < len(s) && s[i] == ',' {
			i++
			continue
		}
		if i < len(s) && s[i] == ']' {
			return finishSplitList(s, i+1, rows)
		}
		return nil, i, false
	}
}

func finishSplitList(s string, i int, rows []splitRow) ([]splitRow, int, bool) {
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != ';' {
		return nil, i, false
	}
	i = skipSpace(s, i+1)
	if i != len(s) {
		return nil, i, false
	}
	return rows, i, true
}

func parseSplitRow(s string, i int) (splitRow, int, bool) {
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != '{' {
		return splitRow{}, i, false
	}
	i++
	var ok bool
	if i, ok = expectKey(s, i, "name"); !ok {
		return splitRow{}, i, false
	}
	if _, i, ok = parseJSONString(s, i); !ok {
		return splitRow{}, i, false
	}
	if i, ok = expectCommaKey(s, i, "tokenAddress"); !ok {
		return splitRow{}, i, false
	}
	token, i, ok := parseJSONString(s, i)
	if !ok {
		return splitRow{}, i, false
	}
	if i, ok = expectCommaKey(s, i, "recipient"); !ok {
		return splitRow{}, i, false
	}
	if _, i, ok = parseJSONString(s, i); !ok {
		return splitRow{}, i, false
	}
	if i, ok = expectCommaKey(s, i, "amount"); !ok {
		return splitRow{}, i, false
	}
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != '(' {
		return splitRow{}, i, false
	}
	expr, i, ok := parseSplitExpr(s, i+1)
	if !ok {
		return splitRow{}, i, false
	}
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != ')' {
		return splitRow{}, i, false
	}
	i++
	const tail = ".toString()"
	if !strings.HasPrefix(s[i:], tail) {
		return splitRow{}, i, false
	}
	i += len(tail)
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != '}' {
		return splitRow{}, i, false
	}
	return splitRow{token: token, amount: expr.amount, unbounded: expr.unbounded}, i + 1, true
}

func parseSplitExpr(s string, i int) (splitExpr, int, bool) {
	i = skipSpace(s, i)
	if hasIdent(s, i, "input") {
		next := skipSpace(s, i+len("input"))
		if next < len(s) && s[next] == '*' {
			end, ok := parsePercentage(s, i)
			if !ok {
				return splitExpr{}, i, false
			}
			return splitExpr{unbounded: true}, end, true
		}
		if next < len(s) && s[next] == '-' {
			end, ok := parseRest(s, next+1)
			if !ok {
				return splitExpr{}, i, false
			}
			return splitExpr{unbounded: true}, end, true
		}
		return splitExpr{unbounded: true}, next, true
	}
	n, end, ok := parseBigN(s, i)
	if !ok {
		return splitExpr{}, i, false
	}
	return splitExpr{amount: n}, end, true
}

func parseRest(s string, i int) (int, bool) {
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != '(' {
		return i, false
	}
	i, ok := parseSplitTerm(s, i+1)
	if !ok {
		return i, false
	}
	for {
		j := skipSpace(s, i)
		if j < len(s) && s[j] == '+' {
			i, ok = parseSplitTerm(s, j+1)
			if !ok {
				return i, false
			}
			continue
		}
		if j < len(s) && s[j] == ')' {
			return j + 1, true
		}
		return i, false
	}
}

func parseSplitTerm(s string, i int) (int, bool) {
	i = skipSpace(s, i)
	if hasIdent(s, i, "input") {
		next := skipSpace(s, i+len("input"))
		if next < len(s) && s[next] == '*' {
			return parsePercentage(s, i)
		}
		if next < len(s) && (s[next] == '-' || s[next] == '*') {
			return i, false
		}
		return next, true
	}
	_, next, ok := parseBigN(s, i)
	if !ok {
		return i, false
	}
	return next, true
}

// parsePercentage matches input * Nn / 100n and does not keep N. Any share
// of the unknown deposit is unbounded. A divisor other than 100n is not
// the generator's form and fails the match.
func parsePercentage(s string, i int) (int, bool) {
	i = skipSpace(s, i)
	if !hasIdent(s, i, "input") {
		return i, false
	}
	i = skipSpace(s, i+len("input"))
	if i >= len(s) || s[i] != '*' {
		return i, false
	}
	_, i, ok := parseBigN(s, i+1)
	if !ok {
		return i, false
	}
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != '/' {
		return i, false
	}
	hundred, i, ok := parseBigN(s, i+1)
	if !ok || hundred.Cmp(big.NewInt(100)) != 0 {
		return i, false
	}
	return i, true
}

func parseBigN(s string, i int) (*big.Int, int, bool) {
	i = skipSpace(s, i)
	start := i
	if i >= len(s) || s[i] < '0' || s[i] > '9' {
		return nil, i, false
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i >= len(s) || s[i] != 'n' {
		return nil, start, false
	}
	n, ok := new(big.Int).SetString(s[start:i], 10)
	if !ok {
		return nil, start, false
	}
	return n, i + 1, true
}

func parseJSONString(s string, i int) (string, int, bool) {
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != '"' {
		return "", i, false
	}
	j := i + 1
	for j < len(s) {
		switch s[j] {
		case '\\':
			if j+1 >= len(s) {
				return "", i, false
			}
			j += 2
		case '"':
			var out string
			if err := json.Unmarshal([]byte(s[i:j+1]), &out); err != nil {
				return "", i, false
			}
			return out, j + 1, true
		default:
			j++
		}
	}
	return "", i, false
}

func expectCommaKey(s string, i int, key string) (int, bool) {
	i = skipSpace(s, i)
	if i >= len(s) || s[i] != ',' {
		return i, false
	}
	return expectKey(s, i+1, key)
}

func expectKey(s string, i int, key string) (int, bool) {
	i = skipSpace(s, i)
	if !hasIdent(s, i, key) {
		return i, false
	}
	i = skipSpace(s, i+len(key))
	if i >= len(s) || s[i] != ':' {
		return i, false
	}
	return i + 1, true
}

func hasIdent(s string, i int, word string) bool {
	if i < 0 || !strings.HasPrefix(s[i:], word) {
		return false
	}
	end := i + len(word)
	if end < len(s) && isIdentCont(s[end]) {
		return false
	}
	return true
}

func skipSpace(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

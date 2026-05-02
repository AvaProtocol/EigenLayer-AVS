package taskengine

import (
	"fmt"
	"html"
	"math/big"
	"strings"
)

// formatTelegramFromStructured formats Summary into Telegram HTML using structured data
// Uses the PRD format: emoji + subject, network, time, executions list, footer
// All user-controlled content is HTML-escaped to prevent XSS attacks
func formatTelegramFromStructured(s Summary) string {
	var sb strings.Builder

	// Get status emoji - API returns subject WITHOUT emoji, aggregator prepends it
	statusEmoji := getStatusEmoji(s)

	// Subject as header with emoji prefix - only bold the workflow name
	if s.Subject != "" {
		sb.WriteString(statusEmoji)
		if statusEmoji != "" {
			sb.WriteString(" ")
		}
		sb.WriteString(formatSubjectWithBoldName(s.Subject))
		sb.WriteString("\n")
	}

	// Skipped note (success runs with branch-skipped steps) — surfaces why the
	// header shows ⚠️ instead of ✅.
	if s.SkippedNote != "" {
		sb.WriteString("<i>")
		sb.WriteString(html.EscapeString(s.SkippedNote))
		sb.WriteString("</i>\n")
	}

	// Network: use body.network field, fallback to workflow.chain or derive from chainID
	network := s.Network
	if network == "" && s.Workflow != nil {
		network = s.Workflow.Chain
		if network == "" && s.Workflow.ChainID > 0 {
			network = getChainDisplayName(s.Workflow.ChainID)
		}
	}

	// Network / Time / Runner / Cost — metadata block. Cost line follows Runner
	// so the "who and what it cost" pair stays adjacent. For simulations the
	// Cost line is a placeholder ("⛽ (cost estimated at deploy)") — actual gas
	// numbers only appear for deployed runs with real receipts.
	// Runner addresses are intentionally NOT <code>-wrapped (hex addresses don't
	// trigger Telegram auto-linking; the wrap adds visual noise).
	hasRunner := s.Runner != nil && s.Runner.SmartWallet != ""
	costLine := formatTelegramCostLine(s)
	if network != "" || s.TriggeredAt != "" || hasRunner || costLine != "" {
		sb.WriteString("\n")
		if network != "" {
			sb.WriteString("<b>Network:</b> ")
			sb.WriteString(html.EscapeString(network))
			sb.WriteString("\n")
		}
		if s.TriggeredAt != "" {
			sb.WriteString("<b>Time:</b> ")
			sb.WriteString(html.EscapeString(formatTimestampHumanReadable(s.TriggeredAt)))
			sb.WriteString("\n")
		}
		if hasRunner {
			sb.WriteString("<b>Runner:</b> ")
			sb.WriteString(html.EscapeString(truncateAddress(s.Runner.SmartWallet)))
			sb.WriteString("\n")
		}
		if costLine != "" {
			sb.WriteString(costLine)
		}
	}

	// Trigger section
	if s.Trigger != "" {
		sb.WriteString("\n<b>Trigger:</b> ")
		sb.WriteString(html.EscapeString(s.Trigger))
		sb.WriteString("\n")
	}

	// Display executions with "Executed:" header (PRD format)
	if len(s.Executions) > 0 {
		sb.WriteString("\n<b>Executed:</b>\n")
		for _, exec := range s.Executions {
			sb.WriteString("• ")
			sb.WriteString(formatBackticksForChannel(exec.Description, "telegram"))
			sb.WriteString("\n")
			if exec.TxHash != "" {
				if explorerURL := buildTxExplorerURL(s, exec.TxHash); explorerURL != "" {
					sb.WriteString("  Transaction: <a href=\"")
					sb.WriteString(html.EscapeString(explorerURL))
					sb.WriteString("\">")
					sb.WriteString(html.EscapeString(truncateTxHash(exec.TxHash)))
					sb.WriteString("</a>\n")
				}
			}
		}
	}

	// "What Went Wrong" section — consistent with email
	if len(s.Errors) > 0 {
		sb.WriteString("\n<b>What Went Wrong:</b>\n")
		for _, err := range s.Errors {
			sb.WriteString("• ")
			sb.WriteString(formatBackticksForChannel(err, "telegram"))
			sb.WriteString("\n")
		}
	}

	if s.Annotation != "" {
		sb.WriteString("\n<i>")
		sb.WriteString(html.EscapeString(s.Annotation))
		sb.WriteString("</i>")
	}

	return strings.TrimSpace(sb.String())
}

// getStatusEmoji returns the emoji for the summary's status.
// A success run with branch-skipped steps renders ⚠️ (warn) rather than ✅,
// matching the yellow badge on email.
func getStatusEmoji(s Summary) string {
	switch s.Status {
	case "success":
		if s.SkippedSteps > 0 {
			return "⚠️"
		}
		return "✅"
	case "failed", "error":
		return "❌"
	default:
		return ""
	}
}

// formatSubjectWithBoldName formats the subject with <code> tags around the
// prefix + workflow name (everything except the trailing status suffix).
// Subject patterns:
//   - "Simulation: {name} successfully completed"
//   - "Run Node: {name} succeeded"
//   - "Run #N: {name} successfully completed"
//   - "{name} successfully completed"
//   - And similar patterns for "failed to execute" and "partially executed"
func formatSubjectWithBoldName(subject string) string {
	// Suffixes to look for (ordered from most specific to least)
	suffixes := []string{
		" successfully completed",
		" failed to execute",
		" partially executed",
		" succeeded",
	}

	// Prefixes to look for
	prefixes := []string{
		"Simulation: ",
		"Run Node: ",
	}

	// Check for "Run #N: " prefix pattern
	runPrefix := ""
	if strings.HasPrefix(subject, "Run #") {
		// Find the ": " after "Run #N"
		if idx := strings.Index(subject, ": "); idx > 0 {
			runPrefix = subject[:idx+2]
		}
	}

	// Find which suffix matches
	var suffix string
	var nameEnd int
	for _, s := range suffixes {
		if strings.HasSuffix(subject, s) {
			suffix = s
			nameEnd = len(subject) - len(s)
			break
		}
	}

	// Check for deployed workflow format: "{name}: succeeded (...)" or "{name}: failed at ..."
	// Must be checked before the generic " failed at " pattern to avoid splitting at the wrong point.
	if suffix == "" {
		for _, marker := range []string{": succeeded (", ": failed at "} {
			if idx := strings.Index(subject, marker); idx > 0 {
				suffix = subject[idx:]
				nameEnd = idx
				break
			}
		}
	}

	// Check for "failed at <stepName>" suffix (Run Node failure format)
	if suffix == "" && strings.Contains(subject, " failed at ") {
		idx := strings.LastIndex(subject, " failed at ")
		if idx > 0 {
			suffix = subject[idx:]
			nameEnd = idx
		}
	}

	// If no suffix found, just escape and return
	if suffix == "" {
		return html.EscapeString(subject)
	}

	// Find the prefix and extract the name
	var prefix string
	nameStart := 0

	if runPrefix != "" {
		prefix = runPrefix
		nameStart = len(runPrefix)
	} else {
		for _, p := range prefixes {
			if strings.HasPrefix(subject, p) {
				prefix = p
				nameStart = len(p)
				break
			}
		}
	}

	// Extract the workflow name
	name := subject[nameStart:nameEnd]

	// Build the formatted string: <code>prefix + name</code> + suffix
	// Using <code> prevents Telegram from auto-linking names that contain dots
	var sb strings.Builder
	sb.WriteString("<code>")
	if prefix != "" {
		sb.WriteString(html.EscapeString(prefix))
	}
	sb.WriteString(html.EscapeString(name))
	sb.WriteString("</code>")
	sb.WriteString(html.EscapeString(suffix))

	return sb.String()
}

// formatTelegramCostLine renders a single Cost line for the metadata block.
// Telegram is space-constrained, so this is one line — no per-step bullets,
// no value-fee detail. For simulations we don't show specific numbers (sim
// gas prices are conservative chain defaults, not real network conditions);
// the line falls back to the "⛽ (cost estimated at deploy)" placeholder.
// Returns "" when there are no fees to render at all.
func formatTelegramCostLine(s Summary) string {
	if s.Fees == nil {
		return ""
	}

	if s.Workflow != nil && s.Workflow.IsSimulation {
		return "⛽ <i>(cost estimated at deploy)</i>\n"
	}

	totalWei := new(big.Int)
	totalGas := new(big.Int)
	for _, c := range s.Fees.Cogs {
		if c == nil {
			continue
		}
		if c.Fee != nil {
			if w, ok := new(big.Int).SetString(c.Fee.Amount, 10); ok {
				totalWei.Add(totalWei, w)
			}
		}
		if c.CostType == "gas" && c.GasUnits != "" {
			if g, ok := new(big.Int).SetString(c.GasUnits, 10); ok {
				totalGas.Add(totalGas, g)
			}
		}
	}

	var parts []string
	if totalWei.Sign() > 0 {
		parts = append(parts, fmt.Sprintf("%s ETH", formatWeiAsEth(totalWei.String())))
	}
	if totalGas.Sign() > 0 {
		parts = append(parts, fmt.Sprintf("(~%s gas)", formatGasUnits(totalGas.String())))
	}
	if s.Fees.ExecutionFee != nil && s.Fees.ExecutionFee.Amount != "" {
		parts = append(parts, fmt.Sprintf("+ $%s platform fee", trimFractionalZeros(s.Fees.ExecutionFee.Amount)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<b>Cost:</b> " + strings.Join(parts, " ") + "\n"
}

// tierShortLabel maps the proto enum string to a display label. Returns "" for
// EXECUTION_TIER_UNSPECIFIED or unknown values so callers can omit the suffix.
func tierShortLabel(tier string) string {
	switch tier {
	case "EXECUTION_TIER_1":
		return "Tier 1"
	case "EXECUTION_TIER_2":
		return "Tier 2"
	case "EXECUTION_TIER_3":
		return "Tier 3"
	default:
		return ""
	}
}

// formatWeiAsEth converts a wei decimal string to a human-readable ETH string
// with up to 8 fractional digits, trailing zeros trimmed (min 2 retained).
func formatWeiAsEth(weiStr string) string {
	bi, ok := new(big.Int).SetString(weiStr, 10)
	if !ok {
		return weiStr
	}
	if bi.Sign() == 0 {
		return "0"
	}
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	intPart := new(big.Int).Div(bi, denom)
	rem := new(big.Int).Mod(bi, denom)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(8), nil)
	frac := new(big.Int).Mul(rem, scale)
	frac.Div(frac, denom)
	fracStr := frac.String()
	for len(fracStr) < 8 {
		fracStr = "0" + fracStr
	}
	fracStr = strings.TrimRight(fracStr, "0")
	if len(fracStr) < 2 {
		fracStr += strings.Repeat("0", 2-len(fracStr))
	}
	return fmt.Sprintf("%s.%s", intPart.String(), fracStr)
}

// formatGasUnits renders a decimal gas-unit string as "422 K" / "1.2 M" / raw.
func formatGasUnits(gasStr string) string {
	bi, ok := new(big.Int).SetString(gasStr, 10)
	if !ok {
		return gasStr
	}
	n := bi.Int64()
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f M", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%d K", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// trimFractionalZeros strips trailing zeros after a decimal point. "0.020000" → "0.02".
func trimFractionalZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

func formatTelegramExampleMessage(workflowName, chainName string) string {
	var sb strings.Builder

	// Status line with emoji and workflow name (code-wrapped to prevent auto-linking)
	sb.WriteString("✅ <code>")
	sb.WriteString(html.EscapeString(workflowName))
	sb.WriteString("</code> completed\n\n")

	// Network
	sb.WriteString("<b>Network:</b> ")
	sb.WriteString(html.EscapeString(chainName))
	sb.WriteString("\n\n")

	// Executed section with example
	sb.WriteString("<b>Executed:</b>\n")
	sb.WriteString("• (Simulated) ")
	sb.WriteString(ExampleExecutionMessage)
	sb.WriteString("\n\n")

	// Example notice
	sb.WriteString("<i>")
	sb.WriteString(ExampleExecutionAnnotation)
	sb.WriteString("</i>")

	return sb.String()
}

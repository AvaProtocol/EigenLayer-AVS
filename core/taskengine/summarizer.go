package taskengine

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Summary represents composed notification content
type Summary struct {
	Subject string
	Body    string
}

// ComposeSummary generates a conservative, deterministic summary based on the VM's
// currently available execution context. It never panics and always returns a fallback.
//
// Semantics (MVP):
//   - If any prior step failed, subject: "{workflowName}: failed at {stepName}",
//     body: "The workflow failed at step \"{stepName}\". Reason: {first line of error}" (truncated internally)
//   - Otherwise, subject: "{workflowName}: succeeded", body: "Finished {lastStepName}."
//   - workflowName: settings.name || workflowContext.name || "Workflow"
//   - step selection: earliest failure; else last successful step if any; else currentStepName fallback
func ComposeSummary(vm *VM, currentStepName string) Summary {
	workflowName := resolveWorkflowName(vm)
	failed, failedName, failedReason := findEarliestFailure(vm)
	totalSteps := len(vm.ExecutionLogs)
	if totalSteps == 0 && isSingleNodeImmediate(vm) {
		totalSteps = 1
	}
	// In single-node execution or when called mid-step (e.g., from a REST node
	// before its log is appended), there may be zero recorded steps even though
	// one step is in-flight. Show at least 1.
	if totalSteps == 0 {
		totalSteps = 1
	}

	// Build subject up-front to support richer failure bodies
	subject := fmt.Sprintf("%s: succeeded (%d steps)", workflowName, totalSteps)
	if failed {
		subject = fmt.Sprintf("%s: failed at %s (%d steps)", workflowName, failedName, totalSteps)
	}

	lastName := findLastSuccessStepName(vm)
	if lastName == "" {
		lastName = safeName(currentStepName)
	}

	// Extract runner smart wallet and owner EOA (best-effort, no panics)
	vm.mu.Lock()
	smartWallet := ""
	ownerEOA := ""
	if aaSender, ok := vm.vars["aa_sender"].(string); ok && aaSender != "" {
		smartWallet = aaSender
	}
	if wc, ok := vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
		if runner, ok := wc["runner"].(string); ok && runner != "" && smartWallet == "" {
			smartWallet = runner
		}
		if owner, ok := wc["owner"].(string); ok && owner != "" {
			ownerEOA = owner
		}
		if eoa, ok := wc["eoaAddress"].(string); ok && eoa != "" && ownerEOA == "" {
			ownerEOA = eoa
		}
	}
	// Also check settings.runner for single-node executions
	if smartWallet == "" {
		if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
			if runner, ok := settings["runner"].(string); ok && strings.TrimSpace(runner) != "" {
				smartWallet = runner
			}
		}
	}
	// Fallback to TaskOwner if available
	if ownerEOA == "" && vm.TaskOwner != (common.Address{}) {
		ownerEOA = vm.TaskOwner.Hex()
	}
	vm.mu.Unlock()

	// Collect concise on-chain action lines from executed steps
	var actionLines []string
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := strings.ToUpper(st.GetType())
		isWrite := strings.Contains(t, "CONTRACT_WRITE")
		isRead := strings.Contains(t, "CONTRACT_READ")
		if !isWrite && !isRead {
			continue
		}

		contractAddr := ""
		methodName := ""
		if st.GetConfig() != nil {
			if cfg, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
				if addr, ok := cfg["contractAddress"].(string); ok {
					contractAddr = addr
				}
				if mcs, ok := cfg["methodCalls"].([]interface{}); ok && len(mcs) > 0 {
					if call, ok := mcs[0].(map[string]interface{}); ok {
						if mn, ok := call["methodName"].(string); ok {
							methodName = mn
						}
					}
				}
			}
		}

		// Include key output data when available (amountOut for swaps, approve value)
		outDetail := ""
		if isWrite && st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
			if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
				// Try common event shapes first (Approval, Swap)
				if ev, ok := m["Approval"].(map[string]interface{}); ok {
					if val, ok := ev["value"].(string); ok && val != "" {
						outDetail = fmt.Sprintf("value=%s", val)
					}
				}
				if ev, ok := m["Swap"].(map[string]interface{}); ok && outDetail == "" {
					// Prefer amountOut from event amounts if present
					if amt, ok := ev["amount1"].(string); ok && amt != "" { // token1 out in USDC/WETH pool often amount1
						outDetail = fmt.Sprintf("amountOut=%s", amt)
					} else if amt0, ok := ev["amount0"].(string); ok && amt0 != "" {
						outDetail = fmt.Sprintf("amountOut=%s", amt0)
					}
				}
			}
			// Fallback to metadata.value.amountOut for writes returning a value (e.g., exactInputSingle)
			if outDetail == "" && st.Metadata != nil {
				if meta := st.Metadata.AsInterface(); meta != nil {
					if arr, ok := meta.([]interface{}); ok && len(arr) > 0 {
						if first, ok := arr[0].(map[string]interface{}); ok {
							if val, ok := first["value"].(map[string]interface{}); ok {
								if amt, ok := val["amountOut"].(string); ok && amt != "" {
									outDetail = fmt.Sprintf("amountOut=%s", amt)
								}
							} else if amtStr, ok := first["value"].(string); ok && amtStr != "" && strings.ToLower(methodName) == "exactinputsingle" {
								outDetail = fmt.Sprintf("amountOut=%s", amtStr)
							}
						}
					}
				}
			}
		}
		if isRead && st.GetContractRead() != nil && st.GetContractRead().Data != nil {
			if m, ok := st.GetContractRead().Data.AsInterface().(map[string]interface{}); ok {
				if v, ok := m[methodName].(map[string]interface{}); ok {
					if amt, ok := v["amountOut"].(string); ok && amt != "" {
						outDetail = fmt.Sprintf("amountOut=%s", amt)
					}
				}
			}
		}

		line := fmt.Sprintf("- %s: %s on %s", st.GetName(), methodName, contractAddr)
		if outDetail != "" {
			line += fmt.Sprintf(" (%s)", outDetail)
		}
		actionLines = append(actionLines, line)
	}

	// Single-node execution enhancement: if there are no recorded steps, try to
	// synthesize concise action lines from inputVariables (carried into the
	// runNodeImmediately call) and settings. This helps users understand what
	// happened even when the workflow isn't deployed end-to-end yet.
	if totalSteps == 1 && len(actionLines) == 0 && isSingleNodeImmediate(vm) {
		vm.mu.Lock()
		var settings map[string]interface{}
		if m, ok := vm.vars["settings"].(map[string]interface{}); ok {
			settings = m
		}
		// Read inputs directly from top-level vars (RunNodeWithInputs adds them as top-level)
		approveVar, hasApprove := vm.vars["approve_token1"].(map[string]interface{})
		quoteVar, hasQuote := vm.vars["get_quote"].(map[string]interface{})
		swapVar, hasSwap := vm.vars["run_swap"].(map[string]interface{})
		vm.mu.Unlock()

		if hasApprove || hasQuote || hasSwap {
			// Helpful addresses from settings
			var token1Addr, quoterV2Addr, swapRouter02Addr string
			if pool, ok := settings["uniswapv3_pool"].(map[string]interface{}); ok {
				if t1, ok := pool["token1"].(map[string]interface{}); ok {
					if id, ok := t1["id"].(string); ok {
						token1Addr = id
					}
				}
			}
			if contracts, ok := settings["uniswapv3_contracts"].(map[string]interface{}); ok {
				if a, ok := contracts["quoterV2"].(string); ok {
					quoterV2Addr = a
				}
				if a, ok := contracts["swapRouter02"].(string); ok {
					swapRouter02Addr = a
				}
			}

			// approve_token1
			if hasApprove {
				if data, ok := approveVar["data"].(map[string]interface{}); ok {
					if approve, ok := data["approve"].(map[string]interface{}); ok {
						val := ""
						if s, ok := approve["value"].(string); ok && s != "" {
							val = s
						}
						line := fmt.Sprintf("- approve_token1: approve on %s", token1Addr)
						if val != "" {
							line += fmt.Sprintf(" (value=%s)", val)
						}
						actionLines = append(actionLines, line)
					}
				}
			}

			// get_quote
			if hasQuote {
				if data, ok := quoteVar["data"].(map[string]interface{}); ok {
					if q, ok := data["quoteExactInputSingle"].(map[string]interface{}); ok {
						amt := ""
						if s, ok := q["amountOut"].(string); ok && s != "" {
							amt = s
						}
						line := fmt.Sprintf("- get_quote: quoteExactInputSingle on %s", quoterV2Addr)
						if amt != "" {
							line += fmt.Sprintf(" (amountOut=%s)", amt)
						}
						actionLines = append(actionLines, line)
					}
				}
			}

			// run_swap
			if hasSwap {
				if data, ok := swapVar["data"].(map[string]interface{}); ok {
					if swap, ok := data["exactInputSingle"].(map[string]interface{}); ok {
						amt := ""
						if s, ok := swap["amountOut"].(string); ok && s != "" {
							amt = s
						}
						line := fmt.Sprintf("- run_swap: exactInputSingle on %s", swapRouter02Addr)
						if amt != "" {
							line += fmt.Sprintf(" (amountOut=%s)", amt)
						}
						actionLines = append(actionLines, line)
					}
				}
			}
		}
	}

	var body string
	if failed {
		// Failure body: include runner/owner and any completed on-chain actions before failure
		body = fmt.Sprintf(
			"The workflow failed at step \"%s\". Reason: %s\nRunner smart wallet: %s (owner EOA: %s)\n",
			failedName, firstLine(failedReason), smartWallet, ownerEOA,
		)
		if len(actionLines) > 0 {
			body += "On-chain actions completed before failure:\n" + strings.Join(actionLines, "\n")
		} else {
			body += "No on-chain actions were completed before the failure."
		}
	} else {
		// Success body using concise narrative per PRD
		chainName := resolveChainName(vm)
		narrative := buildNarrativeFromLogs(vm)
		if strings.TrimSpace(narrative) != "" {
			body = fmt.Sprintf(
				"Smart wallet %s (owner %s) executed %d on-chain actions.\n\n%s\n\nAll steps completed on %s. Last step: %s.",
				smartWallet, ownerEOA, countActionsInLogs(vm), narrative, chainName, lastName,
			)
		} else {
			// Fallback to previous list format
			body = fmt.Sprintf(
				"Workflow '%s' completed successfully with %d steps.\nRunner smart wallet: %s (owner EOA: %s)\n",
				workflowName, totalSteps, smartWallet, ownerEOA,
			)
			if len(actionLines) > 0 {
				body += "On-chain actions:\n" + strings.Join(actionLines, "\n")
				if totalSteps == 0 {
					body += "\nSingle-node execution using prior outputs. A full step-by-step summary will be sent after this workflow is deployed and run end-to-end."
				}
			} else {
				if totalSteps == 0 {
					body += "Single-node execution. A full step-by-step summary will be sent after this workflow is deployed and run end-to-end.\n"
				}
				body += fmt.Sprintf("Finished %s.", lastName)
			}
		}
	}

	return Summary{
		Subject: subject,
		Body:    body,
	}
}

// isSingleNodeImmediate returns true if the VM looks like a temp VM used for a
// one-off RunNodeImmediately execution (no TaskId, exactly one TaskNode).
func isSingleNodeImmediate(vm *VM) bool {
	if vm == nil {
		return false
	}
	// No lock needed for GetTaskId; lock to count TaskNodes safely
	vm.mu.Lock()
	count := len(vm.TaskNodes)
	vm.mu.Unlock()
	return vm.GetTaskId() == "" && count == 1
}

// ------- Helpers for concise narrative formatting -------

func resolveChainName(vm *VM) string {
	// Prefer settings.chain then chain_id mapping, else try smartWalletConfig.ChainID
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if n, ok := settings["chain"].(string); ok && strings.TrimSpace(n) != "" {
			return n
		}
		if id, ok := settings["chain_id"].(int); ok && id != 0 {
			if name := mapChainIDToName(int64(id)); name != "" {
				return name
			}
		}
	}
	if vm.smartWalletConfig != nil && vm.smartWalletConfig.ChainID != 0 {
		if name := mapChainIDToName(vm.smartWalletConfig.ChainID); name != "" {
			return name
		}
	}
	return "unknown network"
}

func mapChainIDToName(id int64) string {
	switch id {
	case 1:
		return "Ethereum"
	case 5, 11155111:
		return "Sepolia"
	case 8453:
		return "Base"
	case 84531:
		return "Base Goerli"
	default:
		return "chain-" + fmt.Sprint(id)
	}
}

func countActionsInLogs(vm *VM) int {
	count := 0
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := strings.ToUpper(st.GetType())
		if strings.Contains(t, "CONTRACT_WRITE") || strings.Contains(t, "CONTRACT_READ") {
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return count
}

func buildNarrativeFromLogs(vm *VM) string {
	// Extract token context from settings (pool tokens and known contracts)
	vm.mu.Lock()
	var settings map[string]interface{}
	if m, ok := vm.vars["settings"].(map[string]interface{}); ok {
		settings = m
	}
	vm.mu.Unlock()

	token0Sym, token0Dec := "", 18
	token1Sym, token1Dec := "", 18
	var routerAddr string
	if settings != nil {
		if pool, ok := settings["uniswapv3_pool"].(map[string]interface{}); ok {
			if t0, ok := pool["token0"].(map[string]interface{}); ok {
				if s, ok := t0["symbol"].(string); ok {
					token0Sym = s
					token0Dec = defaultDecimalsForSymbol(s)
				}
			}
			if t1, ok := pool["token1"].(map[string]interface{}); ok {
				if s, ok := t1["symbol"].(string); ok {
					token1Sym = s
					token1Dec = defaultDecimalsForSymbol(s)
				}
			}
		}
		if contracts, ok := settings["uniswapv3_contracts"].(map[string]interface{}); ok {
			if a, ok := contracts["swapRouter02"].(string); ok {
				routerAddr = a
			}
		}
	}

	// Build sentences
	var sentences []string
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := strings.ToUpper(st.GetType())
		if !(strings.Contains(t, "CONTRACT_WRITE") || strings.Contains(t, "CONTRACT_READ")) {
			continue
		}

		methodName := ""
		if st.GetConfig() != nil {
			if cfg, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
				if mcs, ok := cfg["methodCalls"].([]interface{}); ok && len(mcs) > 0 {
					if call, ok := mcs[0].(map[string]interface{}); ok {
						if mn, ok := call["methodName"].(string); ok {
							methodName = strings.ToLower(mn)
						}
					}
				}
			}
		}

		switch methodName {
		case "approve":
			value := ""
			spender := ""
			if st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
				if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
					if ev, ok := m["Approval"].(map[string]interface{}); ok {
						if v, ok := ev["value"].(string); ok {
							value = v
						}
						if sp, ok := ev["spender"].(string); ok {
							spender = sp
						}
					}
				}
			}
			if spender == "" {
				spender = routerAddr
			}
			if value != "" && token1Sym != "" {
				sentences = append(sentences, fmt.Sprintf("Approved %s %s to Uniswap V3 router %s for trading.", formatAmount(value, token1Dec), token1Sym, spender))
			}

		case "quoteexactinputsingle":
			amountOut := ""
			if st.GetContractRead() != nil && st.GetContractRead().Data != nil {
				if m, ok := st.GetContractRead().Data.AsInterface().(map[string]interface{}); ok {
					if q, ok := m["quoteExactInputSingle"].(map[string]interface{}); ok {
						if a, ok := q["amountOut"].(string); ok {
							amountOut = a
						}
					}
				}
			}
			if amountOut != "" && token0Sym != "" {
				sentences = append(sentences, fmt.Sprintf("Quoted ~%s %s via Uniswap V3.", formatAmount(amountOut, token0Dec), token0Sym))
			}

		case "exactinputsingle":
			// Prefer metadata.value.amountOut on writes
			amountOut := ""
			if st.Metadata != nil {
				if meta := st.Metadata.AsInterface(); meta != nil {
					if arr, ok := meta.([]interface{}); ok && len(arr) > 0 {
						if first, ok := arr[0].(map[string]interface{}); ok {
							if val, ok := first["value"].(map[string]interface{}); ok {
								if amt, ok := val["amountOut"].(string); ok {
									amountOut = amt
								}
							} else if amtStr, ok := first["value"].(string); ok {
								amountOut = amtStr
							}
						}
					}
				}
			}
			if amountOut != "" && token1Sym != "" && token0Sym != "" {
				// We cannot reliably know tokenIn amount without decoding params; use narrative focused on output
				sentences = append(sentences, fmt.Sprintf("Swapped for ~%s %s via Uniswap V3 (exactInputSingle).", formatAmount(amountOut, token0Dec), token0Sym))
			}
		}
	}
	// Join sentences with blank lines for readability
	return strings.Join(sentences, "\n\n")
}

func defaultDecimalsForSymbol(sym string) int {
	switch strings.ToUpper(sym) {
	case "USDC", "USDT":
		return 6
	default:
		return 18
	}
}

func formatAmount(raw string, decimals int) string {
	if raw == "" {
		return "0.0000"
	}
	bi, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return raw
	}
	if decimals <= 0 {
		return bi.String()
	}
	// Convert to decimal with 4 fractional digits
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	// value = bi / denom
	intPart := new(big.Int).Div(bi, denom)
	rem := new(big.Int).Mod(bi, denom)
	// Scale remainder to four decimals
	scaleDigits := int64(4)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(scaleDigits), nil)
	frac := new(big.Int).Mul(rem, scale)
	frac.Div(frac, denom)
	fracStr := frac.String()
	// Left-pad with zeros to fixed width
	for len(fracStr) < int(scaleDigits) {
		fracStr = "0" + fracStr
	}
	return fmt.Sprintf("%s.%s", intPart.String(), fracStr)
}

func resolveWorkflowName(vm *VM) string {
	// Try settings.name
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if n, okn := settings["name"].(string); okn && strings.TrimSpace(n) != "" {
			return n
		}
	}
	if wc, ok := vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
		if n, okn := wc["name"].(string); okn && strings.TrimSpace(n) != "" {
			return n
		}
	}
	return "Workflow"
}

func findEarliestFailure(vm *VM) (bool, string, string) {
	steps := vm.ExecutionLogs
	for _, s := range steps {
		if !s.GetSuccess() {
			name := s.GetName()
			if name == "" {
				name = s.GetId()
			}
			return true, safeName(name), s.GetError()
		}
	}
	return false, "", ""
}

func findLastSuccessStepName(vm *VM) string {
	steps := vm.ExecutionLogs
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].GetSuccess() {
			name := steps[i].GetName()
			if name == "" {
				name = steps[i].GetId()
			}
			return safeName(name)
		}
	}
	return ""
}

func firstLine(s string) string {
	if s == "" {
		return "unknown error"
	}
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

func safeName(s string) string {
	if strings.TrimSpace(s) == "" {
		return "step"
	}
	return s
}

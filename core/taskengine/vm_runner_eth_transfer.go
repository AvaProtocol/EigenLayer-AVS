package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// The session sends, replaced in tests.
var (
	sendUserOpAuto    = preset.SendUserOpAuto
	sendNativeReserve = preset.SendNativeReserveAuto
)

type ETHTransferProcessor struct {
	*CommonProcessor
	ethClient         *ethclient.Client
	smartWalletConfig *config.SmartWalletConfig
	taskOwner         *common.Address
}

func NewETHTransferProcessor(vm *VM, ethClient *ethclient.Client, smartWalletConfig *config.SmartWalletConfig, taskOwner *common.Address) *ETHTransferProcessor {
	return &ETHTransferProcessor{
		CommonProcessor:   &CommonProcessor{vm: vm},
		ethClient:         ethClient,
		smartWalletConfig: smartWalletConfig,
		taskOwner:         taskOwner,
	}
}

// getAASenderString returns the aa_sender variable from the VM as a string, or "" if not set/invalid.
// Caller must not hold vm.mu.
func getAASenderString(vm *VM) string {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if v, ok := vm.vars["aa_sender"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// getAASenderAddress returns the aa_sender variable as *common.Address if it is a valid hex address, or nil.
// Caller must not hold vm.mu.
func getAASenderAddress(vm *VM) *common.Address {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if v, ok := vm.vars["aa_sender"]; ok {
		if s, ok := v.(string); ok && common.IsHexAddress(s) {
			addr := common.HexToAddress(s)
			return &addr
		}
	}
	return nil
}

func (p *ETHTransferProcessor) Execute(stepID string, node *avsproto.ETHTransferNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	executionLog := CreateNodeExecutionStep(stepID, p.GetTaskNode(), p.vm)

	// Initially assume failure until we succeed
	executionLog.Success = false

	var logBuilder strings.Builder
	logBuilder.WriteString(formatNodeExecutionLogHeader(executionLog))

	var err error
	var finalized bool // Track if step was already finalized
	defer func() {
		if !finalized {
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			}
			finalizeStep(executionLog, err == nil, err, "", logBuilder.String())
		}
	}()

	// Get configuration
	config := node.GetConfig()
	if err = validateNodeConfig(config, "ETHTransferNode"); err != nil {
		return executionLog, err
	}

	// Validate Handlebars template size before preprocessing
	if err = validateHandlebarsIfTemplate(config.GetDestination()); err != nil {
		return executionLog, err
	}
	if err = validateHandlebarsIfTemplate(config.GetAmount()); err != nil {
		return executionLog, err
	}

	// Preprocess template variables in configuration
	destination := p.vm.preprocessTextWithVariableMapping(config.GetDestination())
	amountStr := p.vm.preprocessTextWithVariableMapping(config.GetAmount())

	// Log resolved config values for debugging
	logBuilder.WriteString(fmt.Sprintf("Resolved: destination=%s, amount=%s\n", destination, amountStr))

	// Owner collection resolves the authored amount but never reads a live MAX
	// balance or enters the session/simulation/submission path.
	if p.vm.ownerPreparation != nil {
		err = p.collectOwnerETH(stepID, config, destination, amountStr)
		return executionLog, err
	}
	if config.GetReserveGas() {
		err = fmt.Errorf("reserveGas is supported only by owner preparation")
		return executionLog, err
	}

	// Resolve "MAX" (case-insensitive) to smart wallet's full ETH balance
	isMaxTransfer := false
	if strings.EqualFold(amountStr, "MAX") {
		senderAddr := getAASenderAddress(p.vm)
		if senderAddr == nil {
			err = fmt.Errorf("cannot resolve MAX amount: smart wallet address not available")
			return executionLog, err
		}
		if p.ethClient == nil {
			err = fmt.Errorf("cannot resolve MAX amount: no RPC client available")
			return executionLog, err
		}
		// Read the balance only after the run's earlier operations from
		// this wallet are included: a pending one has not paid its prefund
		// or moved its value yet.
		if !p.vm.IsSimulation {
			chain := config.GetChainId()
			if chain <= 0 && p.smartWalletConfig != nil {
				chain = p.smartWalletConfig.ChainID
			}
			if pending := p.vm.unresolvedEarlierWrite(chain); pending != nil {
				err = NewStructuredError(avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, fmt.Sprintf(
					"cannot resolve MAX amount: earlier write %q (%s) may still be pending, so the wallet's balance is not final", pending.Name, pending.Id))
				return executionLog, err
			}
		}
		balance, balErr := p.ethClient.BalanceAt(context.Background(), *senderAddr, nil)
		if balErr != nil {
			err = fmt.Errorf("cannot resolve MAX amount: failed to get balance: %v", balErr)
			return executionLog, err
		}
		if balance.Cmp(big.NewInt(0)) == 0 {
			err = fmt.Errorf("cannot resolve MAX amount: wallet balance is zero")
			return executionLog, err
		}
		amountStr = balance.String()
		isMaxTransfer = true
		logBuilder.WriteString(fmt.Sprintf("MAX resolved to %s wei (wallet: %s)\n", amountStr, senderAddr.Hex()))
	}

	// Validate template variable resolution
	if err = ValidateTemplateVariableResolution(destination, config.GetDestination(), p.vm, "destination"); err != nil {
		return executionLog, err
	}
	if err = ValidateTemplateVariableResolution(amountStr, config.GetAmount(), p.vm, "amount"); err != nil {
		return executionLog, err
	}

	if destination == "" {
		err = fmt.Errorf("destination address is required for ETH transfer")
		return executionLog, err
	}

	if amountStr == "" {
		err = fmt.Errorf("amount is required for ETH transfer")
		return executionLog, err
	}

	// Validate destination address
	if !common.IsHexAddress(destination) {
		err = fmt.Errorf("invalid destination address: %s", destination)
		return executionLog, err
	}

	// Validate amount (assuming it's in wei)
	amountWei, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		err = fmt.Errorf("invalid amount: %s", amountStr)
		return executionLog, err
	}

	if p.vm != nil && p.vm.IsSimulation && p.vm.db != nil {
		checked := amountWei
		if isMaxTransfer && !sponsoredFromConfig(p.smartWalletConfig) {
			// Check what Run checks: the reserve sizes the value. The grant
			// report still observes the whole balance for Review's sizing.
			checked = big.NewInt(0)
		}
		if refusal := p.preflightNativeSend(common.HexToAddress(destination), amountWei, checked); refusal != nil {
			err = refusal
			return executionLog, err
		}
	}

	// Real transactions only when not in simulation context
	if p.smartWalletConfig != nil && !p.vm.IsSimulation {
		// A sponsored MAX sends the whole balance: the chain's Gas Manager
		// policy pays the gas. A self-funded MAX sends what the balance and
		// the grant's remaining native limit allow after the operation's own
		// maximum fee, sized by the send while it prices the operation.
		p.vm.logger.Info("🚀 ETH TRANSFER DEBUG - Using real UserOp transaction path",
			"destination", destination,
			"amount", amountStr)

		return p.executeRealETHTransfer(stepID, destination, amountStr, isMaxTransfer, executionLog, &finalized)
	}

	// Simulation path for ETH transfers (SimulateTask / RunNodeImmediately)
	p.vm.logger.Info("🔮 ETH TRANSFER DEBUG - Using simulation path",
		"destination", destination,
		"amount", amountStr,
		"reason", func() string {
			if p.vm.IsSimulation {
				return "vm_is_simulation"
			}
			if p.smartWalletConfig == nil {
				return "smart_wallet_config_is_nil"
			}
			return "unknown"
		}())

	// Simulate transaction hash
	txHash := fmt.Sprintf("0x%064d", time.Now().UnixNano())

	// Get the sender (smart wallet) address for the transfer object
	fromAddress := getAASenderString(p.vm)

	// Local stand-in for a receipt: the hash is made up and nothing is
	// simulated on Tenderly. Gas units match the installed-grant preflight
	// ceiling so a displayed fee is not a 21k EOA transfer.
	var simChainID uint64
	if p.smartWalletConfig != nil {
		simChainID = uint64(p.smartWalletConfig.ChainID)
	}
	gasUsedBig := new(big.Int).SetUint64(nativePreflightGasUnitsSteady)
	gasPriceBig := new(big.Int).SetUint64(GetDefaultGasPrice(simChainID))
	var estimate *simulatedNativeReserve
	if isMaxTransfer && !sponsoredFromConfig(p.smartWalletConfig) {
		// Test shows about what Run sends. Run prices the actual operation,
		// which can differ.
		if estimate, err = p.simulateNativeReserve(amountWei, gasPriceBig); err != nil {
			return executionLog, err
		}
		gasUsedBig, gasPriceBig = estimate.units, estimate.fee
	}
	totalGasCost := new(big.Int).Mul(gasUsedBig, gasPriceBig)
	executionLog.GasUsed = gasUsedBig.String()
	executionLog.GasPrice = gasPriceBig.String()
	executionLog.TotalGasCost = totalGasCost.String()

	// Build result object for metadata (only transactionHash - success/isSimulated are in response/executionContext)
	resultObj := map[string]interface{}{
		"transactionHash": txHash,
		"gasUsed":         executionLog.GasUsed,
		"gasPrice":        executionLog.GasPrice,
		"totalGasCost":    executionLog.TotalGasCost,
	}
	sent := amountWei
	if estimate != nil {
		amountStr = estimate.sized.Value.String()
		sent = estimate.sized.Value
		addNativeReserveMetadata(resultObj, estimate.sized)
	}
	p.observeRunNative(sent)

	// Create output data: only transfer object in data field (matches ERC20 format)
	ethData := map[string]interface{}{
		"transfer": map[string]interface{}{
			"from":  fromAddress,
			"to":    destination,
			"value": amountStr,
		},
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(ethData)
	if err != nil {
		// Fallback to empty data on error
		if p.vm != nil && p.vm.logger != nil {
			p.vm.logger.Warn("Failed to create ETH transfer output value (simulation), using empty fallback", "error", err, "stepID", stepID)
		}
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	outputData := &avsproto.ETHTransferNode_Output{
		Data: dataValue,
	}

	// Set execution log output
	executionLog.OutputData = &avsproto.Execution_Step_EthTransfer{
		EthTransfer: outputData,
	}

	// Set step-level metadata (matches contract_write pattern)
	if metadataValue, err := structpb.NewValue(resultObj); err == nil {
		executionLog.Metadata = metadataValue
	} else if p.vm != nil && p.vm.logger != nil {
		p.vm.logger.Warn("Failed to create metadata value for ETH transfer", "error", err, "stepID", stepID)
	}

	// Use shared function to set output variable for this step
	// Structure matches contract_write: data contains transfer event, result contains tx details
	setNodeOutputData(p.CommonProcessor, stepID, map[string]interface{}{
		"transaction_hash": txHash,
		"from":             fromAddress,
		"to":               destination,
		"value":            amountStr,
		"success":          true,
		"data": map[string]interface{}{
			"transfer": map[string]interface{}{
				"from":  fromAddress,
				"to":    destination,
				"value": amountStr,
			},
		},
		"result": resultObj,
	})

	// Create log message
	logMessage := fmt.Sprintf("Simulated ETH transfer of %s wei to %s (tx: %s)", amountStr, destination, txHash)
	if estimate != nil {
		logMessage += fmt.Sprintf("; MAX keeps an estimated %s wei for the network fee", estimate.sized.GasReserve)
	}

	// Use shared function to finalize execution step
	finalized = true // Mark as finalized to prevent defer from overwriting
	finalizeStep(executionLog, true, nil, "", logMessage)

	return executionLog, nil
}

// currentMaxFee is the max fee a signed operation would carry now, or the
// reason the quote is unavailable.
func (p *ETHTransferProcessor) currentMaxFee() (*big.Int, error) {
	if p.ethClient == nil {
		return nil, fmt.Errorf("no chain reader for native MAX fee quote")
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativePreflightRPCTimeout)
	defer cancel()
	fee, err := preset.UserOpMaxFeeV07(ctx, p.ethClient, p.smartWalletConfig, p.vm.logger)
	if err != nil {
		return nil, err
	}
	if fee == nil || fee.Sign() <= 0 {
		return nil, fmt.Errorf("native MAX fee quote is not positive")
	}
	return fee, nil
}

// simulatedNativeReserve is a simulation's estimate of a self-funded MAX.
type simulatedNativeReserve struct {
	units, fee *big.Int
	sized      *preset.NativeReserveResult
}

// simulateNativeReserve estimates what Run's reserve send transfers, with the
// same rule (preset.NativeReserveValue): the gas the preflight prices for this
// grant at the current max fee (fallbackFee when it cannot be read), the
// wallet's EntryPoint deposit, and the grant's remaining native limit.
//
// In a grant report, a permission that does not cover the step's own fee is
// noted as a native cap miss, so Review says the cap is too low, and the
// estimate shows what the balance allows. A strict simulation refuses, as Run
// does.
func (p *ETHTransferProcessor) simulateNativeReserve(balance, fallbackFee *big.Int) (*simulatedNativeReserve, error) {
	policy, _, err := p.sessionPolicy()
	if err != nil {
		return nil, err
	}
	units := int64(nativePreflightGasUnitsSteady)
	if policy != nil {
		units = nativePreflightGasUnitsFor(NativeSend, policy)
	}
	fee, quoteErr := p.currentMaxFee()
	if quoteErr != nil {
		fee = fallbackFee
		if p.vm.logger != nil {
			p.vm.logger.Debug("native MAX fee quote failed", "error", quoteErr)
			p.vm.logger.Warn("native MAX fee quote unavailable; using the simulation fallback", "max_fee_per_gas", fee.String())
		}
	}
	maxGasCost := new(big.Int).Mul(big.NewInt(units), fee)
	ctx, cancel := context.WithTimeout(context.Background(), nativePreflightRPCTimeout)
	defer cancel()
	sender := getAASenderAddress(p.vm)
	// A failed deposit read takes 0, as Run does: it reserves more, never less.
	deposit := big.NewInt(0)
	if p.ethClient != nil && sender != nil {
		if read, readErr := preset.EntryPointDepositV07(ctx, p.ethClient, *sender); readErr == nil {
			deposit = read
		}
	}
	remaining, err := p.remainingNativeLimit(ctx, policy, sender)
	if err != nil {
		return nil, err
	}
	sized, err := preset.NativeReserveValue(balance, maxGasCost, deposit, remaining)
	var refusal *preset.NativeReserveError
	if errors.As(err, &refusal) && refusal.Code == preset.NativeCapBelowFeeCode && p.vm.sessionGrantReport != nil {
		p.vm.sessionGrantReport.noteGrantMiss(formatNativeCapBelowFee(maxGasCost, remaining, policy.ID), nil)
		sized, err = preset.NativeReserveValue(balance, maxGasCost, deposit, nil)
	}
	if err != nil {
		return nil, nativeReserveRefusal(err)
	}
	return &simulatedNativeReserve{units: big.NewInt(units), fee: fee, sized: sized}, nil
}

// remainingNativeLimit is C for a simulation: nil when the grant meters no
// native value, the granted limit while its install is still pending, and
// otherwise what is left on chain. A failed read refuses, as Run does.
func (p *ETHTransferProcessor) remainingNativeLimit(ctx context.Context, policy *model.SessionPolicy, sender *common.Address) (*big.Int, error) {
	if policy == nil || policy.NativeSpendCap == nil {
		return nil, nil
	}
	if policy.Grant == nil || !policy.Grant.Applied() {
		return nativeGrantedCap(policy)
	}
	if p.ethClient == nil || sender == nil {
		return nil, fmt.Errorf("reading the remaining native limit: no chain reader")
	}
	left, err := aa.ReadNativeSpendLimit(ctx, p.ethClient, policy.EntityID, *sender)
	if err != nil {
		return nil, fmt.Errorf("reading the remaining native limit: %w", err)
	}
	return left, nil
}

// formatNativeCapBelowFee is the report's miss for a permission that does not
// cover a self-funded MAX's own fee. It carries the native cap code, so Review
// reports the cap as too low.
func formatNativeCapBelowFee(fee, remaining *big.Int, policyID string) string {
	return fmt.Sprintf("%s: the permission's remaining ETH limit (%s wei) does not cover this send's network fee (%s wei) — re-grant with a higher cap (policy %s)",
		SessionPolicyNativeCapExceededCode, remaining, fee, policyID)
}

// The step metadata keys a reserved transfer adds: decimal wei strings, and
// limitedBy only when the permission set the value. A loop iteration carries
// them as well (ethIterationMetadata).
const (
	nativeReserveGasReserveKey   = "gasReserve"
	nativeReserveLimitedByKey    = "limitedBy"
	nativeReserveLeftInWalletKey = "leftInWallet"
)

// addNativeReserveMetadata reports what a reserved transfer kept for the fee
// and, when anything stays in the wallet, how much and why.
func addNativeReserveMetadata(metadata map[string]interface{}, sized *preset.NativeReserveResult) {
	metadata[nativeReserveGasReserveKey] = sized.GasReserve.String()
	if sized.LimitedBy == preset.NativeReserveLimitedByPermission {
		metadata[nativeReserveLimitedByKey] = sized.LimitedBy
	}
	if sized.Left.Sign() > 0 {
		metadata[nativeReserveLeftInWalletKey] = sized.Left.String()
	}
}

// sessionPolicy returns the active grant for the runner wallet, and whether
// there was a wallet to look one up for.
func (p *ETHTransferProcessor) sessionPolicy() (*model.SessionPolicy, bool, error) {
	if p == nil || p.smartWalletConfig == nil || p.vm == nil || p.vm.db == nil || p.taskOwner == nil {
		return nil, false, nil
	}
	sender := getAASenderAddress(p.vm)
	if sender == nil {
		return nil, false, nil
	}
	chainID := p.smartWalletConfig.ChainID
	if chainID <= 0 {
		chainID = p.vm.vmDefaultChainID()
	}
	policy, err := ActiveSessionPolicyForWallet(p.vm.db, chainID, *p.taskOwner, *sender)
	if err != nil {
		return nil, true, fmt.Errorf("SESSION_POLICY_LOOKUP_FAILED: %v", err)
	}
	return policy, true, nil
}

// grantedNativeLimit is the native limit a grant installs, or nil when the
// grant meters no native value.
func grantedNativeLimit(policy *model.SessionPolicy) (*big.Int, error) {
	if policy == nil || policy.NativeSpendCap == nil {
		return nil, nil
	}
	return nativeGrantedCap(policy)
}

// nativeReserveRefusal keeps a reserve refusal's code in the message and adds
// the amounts as details. A short balance is INSUFFICIENT_BALANCE; a short
// permission keeps its message code, like the other session-policy refusals.
func nativeReserveRefusal(err error) error {
	var refusal *preset.NativeReserveError
	if !errors.As(err, &refusal) {
		return err
	}
	details := map[string]interface{}{"code": refusal.Code}
	for key, value := range map[string]*big.Int{
		"balance": refusal.Balance, "gasReserve": refusal.GasReserve,
		"remaining": refusal.Remaining, "maxGasCost": refusal.MaxGasCost,
	} {
		if value != nil {
			details[key] = value.String()
		}
	}
	code := avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED
	if refusal.Code == preset.NativeBalanceBelowFeeCode {
		code = avsproto.ErrorCode_INSUFFICIENT_BALANCE
	}
	return NewStructuredError(code, refusal.Error(), details)
}

// preflightSessionGrant refuses a native ETH send the active grant cannot
// cover, before the bundler returns opaque AA23.
func (p *ETHTransferProcessor) preflightSessionGrant(destination common.Address, amount *big.Int) string {
	if err := p.preflightSessionGrantError(destination, amount); err != nil {
		return err.Error()
	}
	return ""
}

func (p *ETHTransferProcessor) preflightSessionGrantError(destination common.Address, amount *big.Int) error {
	return p.preflightNativeSend(destination, amount, amount)
}

// observeRunNative records the ETH this simulated transfer sends, for one
// run's amount. A self-funded MAX sends its balance less the fee reserve;
// preflightNativeSend has already reported the whole balance for the
// grant. It reports under the same conditions.
func (p *ETHTransferProcessor) observeRunNative(sent *big.Int) {
	if p == nil || p.smartWalletConfig == nil || p.vm == nil || p.vm.db == nil || p.taskOwner == nil || p.vm.sessionGrantReport == nil || getAASenderAddress(p.vm) == nil {
		return
	}
	chainID := p.smartWalletConfig.ChainID
	if chainID <= 0 {
		chainID = p.vm.vmDefaultChainID()
	}
	p.vm.sessionGrantReport.observeRunNative(chainID, p.vm.workflowNodeOf(p.GetTaskNode()), sent)
}

// preflightNativeSend checks checked against the grant. A grant report
// records observed, which can differ: a self-funded MAX checks 0 wei, since
// the send sizes its value, while Review sizes the cap from the whole balance.
func (p *ETHTransferProcessor) preflightNativeSend(destination common.Address, observed, amount *big.Int) error {
	if p == nil || p.smartWalletConfig == nil {
		return nil
	}
	if p.vm != nil && p.vm.db != nil && p.taskOwner != nil && p.vm.sessionGrantReport != nil && getAASenderAddress(p.vm) != nil {
		p.vm.sessionGrantReport.observeNative(destination, observed)
	}
	policy, lookedUp, err := p.sessionPolicy()
	if err != nil {
		return err
	}
	if !lookedUp {
		return nil
	}
	report := p.vm.sessionGrantReport
	if report != nil {
		report.notePolicy(policy)
	}
	if policy == nil {
		// No usable grant: the send path fails "no session authorization".
		if report != nil {
			report.noteNoGrant()
		}
		return nil
	}
	checked := policy
	if isWalletBudget(policy) {
		clone := *policy
		clone.NativeSpendCap = nil
		checked = &clone
	}
	msg := PreflightNativePermission(checked, NativeIntent{
		Recipient: destination,
		Amount:    amount,
		Kind:      NativeSend,
		Sponsored: sponsoredFromConfig(p.smartWalletConfig),
	}, codeAndFeeFromEthClient(p.ethClient))
	if report != nil && msg != "" {
		report.noteGrantMiss(msg, nil)
		return nil
	}
	if msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if isWalletBudget(policy) && report == nil {
		var caller aa.ContractCaller
		if p.ethClient != nil {
			caller = p.ethClient
		}
		return CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: policy, Calls: []PlannedCall{{Target: destination, Value: amount}}, Caller: caller, FeeReader: codeAndFeeFromEthClient(p.ethClient), Native: NativeIntent{Kind: NativeSend, Sponsored: sponsoredFromConfig(p.smartWalletConfig)}})
	}
	return nil
}

// executeRealETHTransfer executes a real UserOp transaction for ETH transfers
func (p *ETHTransferProcessor) executeRealETHTransfer(stepID, destination, amountStr string, isMaxTransfer bool, executionLog *avsproto.Execution_Step, finalized *bool) (*avsproto.Execution_Step, error) {
	p.vm.logger.Info("🔍 REAL ETH TRANSFER DEBUG - Starting real UserOp ETH transfer execution",
		"destination", destination,
		"amount", amountStr)

	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		err := fmt.Errorf("failed to parse amount: %s", amountStr)
		finalizeStep(executionLog, false, nil, err.Error(), "")
		return executionLog, err
	}

	// A self-funded MAX sends what the balance and the remaining native limit
	// allow after the operation's own maximum fee; the send sizes it and
	// checks value + gas against the remaining limit before signing. The
	// balance read for MAX is only its upper bound, so the preflight checks
	// the recipient and that the permission covers the step's own gas, not
	// the whole balance: a permission short of it sends part, by design.
	reserved := isMaxTransfer && !sponsoredFromConfig(p.smartWalletConfig)
	preflightAmount := amount
	if reserved {
		preflightAmount = big.NewInt(0)
	}
	if refusal := p.preflightSessionGrantError(common.HexToAddress(destination), preflightAmount); refusal != nil {
		p.vm.logger.Warn("session grant cannot authorize a native ETH transfer",
			"destination", destination,
			"amount", amountStr,
			"chain_id", p.smartWalletConfig.ChainID)
		err := refusal
		*finalized = true
		finalizeStep(executionLog, false, err, refusal.Error(), "")
		return executionLog, err
	}

	// Parse destination address
	destinationAddr := common.HexToAddress(destination)

	// execute(target, value, data). data stays empty. A native recipient is
	// not an allowedActions target, and AllowlistModule cannot scope the
	// native row to empty calldata, so this packing is what keeps the send
	// a plain transfer — including a send to an acknowledged 7702 recipient.
	smartWalletCallData, err := aa.PackExecuteMAv2(
		destinationAddr, // target address
		amount,          // ETH value to send
		[]byte{},        // empty data for pure ETH transfer
	)
	if err != nil {
		p.vm.logger.Error("Failed to pack smart wallet execute calldata for ETH transfer", "error", err)
		finalizeStep(executionLog, false, nil, fmt.Sprintf("Failed to pack execute calldata: %v", err), "")
		return executionLog, err
	}

	// Sponsorship on MA v2 is the chain's Alchemy Gas Manager policy, applied
	// inside the send path. A MAX transfer used to need the v0.6 paymaster's
	// SkipReimbursement so the whole balance could move; with reimbursement
	// gone there is nothing to skip.
	if p.smartWalletConfig.SponsorshipPolicyID() != "" {
		p.vm.logger.Info("Gas Manager will sponsor the ETH transfer",
			"owner", p.taskOwner.Hex())
	} else {
		p.vm.logger.Info("Self-funded ETH transfer (no alchemy_paymaster_policy_id)",
			"owner", p.taskOwner.Hex())
	}

	// AA overrides from VM
	senderOverride := getAASenderAddress(p.vm)
	var saltOverride *big.Int
	p.vm.mu.Lock()
	if v, ok := p.vm.vars["aa_salt"]; ok {
		if s, ok2 := v.(*big.Int); ok2 {
			saltOverride = s
		}
	}
	p.vm.mu.Unlock()

	// Send UserOp transaction with overrides
	var userOp *preset.SentUserOp
	var receipt *types.Receipt
	var sized *preset.NativeReserveResult
	var reserveRefusal *preset.NativeReserveError
	if reserved {
		policy, _, lookupErr := p.sessionPolicy()
		var limit *big.Int
		if lookupErr == nil {
			limit, lookupErr = grantedNativeLimit(policy)
		}
		if lookupErr != nil {
			err = fmt.Errorf("cannot size MAX against the grant's native limit: %w", lookupErr)
			*finalized = true
			finalizeStep(executionLog, false, err, err.Error(), "")
			return executionLog, err
		}
		userOp, receipt, sized, err = sendNativeReserve(
			p.smartWalletConfig,
			*p.taskOwner,
			&preset.NativeReserve{Recipient: destinationAddr, Balance: amount, GrantedLimit: limit},
			senderOverride,
			saltOverride,
			p.vm.logger,
		)
		errors.As(err, &reserveRefusal)
		err = nativeReserveRefusal(err)
		if sized != nil {
			amountStr = sized.Value.String()
		}
	} else {
		userOp, receipt, err = sendUserOpAuto(
			p.smartWalletConfig,
			*p.taskOwner,
			smartWalletCallData,
			senderOverride,
			saltOverride,
			p.vm.logger,
		)
	}

	if err != nil {
		if reserveRefusal != nil {
			// The wallet's balance or permission, not the bundler: no alert.
			p.vm.logger.Warn("self-funded MAX cannot pay its own network fee",
				"code", reserveRefusal.Code, "destination", destination, "balance", amountStr)
		} else {
			// See preset.LogBundlerError: Warn on on-chain revert, Error on infra/AA.
			preset.LogBundlerError(p.vm.logger, err,
				"bundler: ETH transfer UserOp transaction failed",
				"bundler_error", err,
				"bundler_provider", p.smartWalletConfig.ProviderName(),
				"bundler_url", p.smartWalletConfig.BundlerEndpointLabel(),
				"destination", destination,
				"amount", amountStr)
		}

		// Return error result - deployed workflows must fail if bundler is unavailable
		*finalized = true // Mark as finalized to prevent defer from overwriting
		finalizeStep(executionLog, false, err, fmt.Sprintf("Bundler failed - ETH transfer UserOp transaction could not be sent: %v", err), "")
		return executionLog, err
	}

	// Success! Extract transaction hash
	var txHash string
	if receipt != nil && receipt.TxHash != (common.Hash{}) {
		txHash = receipt.TxHash.Hex()
	} else if userOp != nil {
		// Fallback: use a deterministic hash based on UserOp
		txHash = userOp.UserOpHash.Hex()
	} else {
		txHash = fmt.Sprintf("0x%064d", time.Now().UnixNano())
	}

	p.vm.logger.Info("✅ REAL ETH TRANSFER SUCCESS - UserOp transaction completed",
		"tx_hash", txHash,
		"destination", destination,
		"amount", amountStr)

	// Get the sender (smart wallet) address for the transfer object
	fromAddress := getAASenderString(p.vm)

	// Build result object for metadata (transactionHash + receipt status + gas info)
	resultObj := map[string]interface{}{
		"transactionHash": txHash,
	}

	// What the reserve kept for the fee, so Studio can say "minus the network
	// fee", and, when the permission set the value, the ETH it left behind.
	if sized != nil {
		addNativeReserveMetadata(resultObj, sized)
	}

	// If SendUserOp returned without a receipt (timed out), mark as pending
	// and include the userOpHash so waitForOnChainConfirmationIfNeeded can poll for it.
	if receipt == nil && userOp != nil {
		resultObj["receiptStatus"] = "pending"
		resultObj["userOpHash"] = userOp.UserOpHash.Hex()
	}

	// Extract gas information from receipt if available
	if receipt != nil {
		// Convert types.Receipt to gas cost information
		gasUsed := receipt.GasUsed
		gasPrice := receipt.EffectiveGasPrice

		if gasUsed > 0 && gasPrice != nil && gasPrice.Cmp(big.NewInt(0)) > 0 {
			// Calculate total gas cost: gasUsed * gasPrice (use SetUint64 to avoid int64 overflow)
			gasUsedBig := new(big.Int).SetUint64(gasUsed)
			totalGasCost := new(big.Int).Mul(gasUsedBig, gasPrice)

			// Set gas cost fields in execution step
			executionLog.GasUsed = gasUsedBig.String()
			executionLog.GasPrice = gasPrice.String()
			executionLog.TotalGasCost = totalGasCost.String()

			// Add gas info to result object for metadata
			resultObj["gasUsed"] = executionLog.GasUsed
			resultObj["gasPrice"] = executionLog.GasPrice
			resultObj["totalGasCost"] = executionLog.TotalGasCost

			p.vm.logger.Info("✅ Set gas cost information for ETH transfer",
				"step_id", stepID,
				"gas_used", executionLog.GasUsed,
				"gas_price", executionLog.GasPrice,
				"total_gas_cost", executionLog.TotalGasCost)
		} else {
			p.vm.logger.Debug("⚠️ No gas cost information available from ETH transfer receipt",
				"step_id", stepID,
				"gas_used", gasUsed,
				"gas_price", gasPrice)
		}
	}

	// Create output data: only transfer object in data field (matches ERC20 format)
	ethData := map[string]interface{}{
		"transfer": map[string]interface{}{
			"from":  fromAddress,
			"to":    destination,
			"value": amountStr,
		},
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(ethData)
	if err != nil {
		// Fallback to empty data on error
		if p.vm != nil && p.vm.logger != nil {
			p.vm.logger.Warn("Failed to create ETH transfer output value (real), using empty fallback", "error", err, "stepID", stepID)
		}
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	outputData := &avsproto.ETHTransferNode_Output{
		Data: dataValue,
	}

	// Set execution log output
	executionLog.OutputData = &avsproto.Execution_Step_EthTransfer{
		EthTransfer: outputData,
	}

	// Set step-level metadata (matches contract_write pattern)
	if metadataValue, err := structpb.NewValue(resultObj); err == nil {
		executionLog.Metadata = metadataValue
	} else if p.vm != nil && p.vm.logger != nil {
		p.vm.logger.Warn("Failed to create metadata value for ETH transfer", "error", err, "stepID", stepID)
	}

	// Use shared function to set output variable for this step
	// Structure matches contract_write: data contains transfer event, result contains tx details
	setNodeOutputData(p.CommonProcessor, stepID, map[string]interface{}{
		"transaction_hash": txHash,
		"from":             fromAddress,
		"to":               destination,
		"value":            amountStr,
		"success":          true,
		"data": map[string]interface{}{
			"transfer": map[string]interface{}{
				"from":  fromAddress,
				"to":    destination,
				"value": amountStr,
			},
		},
		"result": resultObj,
	})

	// Create log message
	logMessage := fmt.Sprintf("Real ETH transfer of %s wei to %s (tx: %s)", amountStr, destination, txHash)
	if sized != nil {
		logMessage += fmt.Sprintf("; MAX kept %s wei for the network fee", sized.GasReserve)
		if sized.Left.Sign() > 0 {
			logMessage += fmt.Sprintf(" and left %s wei in the wallet", sized.Left)
			if sized.LimitedBy == preset.NativeReserveLimitedByPermission {
				logMessage += ", the most the permission allowed"
			}
		}
	}

	// Use shared function to finalize execution step
	*finalized = true // Mark as finalized to prevent defer from overwriting
	finalizeStep(executionLog, true, nil, "", logMessage)

	return executionLog, nil
}

func (p *ETHTransferProcessor) collectOwnerETH(stepID string, config *avsproto.ETHTransferNode_Config, destination, amount string) error {
	c := p.vm.ownerPreparation
	if c.nativeID != stepID || c.nativeCollected || (c.contractID != "" && !c.contractCollected) {
		return fmt.Errorf("unexpected owner native transfer")
	}
	if !strings.EqualFold(getAASenderString(p.vm), c.source.Hex()) {
		return fmt.Errorf("owner workflow source changed during preparation")
	}
	if !common.IsHexAddress(destination) {
		return fmt.Errorf("invalid owner native destination")
	}
	target := common.HexToAddress(destination)
	if target == (common.Address{}) || target == c.source {
		return fmt.Errorf("choose a different nonzero native destination")
	}
	value, ok := new(big.Int).SetString(amount, 10)
	if !ok || strings.HasPrefix(amount, "+") || value.Sign() <= 0 || value.BitLen() > 256 {
		return fmt.Errorf("owner ETHTransfer needs a positive captured amount in wei; MAX is not supported")
	}
	if len(c.calls) >= ownerBatchMaxCalls {
		return fmt.Errorf("owner batch exceeds call limit")
	}
	c.calls = append(c.calls, aa.Call{Target: target, Value: value, Data: []byte{}})
	c.reserveGas, c.nativeCollected = config.GetReserveGas(), true
	return nil
}

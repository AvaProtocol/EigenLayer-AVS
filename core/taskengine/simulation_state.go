package taskengine

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// SimulationStateMap tracks accumulated state overrides during workflow simulation.
// It captures how on-chain state *would* change if each simulated step actually executed,
// and feeds those changes forward as Tenderly state_objects overrides so subsequent
// simulation steps see a consistent view of the world.
type SimulationStateMap struct {
	mu sync.Mutex

	// storage maps lowercase contract address → storage slot hash → hex value.
	// These are passed as state_objects[addr].storage in Tenderly API calls.
	storage map[string]map[string]string

	// ethBalances maps lowercase address → hex balance string.
	// These are passed as state_objects[addr].balance in Tenderly API calls.
	ethBalances map[string]string

	// balanceSlotCache maps lowercase token contract address → the uint slot index
	// used by that token's _balances mapping. Discovered once via probing, then reused.
	balanceSlotCache map[string]*int64

	logger logger.Logger
}

// NewSimulationStateMap creates a new empty state map for a simulation run.
func NewSimulationStateMap(log logger.Logger) *SimulationStateMap {
	return &SimulationStateMap{
		storage:          make(map[string]map[string]string),
		ethBalances:      make(map[string]string),
		balanceSlotCache: make(map[string]*int64),
		logger:           log,
	}
}

// SetStorageSlot records a storage override for a contract address.
func (s *SimulationStateMap) SetStorageSlot(contractAddress string, slot string, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	addr := strings.ToLower(contractAddress)
	if s.storage[addr] == nil {
		s.storage[addr] = make(map[string]string)
	}
	s.storage[addr][slot] = value
}

// SetETHBalance records an ETH balance override for an address.
func (s *SimulationStateMap) SetETHBalance(address string, balanceHex string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ethBalances[strings.ToLower(address)] = balanceHex
}

// MergeRawStateDiff merges Tenderly's raw_state_diff entries into the accumulated state.
// Each entry has: address, key (storage slot), original, dirty (new value).
func (s *SimulationStateMap) MergeRawStateDiff(rawStateDiff []interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, entry := range rawStateDiff {
		entryMap, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}

		addr, _ := entryMap["address"].(string)
		key, _ := entryMap["key"].(string)
		dirty, _ := entryMap["dirty"].(string)

		if addr == "" || key == "" || dirty == "" {
			continue
		}

		addr = strings.ToLower(addr)
		if s.storage[addr] == nil {
			s.storage[addr] = make(map[string]string)
		}
		s.storage[addr][key] = dirty
	}
}

// BuildStateObjects constructs the state_objects map for a Tenderly API request,
// merging accumulated overrides with the existing sender ETH balance override.
func (s *SimulationStateMap) BuildStateObjects(senderAddress string, senderETHOverride string) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	stateObjects := make(map[string]interface{})

	// Apply accumulated storage overrides
	for addr, slots := range s.storage {
		addrObj := s.getOrCreateAddrObj(stateObjects, addr)
		storageMap := make(map[string]string)
		for slot, val := range slots {
			storageMap[slot] = val
		}
		addrObj["storage"] = storageMap
	}

	// Apply accumulated ETH balance overrides
	for addr, balance := range s.ethBalances {
		addrObj := s.getOrCreateAddrObj(stateObjects, addr)
		addrObj["balance"] = balance
	}

	// Always ensure the sender has enough ETH for gas
	senderKey := strings.ToLower(senderAddress)
	senderObj := s.getOrCreateAddrObj(stateObjects, senderKey)
	if _, hasBalance := senderObj["balance"]; !hasBalance {
		senderObj["balance"] = senderETHOverride
	}

	return stateObjects
}

// getOrCreateAddrObj returns the override object for an address, creating it if needed.
// Must be called with s.mu held.
func (s *SimulationStateMap) getOrCreateAddrObj(stateObjects map[string]interface{}, addr string) map[string]interface{} {
	existing, ok := stateObjects[addr]
	if ok {
		if m, ok := existing.(map[string]interface{}); ok {
			return m
		}
	}
	m := make(map[string]interface{})
	stateObjects[addr] = m
	return m
}

// erc20BalanceSlot computes the keccak256 storage slot for _balances[holder]
// given the mapping's base slot index in the contract's storage layout.
func erc20BalanceSlot(holder common.Address, mappingSlot int64) common.Hash {
	// abi.encode(address, uint256) — each padded to 32 bytes
	key := common.LeftPadBytes(holder.Bytes(), 32)
	slot := common.LeftPadBytes(big.NewInt(mappingSlot).Bytes(), 32)
	return crypto.Keccak256Hash(append(key, slot...))
}

// erc20AllowanceSlot computes the keccak256 storage slot for allowance[owner][spender]
// given the mapping's base slot index in the contract's storage layout.
// Formula: keccak256(abi.encode(spender, keccak256(abi.encode(owner, allowanceSlot))))
func erc20AllowanceSlot(owner, spender common.Address, mappingSlot int64) common.Hash {
	// Inner hash: keccak256(abi.encode(owner, allowanceSlot))
	ownerPadded := common.LeftPadBytes(owner.Bytes(), 32)
	slotPadded := common.LeftPadBytes(big.NewInt(mappingSlot).Bytes(), 32)
	innerHash := crypto.Keccak256Hash(append(ownerPadded, slotPadded...))

	// Outer hash: keccak256(abi.encode(spender, innerHash))
	spenderPadded := common.LeftPadBytes(spender.Bytes(), 32)
	return crypto.Keccak256Hash(append(spenderPadded, innerHash.Bytes()...))
}

// Common ERC20 balance mapping slot indices across different implementations.
// 0: standard OpenZeppelin ERC20, 1-3: various token implementations,
// 9: USDC (FiatTokenV2 proxy), 51: Compound cToken-style contracts.
var commonBalanceSlots = []int64{0, 1, 2, 3, 9, 51}

// Common ERC20 allowance mapping slot indices across different implementations.
// 1: OpenZeppelin ERC20 v4/v5, 2-3: legacy implementations, 10: USDC FiatTokenV2.
var commonAllowanceSlots = []int64{1, 2, 3, 10}

// ProbeERC20BalanceSlot discovers which storage slot a token contract uses for
// its _balances mapping by comparing eth_getStorageAt results against balanceOf.
// Returns the slot index, or an error if no match is found.
func (s *SimulationStateMap) ProbeERC20BalanceSlot(
	ctx context.Context,
	reader ChainStateReader,
	tokenContract common.Address,
	holder common.Address,
) (int64, error) {
	tokenKey := strings.ToLower(tokenContract.Hex())

	// Check cache first
	s.mu.Lock()
	cached, hasCached := s.balanceSlotCache[tokenKey]
	s.mu.Unlock()
	if hasCached {
		if cached == nil {
			return -1, fmt.Errorf("previously failed to discover balance slot for %s", tokenKey)
		}
		return *cached, nil
	}

	// Call balanceOf(holder) to get the expected value
	balanceOfSig := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	callData := append(balanceOfSig, common.LeftPadBytes(holder.Bytes(), 32)...)

	result, err := reader.CallContract(ctx, ethereum.CallMsg{
		To:   &tokenContract,
		Data: callData,
	}, nil)
	if err != nil {
		s.cacheSlotResult(tokenKey, nil)
		return -1, fmt.Errorf("balanceOf call failed: %w", err)
	}

	expectedBalance := new(big.Int).SetBytes(result)

	// Only probe with the target holder if they have a non-zero balance.
	// When balance is 0, every empty storage slot reads as 0 and would
	// false-match, so we skip straight to the reference-holder approach.
	if expectedBalance.Sign() > 0 {
		for _, candidateSlot := range commonBalanceSlots {
			slotHash := erc20BalanceSlot(holder, candidateSlot)
			storageValue, err := reader.GetStorageAt(ctx, tokenContract, slotHash)
			if err != nil {
				continue
			}

			storedBalance := new(big.Int).SetBytes(storageValue)
			if storedBalance.Cmp(expectedBalance) == 0 {
				if s.logger != nil {
					s.logger.Info("Discovered ERC20 balance storage slot",
						"token", tokenContract.Hex(),
						"slot", candidateSlot,
						"balance", expectedBalance.String())
				}
				discoveredSlot := candidateSlot
				s.cacheSlotResult(tokenKey, &discoveredSlot)
				return discoveredSlot, nil
			}
		}

		// Non-zero balance but no slot matched — fall through to reference-holder probe
	}

	// Holder balance is 0: every empty storage slot reads as 0 and would
	// false-match. Try to find a reference holder with non-zero balance by
	// checking well-known addresses (e.g. address(1) from test mints).
	if expectedBalance.Sign() == 0 {
		if s.logger != nil {
			s.logger.Info("Holder has zero balance, probing with reference addresses",
				"token", tokenContract.Hex(),
				"holder", holder.Hex())
		}

		referenceAddresses := []common.Address{
			common.HexToAddress("0x0000000000000000000000000000000000000001"),
			common.HexToAddress("0x0000000000000000000000000000000000000002"),
		}

		for _, refAddr := range referenceAddresses {
			refCallData := append(crypto.Keccak256([]byte("balanceOf(address)"))[:4], common.LeftPadBytes(refAddr.Bytes(), 32)...)
			refResult, err := reader.CallContract(ctx, ethereum.CallMsg{To: &tokenContract, Data: refCallData}, nil)
			if err != nil {
				continue
			}
			refBalance := new(big.Int).SetBytes(refResult)
			if refBalance.Sign() == 0 {
				continue
			}

			// Found a reference holder with balance — probe slots against it
			for _, candidateSlot := range commonBalanceSlots {
				slotHash := erc20BalanceSlot(refAddr, candidateSlot)
				storageValue, err := reader.GetStorageAt(ctx, tokenContract, slotHash)
				if err != nil {
					continue
				}
				storedBalance := new(big.Int).SetBytes(storageValue)
				if storedBalance.Cmp(refBalance) == 0 {
					if s.logger != nil {
						s.logger.Info("Discovered ERC20 balance storage slot via reference holder",
							"token", tokenContract.Hex(),
							"referenceHolder", refAddr.Hex(),
							"slot", candidateSlot,
							"refBalance", refBalance.String())
					}
					discoveredSlot := candidateSlot
					s.cacheSlotResult(tokenKey, &discoveredSlot)
					return discoveredSlot, nil
				}
			}
		}

		// Last resort: default to slot 9 (USDC-like) since it's the most common
		// ERC20 proxy pattern and slot 0 rarely works for proxy contracts.
		defaultSlot := int64(9)
		if s.logger != nil {
			s.logger.Warn("Could not discover balance slot via reference holders, using default slot 9",
				"token", tokenContract.Hex(),
				"holder", holder.Hex())
		}
		s.cacheSlotResult(tokenKey, &defaultSlot)
		return defaultSlot, nil
	}

	s.cacheSlotResult(tokenKey, nil)
	return -1, fmt.Errorf("could not discover balance slot for token %s (tried slots %v)", tokenContract.Hex(), commonBalanceSlots)
}

func (s *SimulationStateMap) cacheSlotResult(tokenKey string, slot *int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.balanceSlotCache[tokenKey] = slot
}

// InjectERC20BalanceChange updates the state map to reflect an ERC20 balance change
// for the given holder on the given token contract. It queries the current on-chain
// balance via RPC, adds the delta, and sets the storage override.
func (s *SimulationStateMap) InjectERC20BalanceChange(
	ctx context.Context,
	smartWalletConfig *config.SmartWalletConfig,
	tokenContract common.Address,
	holder common.Address,
	delta *big.Int,
) error {
	if smartWalletConfig == nil {
		return fmt.Errorf("no smart wallet config available for ERC20 balance probing")
	}

	// Resolve the per-chain reader (worker-routed in gateway mode); fall back
	// to a direct dial only when no reader is registered.
	reader := GetChainStateReaderForChain(uint64(smartWalletConfig.ChainID))
	if reader == nil {
		if smartWalletConfig.EthRpcUrl == "" {
			return fmt.Errorf("no chain-state reader or RPC URL available for ERC20 balance probing")
		}
		client, dialErr := ethclient.DialContext(ctx, smartWalletConfig.EthRpcUrl)
		if dialErr != nil {
			return fmt.Errorf("failed to connect to RPC: %w", dialErr)
		}
		defer client.Close()
		reader = NewDirectChainStateReader(client, smartWalletConfig.ChainID)
	}

	// Discover the balance mapping slot
	mappingSlot, err := s.ProbeERC20BalanceSlot(ctx, reader, tokenContract, holder)
	if err != nil {
		return fmt.Errorf("failed to probe balance slot: %w", err)
	}

	// Get current on-chain balance
	balanceOfSig := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	callData := append(balanceOfSig, common.LeftPadBytes(holder.Bytes(), 32)...)

	result, err := reader.CallContract(ctx, ethereum.CallMsg{
		To:   &tokenContract,
		Data: callData,
	}, nil)
	if err != nil {
		return fmt.Errorf("balanceOf call failed: %w", err)
	}

	currentBalance := new(big.Int).SetBytes(result)
	newBalance := new(big.Int).Add(currentBalance, delta)
	if newBalance.Sign() < 0 {
		newBalance = big.NewInt(0)
	}

	// Compute the storage slot and set the override
	slotHash := erc20BalanceSlot(holder, mappingSlot)
	valueHex := fmt.Sprintf("0x%064x", newBalance)

	s.SetStorageSlot(tokenContract.Hex(), slotHash.Hex(), valueHex)

	if s.logger != nil {
		s.logger.Info("Injected ERC20 balance override for simulation",
			"token", tokenContract.Hex(),
			"holder", holder.Hex(),
			"currentBalance", currentBalance.String(),
			"delta", delta.String(),
			"newBalance", newBalance.String(),
			"slot", slotHash.Hex())
	}

	return nil
}

// InjectETHBalanceChange updates the state map to reflect an ETH balance change
// for the given address. It reads the current on-chain balance through the
// per-chain worker (gateway mode) and adds the delta, falling back to a direct
// dial only when no chain-state reader is registered — mirroring
// InjectERC20BalanceChange so the gateway issues no direct per-chain dial.
func (s *SimulationStateMap) InjectETHBalanceChange(
	ctx context.Context,
	smartWalletConfig *config.SmartWalletConfig,
	address common.Address,
	delta *big.Int,
) error {
	if smartWalletConfig == nil {
		return fmt.Errorf("no smart wallet config available for ETH balance probing")
	}

	// Resolve the per-chain reader (worker-routed in gateway mode); fall back
	// to a direct dial only when no reader is registered.
	reader := GetChainStateReaderForChain(uint64(smartWalletConfig.ChainID))
	if reader == nil {
		if smartWalletConfig.EthRpcUrl == "" {
			return fmt.Errorf("no chain-state reader or RPC URL available for ETH balance probing")
		}
		client, dialErr := ethclient.DialContext(ctx, smartWalletConfig.EthRpcUrl)
		if dialErr != nil {
			return fmt.Errorf("failed to connect to RPC for ETH balance: %w", dialErr)
		}
		defer client.Close()
		reader = NewDirectChainStateReader(client, smartWalletConfig.ChainID)
	}

	currentBalance, err := reader.GetBalance(ctx, address)
	if err != nil {
		return fmt.Errorf("failed to get ETH balance: %w", err)
	}

	newBalance := new(big.Int).Add(currentBalance, delta)
	if newBalance.Sign() < 0 {
		newBalance = big.NewInt(0)
	}

	balanceHex := fmt.Sprintf("0x%x", newBalance)
	s.SetETHBalance(address.Hex(), balanceHex)

	if s.logger != nil {
		s.logger.Info("Injected ETH balance override for simulation",
			"address", address.Hex(),
			"currentBalance", currentBalance.String(),
			"delta", delta.String(),
			"newBalance", newBalance.String())
	}

	return nil
}

// Default storage-slot indices used when a user-supplied ERC20 override does
// not specify them. These match the most common standard-ERC20 layout; tokens
// with non-standard layouts (e.g. USDC FiatToken at 9/10) must set the slots
// explicitly. See TENDERLY_STATE_OVERRIDES.md for a reference table.
const (
	defaultERC20BalanceSlot   = int64(0)
	defaultERC20AllowanceSlot = int64(3)
)

// maxUint256 is 2^256 - 1, the largest value an EVM storage word can hold.
var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// parseUint256 parses a balance/allowance override value supplied as either a
// 0x-prefixed hex string or a plain decimal string. The result is validated to
// be a non-negative value that fits in a uint256, so it can never produce an
// invalid EVM storage word.
func parseUint256(value string) (*big.Int, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, fmt.Errorf("empty value")
	}
	var n *big.Int
	var ok bool
	if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0X") {
		n, ok = new(big.Int).SetString(v[2:], 16)
		if !ok {
			return nil, fmt.Errorf("invalid hex value %q", value)
		}
	} else {
		n, ok = new(big.Int).SetString(v, 10)
		if !ok {
			return nil, fmt.Errorf("invalid decimal value %q", value)
		}
	}
	if n.Sign() < 0 {
		return nil, fmt.Errorf("value %q must be non-negative", value)
	}
	if n.Cmp(maxUint256) > 0 {
		return nil, fmt.Errorf("value %q exceeds uint256 max", value)
	}
	return n, nil
}

// erc20SlotOrDefault resolves the storage mapping slot for an override: the
// caller's explicit slot when supplied (bounds-checked so an out-of-range
// uint64 can't overflow into a bogus int64), otherwise the default slot for
// that mapping. Tokens with non-standard layouts (e.g. USDC FiatToken at 9/10)
// should pass the slot explicitly — see TENDERLY_STATE_OVERRIDES.md.
func erc20SlotOrDefault(explicit *uint64, def int64) (int64, error) {
	if explicit == nil {
		return def, nil
	}
	if *explicit > math.MaxInt64 {
		return 0, fmt.Errorf("storage slot %d exceeds the supported range", *explicit)
	}
	return int64(*explicit), nil
}

// ApplyUserERC20Override seeds the simulation state with a caller-supplied ERC20
// balance and/or allowance override. Unlike InjectERC20BalanceChange, the values
// are absolute (not deltas) and no RPC call is made — the caller provides the
// exact balance/allowance and (optionally) the mapping slots, so this is a pure,
// synchronous state mutation suitable for isolated RunNodeImmediately simulations.
//
// balance overrides the holder's balanceOf; allowance overrides
// allowance[owner][spender] and therefore requires a spender address.
func (s *SimulationStateMap) ApplyUserERC20Override(
	tokenAddress, ownerAddress, spenderAddress string,
	balance, allowance string,
	balanceSlot, allowanceSlot *uint64,
) error {
	if !common.IsHexAddress(tokenAddress) {
		return fmt.Errorf("invalid token_address %q", tokenAddress)
	}
	if !common.IsHexAddress(ownerAddress) {
		return fmt.Errorf("invalid owner_address %q", ownerAddress)
	}
	token := common.HexToAddress(tokenAddress)
	owner := common.HexToAddress(ownerAddress)

	if balance == "" && allowance == "" {
		return fmt.Errorf("ERC20 override for token %s specifies neither balance nor allowance", tokenAddress)
	}

	if balance != "" {
		bal, err := parseUint256(balance)
		if err != nil {
			return fmt.Errorf("balance override: %w", err)
		}
		slot, err := erc20SlotOrDefault(balanceSlot, defaultERC20BalanceSlot)
		if err != nil {
			return fmt.Errorf("balance override: %w", err)
		}
		slotHash := erc20BalanceSlot(owner, slot)
		s.SetStorageSlot(token.Hex(), slotHash.Hex(), fmt.Sprintf("0x%064x", bal))

		if s.logger != nil {
			s.logger.Info("Applied user ERC20 balance override for simulation",
				"token", token.Hex(),
				"owner", owner.Hex(),
				"balance", bal.String(),
				"slot", slotHash.Hex())
		}
	}

	if allowance != "" {
		if !common.IsHexAddress(spenderAddress) {
			return fmt.Errorf("allowance override for token %s requires a valid spender_address", tokenAddress)
		}
		spender := common.HexToAddress(spenderAddress)
		allow, err := parseUint256(allowance)
		if err != nil {
			return fmt.Errorf("allowance override: %w", err)
		}
		slot, err := erc20SlotOrDefault(allowanceSlot, defaultERC20AllowanceSlot)
		if err != nil {
			return fmt.Errorf("allowance override: %w", err)
		}
		slotHash := erc20AllowanceSlot(owner, spender, slot)
		s.SetStorageSlot(token.Hex(), slotHash.Hex(), fmt.Sprintf("0x%064x", allow))

		if s.logger != nil {
			s.logger.Info("Applied user ERC20 allowance override for simulation",
				"token", token.Hex(),
				"owner", owner.Hex(),
				"spender", spender.Hex(),
				"allowance", allow.String(),
				"slot", slotHash.Hex())
		}
	}

	return nil
}

// IsEmpty returns true if there are no accumulated state overrides.
func (s *SimulationStateMap) IsEmpty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.storage) == 0 && len(s.ethBalances) == 0
}

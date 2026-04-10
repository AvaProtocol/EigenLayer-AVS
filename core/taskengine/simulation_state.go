package taskengine

import (
	"context"
	"fmt"
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

// Common ERC20 balance mapping slot indices across different implementations.
// 0: standard OpenZeppelin ERC20, 1-3: various token implementations,
// 9: USDC (FiatTokenV2 proxy), 51: Compound cToken-style contracts.
var commonBalanceSlots = []int64{0, 1, 2, 3, 9, 51}

// ProbeERC20BalanceSlot discovers which storage slot a token contract uses for
// its _balances mapping by comparing eth_getStorageAt results against balanceOf.
// Returns the slot index, or an error if no match is found.
func (s *SimulationStateMap) ProbeERC20BalanceSlot(
	ctx context.Context,
	rpcURL string,
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

	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return -1, fmt.Errorf("failed to connect to RPC for slot probing: %w", err)
	}
	defer client.Close()

	// Call balanceOf(holder) to get the expected value
	balanceOfSig := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	callData := append(balanceOfSig, common.LeftPadBytes(holder.Bytes(), 32)...)

	result, err := client.CallContract(ctx, ethereum.CallMsg{
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
			storageValue, err := client.StorageAt(ctx, tokenContract, slotHash, nil)
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
			refResult, err := client.CallContract(ctx, ethereum.CallMsg{To: &tokenContract, Data: refCallData}, nil)
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
				storageValue, err := client.StorageAt(ctx, tokenContract, slotHash, nil)
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
	if smartWalletConfig == nil || smartWalletConfig.EthRpcUrl == "" {
		return fmt.Errorf("no RPC URL available for ERC20 balance probing")
	}

	rpcURL := smartWalletConfig.EthRpcUrl

	// Discover the balance mapping slot
	mappingSlot, err := s.ProbeERC20BalanceSlot(ctx, rpcURL, tokenContract, holder)
	if err != nil {
		return fmt.Errorf("failed to probe balance slot: %w", err)
	}

	// Get current on-chain balance
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer client.Close()

	balanceOfSig := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	callData := append(balanceOfSig, common.LeftPadBytes(holder.Bytes(), 32)...)

	result, err := client.CallContract(ctx, ethereum.CallMsg{
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
// for the given address. It queries the current on-chain balance via RPC and adds the delta.
func (s *SimulationStateMap) InjectETHBalanceChange(
	ctx context.Context,
	rpcURL string,
	address common.Address,
	delta *big.Int,
) error {
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RPC for ETH balance: %w", err)
	}
	defer client.Close()

	currentBalance, err := client.BalanceAt(ctx, address, nil)
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

// IsEmpty returns true if there are no accumulated state overrides.
func (s *SimulationStateMap) IsEmpty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.storage) == 0 && len(s.ethBalances) == 0
}

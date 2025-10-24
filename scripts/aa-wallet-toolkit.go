package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/paymaster"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"gopkg.in/yaml.v3"
)

func main() {
	// Command-line flags
	mode := flag.String("mode", "verify-paymaster", "Operation mode: clear-mempool, verify-paymaster, or all")
	configPath := flag.String("config", "config/aggregator-sepolia.yaml", "Path to aggregator config file")
	flag.Parse()

	fmt.Println("AA Wallet Toolkit")
	fmt.Println("=================")
	fmt.Println()

	switch *mode {
	case "clear-mempool":
		clearMempool(*configPath)
	case "verify-paymaster":
		verifyPaymaster(*configPath)
	case "all":
		// Run all diagnostic steps
		fmt.Println("📋 Running all diagnostic steps...")
		fmt.Println()
		verifyPaymaster(*configPath)
		fmt.Println("\n" + strings.Repeat("=", 60) + "\n")
		clearMempool(*configPath)
	default:
		log.Fatalf("Unknown mode: %s. Use: clear-mempool, verify-paymaster, or all", *mode)
	}
}

// Step 1: Verify Paymaster Configuration
func verifyPaymaster(configPath string) {
	fmt.Println("🔍 Step 1: Verify Paymaster Configuration")
	fmt.Println("==========================================")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Printf("❌ Failed to load config: %v", err)
		return
	}

	// Connect to RPC
	client, err := ethclient.Dial(cfg.EthRpcUrl)
	if err != nil {
		log.Printf("❌ Failed to connect to RPC: %v", err)
		return
	}
	defer client.Close()

	// Create paymaster contract instance
	pm, err := paymaster.NewPayMaster(cfg.PaymasterAddress, client)
	if err != nil {
		log.Printf("❌ Failed to create paymaster contract: %v", err)
		return
	}

	// Get paymaster signer
	signer, err := pm.VerifyingSigner(nil)
	if err != nil {
		log.Printf("❌ Failed to get verifying signer: %v", err)
		return
	}

	// Get paymaster deposit
	deposit, err := pm.GetDeposit(nil)
	if err != nil {
		log.Printf("❌ Failed to get deposit: %v", err)
		return
	}

	fmt.Printf("Paymaster Address:  %s\n", cfg.PaymasterAddress.Hex())
	fmt.Printf("Verifying Signer:   %s\n", signer.Hex())
	fmt.Printf("Deposit:            %s wei (%.6f ETH)\n", deposit.String(), float64(deposit.Int64())/1e18)
	fmt.Printf("Controller Address: %s\n", cfg.ControllerAddress.Hex())
	fmt.Println()

	if strings.EqualFold(cfg.ControllerAddress.Hex(), signer.Hex()) {
		fmt.Println("✅ Controller key MATCHES paymaster signer - OK!")
	} else {
		fmt.Println("❌ Controller key DOES NOT MATCH paymaster signer!")
		fmt.Printf("   Expected: %s\n", signer.Hex())
		fmt.Printf("   Got:      %s\n", cfg.ControllerAddress.Hex())
		fmt.Println()
		fmt.Println("💡 Fix: Update controller_private_key in config or deploy new paymaster")
	}

	if deposit.Cmp(big.NewInt(0)) == 0 {
		fmt.Println()
		fmt.Println("⚠️  WARNING: Paymaster has ZERO deposit!")
		fmt.Println("   Solution: Fund the paymaster with ETH using:")
		fmt.Printf("   cast send %s --value 0.1ether --rpc-url <rpc>\n", cfg.PaymasterAddress.Hex())
	}
}

// Step 2: Clear Bundler Mempool
func clearMempool(configPath string) {
	fmt.Println("🧹 Step 2: Clear Bundler Mempool")
	fmt.Println("=================================")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Printf("❌ Failed to load config: %v", err)
		return
	}

	entrypoint := config.DefaultEntrypointAddressHex

	// Check mempool
	resp, err := bundlerRPC(cfg.BundlerURL, "debug_bundler_dumpMempool", []interface{}{entrypoint})
	if err != nil {
		log.Printf("❌ Failed to check mempool: %v", err)
		return
	}

	var mempoolResult struct {
		Result []map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(resp, &mempoolResult); err != nil {
		log.Printf("❌ Failed to parse mempool response: %v", err)
		return
	}

	userOpCount := len(mempoolResult.Result)
	if userOpCount == 0 {
		fmt.Println("✅ Mempool is empty - no stuck UserOps")
		return
	}

	fmt.Printf("⚠️  Found %d UserOp(s) in mempool\n\n", userOpCount)

	// Show stuck UserOps
	for i, userOp := range mempoolResult.Result {
		sender, _ := userOp["sender"].(string)
		nonce, _ := userOp["nonce"].(string)
		initCode, _ := userOp["initCode"].(string)
		paymasterData, _ := userOp["paymasterAndData"].(string)

		fmt.Printf("  [%d] Sender: %s\n", i+1, sender)
		fmt.Printf("      Nonce: %s\n", nonce)
		fmt.Printf("      Has InitCode: %v\n", initCode != "" && initCode != "0x")
		fmt.Printf("      Has Paymaster: %v\n", paymasterData != "" && paymasterData != "0x")
		fmt.Println()
	}

	// Try to force process
	fmt.Println("🚀 Attempting to force-process stuck UserOps...")
	_, err = bundlerRPC(cfg.BundlerURL, "debug_bundler_sendBundleNow", []interface{}{})
	if err != nil {
		log.Printf("❌ Failed to send bundle: %v", err)
		return
	}

	// Wait and check again
	time.Sleep(2 * time.Second)
	resp2, _ := bundlerRPC(cfg.BundlerURL, "debug_bundler_dumpMempool", []interface{}{entrypoint})
	var mempoolResult2 struct {
		Result []map[string]interface{} `json:"result"`
	}
	json.Unmarshal(resp2, &mempoolResult2)

	if len(mempoolResult2.Result) == 0 {
		fmt.Println("✅ Mempool cleared successfully!")
	} else {
		fmt.Printf("⚠️  Mempool still has %d UserOp(s)\n", len(mempoolResult2.Result))
		fmt.Println("   May need to fund wallets or restart bundler")
	}
}

// bundlerRPC makes an RPC call to the bundler
func bundlerRPC(bundlerURL, method string, params []interface{}) ([]byte, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(bundlerURL, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// YAMLConfig structure matching aggregator-sepolia.yaml
type YAMLConfig struct {
	SmartWallet struct {
		EthRpcUrl            string `yaml:"eth_rpc_url"`
		EthWsUrl             string `yaml:"eth_ws_url"`
		BundlerURL           string `yaml:"bundler_url"`
		ControllerPrivateKey string `yaml:"controller_private_key"`
		FactoryAddress       string `yaml:"factory_address"`
		EntrypointAddress    string `yaml:"entrypoint_address"`
		PaymasterAddress     string `yaml:"paymaster_address"`
		MaxWalletsPerOwner   int    `yaml:"max_wallets_per_owner"`
	} `yaml:"smart_wallet"`
}

func loadConfig(path string) (*config.SmartWalletConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var yamlCfg YAMLConfig
	if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	client, err := ethclient.Dial(yamlCfg.SmartWallet.EthRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer client.Close()

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	// Use addresses from YAML with fallback to defaults
	factoryAddr := common.HexToAddress(config.DefaultFactoryProxyAddressHex)
	if yamlCfg.SmartWallet.FactoryAddress != "" {
		factoryAddr = common.HexToAddress(yamlCfg.SmartWallet.FactoryAddress)
	}

	entrypointAddr := common.HexToAddress(config.DefaultEntrypointAddressHex)
	if yamlCfg.SmartWallet.EntrypointAddress != "" {
		entrypointAddr = common.HexToAddress(yamlCfg.SmartWallet.EntrypointAddress)
	}

	paymasterAddr := common.HexToAddress(config.DefaultPaymasterAddressHex)
	if yamlCfg.SmartWallet.PaymasterAddress != "" {
		paymasterAddr = common.HexToAddress(yamlCfg.SmartWallet.PaymasterAddress)
	}

	controllerKey, err := crypto.HexToECDSA(yamlCfg.SmartWallet.ControllerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse controller private key: %w", err)
	}

	// Derive controller address from the private key
	controllerAddr := crypto.PubkeyToAddress(controllerKey.PublicKey)

	return &config.SmartWalletConfig{
		EthRpcUrl:            yamlCfg.SmartWallet.EthRpcUrl,
		EthWsUrl:             yamlCfg.SmartWallet.EthWsUrl,
		BundlerURL:           yamlCfg.SmartWallet.BundlerURL,
		ControllerPrivateKey: controllerKey,
		ControllerAddress:    controllerAddr,
		FactoryAddress:       factoryAddr,
		EntrypointAddress:    entrypointAddr,
		PaymasterAddress:     paymasterAddr,
		ChainID:              chainID.Int64(),
		MaxWalletsPerOwner:   yamlCfg.SmartWallet.MaxWalletsPerOwner,
	}, nil
}

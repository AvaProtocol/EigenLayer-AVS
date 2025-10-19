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

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/paymaster"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"gopkg.in/yaml.v3"
)

func main() {
	// Command-line flags
	mode := flag.String("mode", "check", "Operation mode: check, deploy, clear-mempool, verify-paymaster, or all")
	configPath := flag.String("config", "config/aggregator-sepolia.yaml", "Path to aggregator config file")
	ownerPrivateKey := flag.String("owner-key", "", "Owner EOA private key (overrides TEST_PRIVATE_KEY env var)")
	salt := flag.Int64("salt", 0, "Salt value for smart wallet derivation (default: 0)")
	flag.Parse()

	fmt.Println("🛠️  Smart Wallet Management Tool")
	fmt.Println("=================================")
	fmt.Println()

	switch *mode {
	case "check":
		checkWallet(*configPath, *ownerPrivateKey, *salt)
	case "deploy":
		deployWallet(*configPath, *ownerPrivateKey, *salt)
	case "clear-mempool":
		clearMempool(*configPath)
	case "verify-paymaster":
		verifyPaymaster(*configPath)
	case "all":
		// Run all steps in sequence
		fmt.Println("📋 Running all diagnostic steps...")
		fmt.Println()
		verifyPaymaster(*configPath)
		fmt.Println("\n" + strings.Repeat("=", 60) + "\n")
		clearMempool(*configPath)
		fmt.Println("\n" + strings.Repeat("=", 60) + "\n")
		checkWallet(*configPath, *ownerPrivateKey, *salt)
		fmt.Println("\n" + strings.Repeat("=", 60) + "\n")
		deployWallet(*configPath, *ownerPrivateKey, *salt)
	default:
		log.Fatalf("Unknown mode: %s. Use: check, deploy, clear-mempool, verify-paymaster, or all", *mode)
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

	// Get controller address from private key
	controllerAddr := crypto.PubkeyToAddress(cfg.ControllerPrivateKey.PublicKey)

	fmt.Printf("Paymaster Address:  %s\n", cfg.PaymasterAddress.Hex())
	fmt.Printf("Verifying Signer:   %s\n", signer.Hex())
	fmt.Printf("Deposit:            %s wei (%.6f ETH)\n", deposit.String(), float64(deposit.Int64())/1e18)
	fmt.Printf("Controller Address: %s\n", controllerAddr.Hex())
	fmt.Println()

	if strings.EqualFold(controllerAddr.Hex(), signer.Hex()) {
		fmt.Println("✅ Controller key MATCHES paymaster signer - OK!")
	} else {
		fmt.Println("❌ Controller key DOES NOT MATCH paymaster signer!")
		fmt.Printf("   Expected: %s\n", signer.Hex())
		fmt.Printf("   Got:      %s\n", controllerAddr.Hex())
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

// Step 3: Check Wallet Status
func checkWallet(configPath, ownerKey string, salt int64) {
	fmt.Println("📊 Step 3: Check Wallet Status")
	fmt.Println("===============================")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}

	// Get owner private key
	if ownerKey == "" {
		ownerKey = os.Getenv("TEST_PRIVATE_KEY")
	}
	if ownerKey == "" {
		log.Fatal("❌ Owner private key required. Set TEST_PRIVATE_KEY env var or use --owner-key flag")
	}

	ownerECDSA, err := crypto.HexToECDSA(ownerKey)
	if err != nil {
		log.Fatalf("❌ Failed to parse owner private key: %v", err)
	}
	ownerAddress := crypto.PubkeyToAddress(ownerECDSA.PublicKey)

	fmt.Printf("Owner EOA: %s\n", ownerAddress.Hex())
	fmt.Printf("Salt:      %d\n", salt)

	// Connect to RPC
	client, err := ethclient.Dial(cfg.EthRpcUrl)
	if err != nil {
		log.Fatalf("❌ Failed to connect to RPC: %v", err)
	}
	defer client.Close()

	// Set up AA configuration
	aa.SetFactoryAddress(cfg.FactoryAddress)
	aa.SetEntrypointAddress(cfg.EntrypointAddress)

	// Derive smart wallet address
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(salt))
	if err != nil {
		log.Fatalf("❌ Failed to derive smart wallet address: %v", err)
	}

	fmt.Printf("Wallet:    %s\n\n", smartWalletAddr.Hex())

	// Check if deployed
	code, err := client.CodeAt(context.Background(), *smartWalletAddr, nil)
	if err != nil {
		log.Fatalf("❌ Failed to check contract code: %v", err)
	}

	if len(code) > 0 {
		fmt.Printf("✅ Wallet is DEPLOYED\n")
		fmt.Printf("   Code size: %d bytes\n", len(code))

		// Show balance
		balance, _ := client.BalanceAt(context.Background(), *smartWalletAddr, nil)
		if balance != nil {
			fmt.Printf("   Balance: %s wei (%.6f ETH)\n", balance.String(), float64(balance.Int64())/1e18)
		}

		// Show EntryPoint deposit
		entryPoint, err := aa.NewEntryPoint(cfg.EntrypointAddress, client)
		if err == nil {
			depositInfo, err := entryPoint.GetDepositInfo(nil, *smartWalletAddr)
			if err == nil {
				fmt.Printf("   EntryPoint deposit: %s wei (%.6f ETH)\n", depositInfo.Deposit.String(), float64(depositInfo.Deposit.Int64())/1e18)
			}
		}
	} else {
		fmt.Println("⚠️  Wallet is NOT deployed")
		fmt.Println("   Run with --mode=deploy to deploy it")
	}
}

// Step 4: Deploy Wallet
func deployWallet(configPath, ownerKey string, salt int64) {
	fmt.Println("🚀 Step 4: Deploy Smart Wallet")
	fmt.Println("===============================")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}

	// Get owner private key
	if ownerKey == "" {
		ownerKey = os.Getenv("TEST_PRIVATE_KEY")
	}
	if ownerKey == "" {
		log.Fatal("❌ Owner private key required. Set TEST_PRIVATE_KEY env var or use --owner-key flag")
	}

	ownerECDSA, err := crypto.HexToECDSA(ownerKey)
	if err != nil {
		log.Fatalf("❌ Failed to parse owner private key: %v", err)
	}
	ownerAddress := crypto.PubkeyToAddress(ownerECDSA.PublicKey)

	// Connect to RPC
	client, err := ethclient.Dial(cfg.EthRpcUrl)
	if err != nil {
		log.Fatalf("❌ Failed to connect to RPC: %v", err)
	}
	defer client.Close()

	// Set up AA configuration
	aa.SetFactoryAddress(cfg.FactoryAddress)
	aa.SetEntrypointAddress(cfg.EntrypointAddress)

	// Derive smart wallet address
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(salt))
	if err != nil {
		log.Fatalf("❌ Failed to derive smart wallet address: %v", err)
	}

	fmt.Printf("Owner EOA: %s\n", ownerAddress.Hex())
	fmt.Printf("Wallet:    %s\n", smartWalletAddr.Hex())
	fmt.Printf("Salt:      %d\n\n", salt)

	// Check if already deployed
	code, err := client.CodeAt(context.Background(), *smartWalletAddr, nil)
	if err != nil {
		log.Fatalf("❌ Failed to check contract code: %v", err)
	}

	if len(code) > 0 {
		fmt.Println("✅ Wallet is already deployed - skipping")
		return
	}

	// Deploy
	fmt.Printf("Factory:    %s\n", cfg.FactoryAddress.Hex())
	fmt.Printf("EntryPoint: %s\n", cfg.EntrypointAddress.Hex())
	fmt.Printf("Paymaster:  %s\n", cfg.PaymasterAddress.Hex())
	fmt.Printf("Bundler:    %s\n\n", cfg.BundlerURL)

	// Create minimal calldata
	emptyCallData, err := aa.PackExecute(ownerAddress, big.NewInt(0), []byte{})
	if err != nil {
		log.Fatalf("❌ Failed to pack execute calldata: %v", err)
	}

	// Create paymaster request
	paymasterReq := preset.GetVerifyingPaymasterRequestForDuration(
		cfg.PaymasterAddress,
		15*time.Minute,
	)

	fmt.Println("📤 Sending UserOp to deploy wallet...")

	// Send UserOp
	userOp, receipt, err := preset.SendUserOp(
		cfg,
		ownerAddress,
		emptyCallData,
		paymasterReq,
		smartWalletAddr,
		nil,
	)

	if err != nil {
		log.Fatalf("❌ Failed to send UserOp: %v", err)
	}

	fmt.Println()
	if receipt != nil {
		fmt.Printf("✅ Wallet deployed successfully!\n")
		fmt.Printf("   Transaction: %s\n", receipt.TxHash.Hex())
		fmt.Printf("   Block: %d\n", receipt.BlockNumber.Uint64())
		fmt.Printf("   Gas used: %d\n", receipt.GasUsed)
		fmt.Printf("   Status: %d\n", receipt.Status)
	} else if userOp != nil {
		fmt.Printf("⏳ UserOp submitted (pending)\n")
		userOpHash := userOp.GetUserOpHash(cfg.EntrypointAddress, big.NewInt(cfg.ChainID))
		fmt.Printf("   UserOp Hash: %s\n", userOpHash.Hex())
	}

	// Verify deployment
	fmt.Println("\n🔍 Verifying deployment...")
	time.Sleep(3 * time.Second)

	code, _ = client.CodeAt(context.Background(), *smartWalletAddr, nil)
	if len(code) > 0 {
		fmt.Printf("✅ Deployment verified! Code size: %d bytes\n", len(code))
	} else {
		fmt.Println("⚠️  Not yet confirmed. Check again in a few seconds.")
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

	return &config.SmartWalletConfig{
		EthRpcUrl:            yamlCfg.SmartWallet.EthRpcUrl,
		EthWsUrl:             yamlCfg.SmartWallet.EthWsUrl,
		BundlerURL:           yamlCfg.SmartWallet.BundlerURL,
		ControllerPrivateKey: controllerKey,
		FactoryAddress:       factoryAddr,
		EntrypointAddress:    entrypointAddr,
		PaymasterAddress:     paymasterAddr,
		ChainID:              chainID.Int64(),
		MaxWalletsPerOwner:   yamlCfg.SmartWallet.MaxWalletsPerOwner,
	}, nil
}

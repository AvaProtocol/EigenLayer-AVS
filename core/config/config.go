package config

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v2"

	"github.com/Layr-Labs/eigensdk-go/chainio/clients/eth"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients/wallet"
	"github.com/Layr-Labs/eigensdk-go/chainio/txmgr"
	"github.com/Layr-Labs/eigensdk-go/crypto/bls"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	rpccalls "github.com/Layr-Labs/eigensdk-go/metrics/collectors/rpc_calls"
	"github.com/Layr-Labs/eigensdk-go/signerv2"

	sdkutils "github.com/Layr-Labs/eigensdk-go/utils"
)

// DefaultMaxWalletsPerOwner defines the default cap used when the YAML config omits
// or specifies a non-positive value for max wallets per owner. Unlimited is not supported.
const DefaultMaxWalletsPerOwner = 5

// HardMaxWalletsPerOwner defines the absolute maximum allowed, regardless of config.
// Any configured value above this will be clamped down to this hard limit.
const HardMaxWalletsPerOwner = 2000

// DefaultFactoryProxyAddressHex is the default smart wallet factory Proxy address
// deployed across supported chains. If the aggregator config omits the
// smart_wallet.factory_address field, this value will be used.
const DefaultFactoryProxyAddressHex = "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"

// DefaultEntrypointAddressHex is the default ERC-4337 EntryPoint address used
// across supported chains. If the aggregator config omits the
// smart_wallet.entrypoint_address field, this value will be used.
const DefaultEntrypointAddressHex = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"

// DefaultPaymasterAddressHex is the default VerifyingPaymaster address
// controlled by us and deployed uniformly across supported chains. If the
// aggregator config omits the smart_wallet.paymaster_address field, this
// value will be used.
const DefaultPaymasterAddressHex = "0xB985af5f96EF2722DC99aEBA573520903B86505e"

// Config contains all of the configuration information for a credible squaring aggregators and challengers.
// Operators use a separate config. (see config-files/operator.anvil.yaml)
type Config struct {
	EcdsaPrivateKey           *ecdsa.PrivateKey
	BlsPrivateKey             *bls.PrivateKey
	Logger                    sdklogging.Logger
	EigenMetricsIpPortAddress string
	SentryDsn                 string
	ServerName                string

	// we need the url for the eigensdk currently... eventually standardize api so as to
	// only take an ethclient or an rpcUrl (and build the ethclient at each constructor site)
	EthHttpRpcUrl                     string
	EthWsRpcUrl                       string
	EthHttpClient                     *eth.InstrumentedClient
	EthWsClient                       *eth.InstrumentedClient
	OperatorStateRetrieverAddr        common.Address
	AutomationRegistryCoordinatorAddr common.Address
	RpcBindAddress                    string
	HttpBindAddress                   string
	RegisterOperatorOnStartup         bool
	// json:"-" skips this field when marshaling (only used for logging to stdout), since SignerFn doesnt implement marshalJson
	SignerFn          signerv2.SignerFn `json:"-"`
	TxMgr             txmgr.TxManager
	AggregatorAddress common.Address

	DbPath    string
	BackupDir string

	JwtSecret []byte

	// Account abstraction config
	SmartWallet *SmartWalletConfig

	BackupConfig BackupConfig

	SocketPath  string
	Environment sdklogging.LogLevel

	MacroVars    map[string]string
	MacroSecrets map[string]string

	// List of approved operator addresses that can process tasks
	ApprovedOperators []common.Address

	MetricsReg *prometheus.Registry

	// Tenderly HTTP Simulation API credentials (optional)
	TenderlyAccount   string `yaml:"tenderly_account"`
	TenderlyProject   string `yaml:"tenderly_project"`
	TenderlyAccessKey string `yaml:"tenderly_access_key"`

	// Test private key for Go tests (optional)
	TestPrivateKey string
}

type SmartWalletConfig struct {
	EthRpcUrl         string
	EthWsUrl          string
	BundlerURL        string
	FactoryAddress    common.Address
	EntrypointAddress common.Address
	// ChainID of the connected network (derived at runtime from RPC)
	ChainID int64

	ControllerPrivateKey *ecdsa.PrivateKey
	PaymasterAddress     common.Address
	WhitelistAddresses   []common.Address

	// Maximum number of smart wallets allowed per EOA owner
	// Unlimited is NOT supported. If zero or negative, the default limit is applied.
	MaxWalletsPerOwner int
}

type BackupConfig struct {
	Enabled         bool   // Whether periodic backups are enabled
	IntervalMinutes int    // Interval between backups in minutes
	BackupDir       string // Directory to store backups
}

// These are read from configPath
type ConfigRaw struct {
	EcdsaPrivateKey string              `yaml:"ecdsa_private_key"`
	Environment     sdklogging.LogLevel `yaml:"environment"`
	EthRpcUrl       string              `yaml:"eth_rpc_url"`
	EthWsUrl        string              `yaml:"eth_ws_url"`
	SentryDsn       string              `yaml:"sentry_dsn,omitempty"`
	ServerName      string              `yaml:"server_name,omitempty"`

	RpcBindAddress  string `yaml:"rpc_bind_address"`
	HttpBindAddress string `yaml:"http_bind_address"`

	OperatorStateRetrieverAddr string `yaml:"operator_state_retriever_address"`
	AVSRegistryCoordinatorAddr string `yaml:"avs_registry_coordinator_address"`

	DbPath    string `yaml:"db_path"`
	BackupDir string `yaml:"backup_dir"`
	JwtSecret string `yaml:"jwt_secret"`

	SmartWallet struct {
		EthRpcUrl            string   `yaml:"eth_rpc_url"`
		EthWsUrl             string   `yaml:"eth_ws_url"`
		BundlerURL           string   `yaml:"bundler_url"`
		FactoryAddress       string   `yaml:"factory_address"`
		EntrypointAddress    string   `yaml:"entrypoint_address"`
		ControllerPrivateKey string   `yaml:"controller_private_key"`
		PaymasterAddress     string   `yaml:"paymaster_address"`
		WhitelistAddresses   []string `yaml:"whitelist_addresses"`
		MaxWalletsPerOwner   int      `yaml:"max_wallets_per_owner"`
	} `yaml:"smart_wallet"`

	Backup struct {
		Enabled         bool   `yaml:"enabled"`
		IntervalMinutes int    `yaml:"interval_minutes"`
		BackupDir       string `yaml:"backup_dir"`
	} `yaml:"backup"`

	SocketPath string `yaml:"socket_path"`

	// List of approved operator addresses that can process tasks
	ApprovedOperators []string `yaml:"approved_operators"`

	Macros map[string]map[string]string `yaml:"macros"`

	// Tenderly HTTP Simulation API credentials (optional)
	TenderlyAccount   string `yaml:"tenderly_account"`
	TenderlyProject   string `yaml:"tenderly_project"`
	TenderlyAccessKey string `yaml:"tenderly_access_key"`

	// Test private key for Go tests (optional)
	TestPrivateKey string `yaml:"test_private_key"`
}

// These are read from CredibleSquaringDeploymentFileFlag
type AutomationDeploymentRaw struct {
	Addresses AutomationContractsRaw `json:"addresses"`
}

type AutomationContractsRaw struct {
	RegistryCoordinatorAddr    string `json:"registryCoordinator"`
	OperatorStateRetrieverAddr string `json:"operatorStateRetriever"`
}

// NewConfig parses config file to read from from flags or environment variables
// Note: This config is shared by challenger and aggregator and so we put in the core.
// Operator has a different config and is meant to be used by the operator CLI.
func NewConfig(configFilePath string) (*Config, error) {
	var configRaw ConfigRaw
	if configFilePath != "" {
		if err := ReadYamlConfig(configFilePath, &configRaw); err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	logger, err := sdklogging.NewZapLogger(configRaw.Environment)
	if err != nil {
		return nil, err
	}

	reg := prometheus.NewRegistry()
	rpcCallsCollector := rpccalls.NewCollector("exampleAvs", reg)

	ethRpcClient, err := eth.NewInstrumentedClient(configRaw.EthRpcUrl, rpcCallsCollector)
	if err != nil {
		// In CI environment, gracefully handle HTTP RPC connection failures
		if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
			logger.Warnf("HTTP RPC client connection failed in CI environment, setting to nil: %v", err)
			ethRpcClient = nil
		} else {
			logger.Errorf("Cannot create http ethclient", "err", err)
			return nil, err
		}
	}

	ethWsClient, err := eth.NewInstrumentedClient(configRaw.EthWsUrl, rpcCallsCollector)
	if err != nil {
		// In CI environment, gracefully handle WebSocket connection failures
		if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
			logger.Warnf("WebSocket client connection failed in CI environment, setting to nil: %v", err)
			ethWsClient = nil
		} else {
			logger.Errorf("Cannot create ws ethclient", "err", err)
			return nil, err
		}
	}

	ecdsaPrivateKeyString := configRaw.EcdsaPrivateKey
	ecdsaPrivateKey, err := crypto.HexToECDSA(ecdsaPrivateKeyString)
	if err != nil {
		logger.Errorf("Cannot parse ecdsa private key", "err", err)
		return nil, err
	}

	aggregatorAddr, err := sdkutils.EcdsaPrivateKeyToAddress(ecdsaPrivateKey)
	if err != nil {
		logger.Error("Cannot get operator address", "err", err)
		return nil, err
	}

	// Validate that smart wallet RPC URL is configured - this is critical for contract operations
	if configRaw.SmartWallet.EthRpcUrl == "" {
		logger.Error("smart_wallet.eth_rpc_url is required but not configured")
		logger.Error("smart_wallet.eth_rpc_url is required but not configured. This RPC URL is critical for Base aggregator contract operations; without it, the aggregator cannot interact with the blockchain.")
		return nil, fmt.Errorf("critical configuration error: smart_wallet.eth_rpc_url must be set because it is required for Base aggregator contract operations")
	}

	// Create separate RPC client for smart wallet operations (contract read/write)
	smartWalletRpcClient, err := eth.NewInstrumentedClient(configRaw.SmartWallet.EthRpcUrl, rpcCallsCollector)
	if err != nil {
		logger.Error("Cannot create smart wallet RPC client", "url", configRaw.SmartWallet.EthRpcUrl, "err", err)
		return nil, fmt.Errorf("critical error: failed to connect to smart wallet RPC: %w", err)
	}

	// Get chain ID from smart wallet RPC (for contract operations)
	smartWalletChainId, err := smartWalletRpcClient.ChainID(context.Background())
	if err != nil {
		logger.Error("Cannot get smart wallet chainId", "url", configRaw.SmartWallet.EthRpcUrl, "err", err)
		return nil, fmt.Errorf("critical error: failed to get chain ID from smart wallet RPC: %w", err)
	}

	// Get chain ID from main EigenLayer RPC (for EigenLayer operations)
	eigenLayerChainId, err := ethRpcClient.ChainID(context.Background())
	if err != nil {
		logger.Error("Cannot get EigenLayer chainId", "url", configRaw.EthRpcUrl, "err", err)
		return nil, fmt.Errorf("failed to get chain ID from EigenLayer RPC: %w", err)
	}

	logger.Info("Chain ID configuration",
		"eigenlayer_chain_id", eigenLayerChainId.Int64(),
		"eigenlayer_rpc", configRaw.EthRpcUrl,
		"smart_wallet_chain_id", smartWalletChainId.Int64(),
		"smart_wallet_rpc", configRaw.SmartWallet.EthRpcUrl)

	signerV2, _, err := signerv2.SignerFromConfig(signerv2.Config{PrivateKey: ecdsaPrivateKey}, eigenLayerChainId)
	if err != nil {
		panic(err)
	}

	skWallet, err := wallet.NewPrivateKeyWallet(ethRpcClient, signerV2, aggregatorAddr, logger)
	if err != nil {
		panic(err)
	}

	txMgr := txmgr.NewSimpleTxManager(skWallet, ethRpcClient, logger, aggregatorAddr)

	controllerPrivateKey, err := crypto.HexToECDSA(configRaw.SmartWallet.ControllerPrivateKey)
	if err != nil {
		panic(err)
	}

	// Enforce sane default for max wallets per owner (no unlimited allowed)
	if configRaw.SmartWallet.MaxWalletsPerOwner <= 0 {
		configRaw.SmartWallet.MaxWalletsPerOwner = DefaultMaxWalletsPerOwner
	}
	// Enforce hard upper bound to prevent abuse
	if configRaw.SmartWallet.MaxWalletsPerOwner > HardMaxWalletsPerOwner {
		configRaw.SmartWallet.MaxWalletsPerOwner = HardMaxWalletsPerOwner
	}

	if configRaw.BackupDir == "" {
		// If backup dir is not set, use the default path, usually this path will be mount from our docker compose host
		configRaw.BackupDir = "/tmp/ap-avs-backup"
	}

	config := &Config{
		EcdsaPrivateKey: ecdsaPrivateKey,
		Logger:          logger,
		SentryDsn:       configRaw.SentryDsn,
		ServerName:      configRaw.ServerName,
		EthWsRpcUrl:     configRaw.EthWsUrl,
		EthHttpRpcUrl:   configRaw.EthRpcUrl,
		EthHttpClient:   ethRpcClient,
		EthWsClient:     ethWsClient,

		Environment:                       configRaw.Environment,
		OperatorStateRetrieverAddr:        common.HexToAddress(configRaw.OperatorStateRetrieverAddr),
		AutomationRegistryCoordinatorAddr: common.HexToAddress(configRaw.AVSRegistryCoordinatorAddr),
		RpcBindAddress:                    configRaw.RpcBindAddress,
		HttpBindAddress:                   configRaw.HttpBindAddress,
		SignerFn:                          signerV2,
		TxMgr:                             txMgr,
		AggregatorAddress:                 aggregatorAddr,

		DbPath:    configRaw.DbPath,
		BackupDir: configRaw.BackupDir,
		JwtSecret: []byte(configRaw.JwtSecret),

		SmartWallet: &SmartWalletConfig{
			EthRpcUrl:            configRaw.SmartWallet.EthRpcUrl,
			EthWsUrl:             configRaw.SmartWallet.EthWsUrl,
			BundlerURL:           configRaw.SmartWallet.BundlerURL,
			FactoryAddress:       common.HexToAddress(firstNonEmpty(configRaw.SmartWallet.FactoryAddress, DefaultFactoryProxyAddressHex)),
			EntrypointAddress:    common.HexToAddress(firstNonEmpty(configRaw.SmartWallet.EntrypointAddress, DefaultEntrypointAddressHex)),
			ChainID:              smartWalletChainId.Int64(), // Use smart wallet chain ID, not EigenLayer chain ID (prevents cross-chain configuration errors for Base aggregator)
			ControllerPrivateKey: controllerPrivateKey,
			PaymasterAddress:     common.HexToAddress(firstNonEmpty(configRaw.SmartWallet.PaymasterAddress, DefaultPaymasterAddressHex)),
			WhitelistAddresses:   convertToAddressSlice(configRaw.SmartWallet.WhitelistAddresses),
			MaxWalletsPerOwner:   configRaw.SmartWallet.MaxWalletsPerOwner,
		},

		BackupConfig: BackupConfig{
			Enabled:         configRaw.Backup.Enabled,
			IntervalMinutes: configRaw.Backup.IntervalMinutes,
			BackupDir:       configRaw.Backup.BackupDir,
		},

		SocketPath:        configRaw.SocketPath,
		MacroVars:         configRaw.Macros["vars"],
		MacroSecrets:      configRaw.Macros["secrets"],
		ApprovedOperators: convertToAddressSlice(configRaw.ApprovedOperators),
		MetricsReg:        reg,

		// Pass through Tenderly credentials (if provided in YAML)
		TenderlyAccount:   configRaw.TenderlyAccount,
		TenderlyProject:   configRaw.TenderlyProject,
		TenderlyAccessKey: configRaw.TenderlyAccessKey,

		// Pass through test private key (if provided in YAML)
		TestPrivateKey: configRaw.TestPrivateKey,
	}

	if config.SocketPath == "" {
		config.SocketPath = "/tmp/ap.sock"
	}
	// If HttpBindAddress is empty, HTTP server will be disabled (startup code will skip starting it)
	config.validate()
	return config, nil
}

func (c *Config) validate() {
	// TODO: make sure every pointer is non-nil
	if c.OperatorStateRetrieverAddr == common.HexToAddress("") {
		panic("Config: BLSOperatorStateRetrieverAddr is required")
	}
	if c.AutomationRegistryCoordinatorAddr == common.HexToAddress("") {
		panic("Config: AutomationRegistryCoordinatorAddr is required")
	}
}

// firstNonEmpty returns the first non-empty string among the arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func ReadYamlConfig(path string, o interface{}) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		log.Fatal("Path ", path, " does not exist")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	err = yaml.Unmarshal(b, o)
	if err != nil {
		log.Fatalf("unable to parse file with error %#v", err)
	}

	return nil
}

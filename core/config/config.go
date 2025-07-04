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
}

type SmartWalletConfig struct {
	EthRpcUrl         string
	EthWsUrl          string
	BundlerURL        string
	FactoryAddress    common.Address
	EntrypointAddress common.Address

	ControllerPrivateKey *ecdsa.PrivateKey
	PaymasterAddress     common.Address
	WhitelistAddresses   []common.Address
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

	RpcBindAddress string `yaml:"rpc_bind_address"`

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
		logger.Errorf("Cannot create http ethclient", "err", err)
		return nil, err
	}

	ethWsClient, err := eth.NewInstrumentedClient(configRaw.EthWsUrl, rpcCallsCollector)
	if err != nil {
		logger.Errorf("Cannot create ws ethclient", "err", err)
		return nil, err
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

	chainId, err := ethRpcClient.ChainID(context.Background())
	if err != nil {
		logger.Error("Cannot get chainId", "err", err)
		return nil, err
	}

	signerV2, _, err := signerv2.SignerFromConfig(signerv2.Config{PrivateKey: ecdsaPrivateKey}, chainId)
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
			FactoryAddress:       common.HexToAddress(configRaw.SmartWallet.FactoryAddress),
			EntrypointAddress:    common.HexToAddress(configRaw.SmartWallet.EntrypointAddress),
			ControllerPrivateKey: controllerPrivateKey,
			PaymasterAddress:     common.HexToAddress(configRaw.SmartWallet.PaymasterAddress),
			WhitelistAddresses:   convertToAddressSlice(configRaw.SmartWallet.WhitelistAddresses),
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
	}

	if config.SocketPath == "" {
		config.SocketPath = "/tmp/ap.sock"
	}
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

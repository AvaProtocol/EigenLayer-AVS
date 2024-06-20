package config

import (
	"context"
	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Layr-Labs/eigensdk-go/chainio/clients/eth"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients/wallet"
	"github.com/Layr-Labs/eigensdk-go/chainio/txmgr"
	"github.com/Layr-Labs/eigensdk-go/crypto/bls"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/Layr-Labs/eigensdk-go/signerv2"

	sdkutils "github.com/Layr-Labs/eigensdk-go/utils"
)

// Config contains all of the configuration information for a credible squaring aggregators and challengers.
// Operators use a separate config. (see config-files/operator.anvil.yaml)
type Config struct {
	EcdsaPrivateKey           *ecdsa.PrivateKey `yaml:"ecdsa_private_key"`
	BlsPrivateKey             *bls.PrivateKey   `yaml:"bls_private_key"`
	Logger                    sdklogging.Logger `yaml:"-"`
	EigenMetricsIpPortAddress string

	// we need the url for the eigensdk currently... eventually standardize api so as to
	// only take an ethclient or an rpcUrl (and build the ethclient at each constructor site)
	EthHttpRpcUrl                     string
	EthWsRpcUrl                       string
	EthHttpClient                     eth.Client
	EthWsClient                       eth.Client
	OperatorStateRetrieverAddr        common.Address
	AutomationRegistryCoordinatorAddr common.Address
	RpcBindAddress                    string
	RegisterOperatorOnStartup         bool
	// json:"-" skips this field when marshaling (only used for logging to stdout), since SignerFn doesnt implement marshalJson
	SignerFn          signerv2.SignerFn `json:"-"`
	TxMgr             txmgr.TxManager
	AggregatorAddress common.Address

	DbPath    string
	JwtSecret []byte
}

// These are read from configPath
type ConfigRaw struct {
	EcdsaPrivateKey string              `yaml:"ecdsa_private_key"`
	Environment     sdklogging.LogLevel `yaml:"environment"`
	EthRpcUrl       string              `yaml:"eth_rpc_url"`
	EthWsUrl        string              `yaml:"eth_ws_url"`

	RpcBindAddress string `yaml:"rpc_bind_address"`

	OperatorStateRetrieverAddr string `yaml:"operator_state_retriever_address"`
	AVSRegistryCoordinatorAddr string `yaml:"avs_registry_coordinator_address"`

	DbPath    string `yaml:"db_path"`
	JwtSecret string `yaml:"jwt_secret"`
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
		sdkutils.ReadYamlConfig(configFilePath, &configRaw)
	}

	logger, err := sdklogging.NewZapLogger(configRaw.Environment)
	if err != nil {
		return nil, err
	}

	ethRpcClient, err := eth.NewClient(configRaw.EthRpcUrl)
	if err != nil {
		logger.Errorf("Cannot create http ethclient", "err", err)
		return nil, err
	}

	ethWsClient, err := eth.NewClient(configRaw.EthWsUrl)
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

	config := &Config{
		EcdsaPrivateKey:                   ecdsaPrivateKey,
		Logger:                            logger,
		EthWsRpcUrl:                       configRaw.EthWsUrl,
		EthHttpRpcUrl:                     configRaw.EthRpcUrl,
		EthHttpClient:                     ethRpcClient,
		EthWsClient:                       ethWsClient,
		OperatorStateRetrieverAddr:        common.HexToAddress(configRaw.OperatorStateRetrieverAddr),
		AutomationRegistryCoordinatorAddr: common.HexToAddress(configRaw.AVSRegistryCoordinatorAddr),
		RpcBindAddress:                    configRaw.RpcBindAddress,
		SignerFn:                          signerV2,
		TxMgr:                             txMgr,
		AggregatorAddress:                 aggregatorAddr,

		DbPath:    configRaw.DbPath,
		JwtSecret: []byte(configRaw.JwtSecret),
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

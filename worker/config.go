package worker

import (
	"crypto/ecdsa"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"gopkg.in/yaml.v2"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

type WorkerConfig struct {
	Environment   string `yaml:"environment"`
	ChainID       int64  `yaml:"chain_id"`
	ChainName     string `yaml:"chain_name"`
	ListenAddress string `yaml:"listen_address"`
	HealthAddress string `yaml:"health_address"`

	EthRpcUrl  string `yaml:"eth_rpc_url"`
	EthWsUrl   string `yaml:"eth_ws_url"`
	BundlerURL string `yaml:"bundler_url"`

	SmartWallet SmartWalletRaw `yaml:"smart_wallet"`
}

type SmartWalletRaw struct {
	ControllerPrivateKey string `yaml:"controller_private_key"`
	PaymasterAddress     string `yaml:"paymaster_address"`
	FactoryAddress       string `yaml:"factory_address"`
	EntrypointAddress    string `yaml:"entrypoint_address"`
}

func NewWorkerConfig(configPath string) (*WorkerConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg WorkerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.ChainID == 0 {
		return nil, fmt.Errorf("chain_id is required")
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "0.0.0.0:50051"
	}
	if cfg.HealthAddress == "" {
		cfg.HealthAddress = "0.0.0.0:8090"
	}

	return &cfg, nil
}

// ToSmartWalletConfig converts the worker's raw config into the shared SmartWalletConfig
// used by existing execution code (preset.SendUserOp, aa.GetNonce, etc.)
func (c *WorkerConfig) ToSmartWalletConfig() (*config.SmartWalletConfig, error) {
	var controllerKey *ecdsa.PrivateKey
	var controllerAddr common.Address

	if c.SmartWallet.ControllerPrivateKey != "" {
		key, err := crypto.HexToECDSA(c.SmartWallet.ControllerPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("invalid controller private key: %w", err)
		}
		controllerKey = key
		controllerAddr = crypto.PubkeyToAddress(key.PublicKey)
	}

	factoryAddr := common.HexToAddress(config.DefaultFactoryProxyAddressHex)
	if c.SmartWallet.FactoryAddress != "" {
		factoryAddr = common.HexToAddress(c.SmartWallet.FactoryAddress)
	}

	entrypointAddr := common.HexToAddress(config.DefaultEntrypointAddressHex)
	if c.SmartWallet.EntrypointAddress != "" {
		entrypointAddr = common.HexToAddress(c.SmartWallet.EntrypointAddress)
	}

	paymasterAddr := common.HexToAddress(config.DefaultPaymasterAddressHex)
	if c.SmartWallet.PaymasterAddress != "" {
		paymasterAddr = common.HexToAddress(c.SmartWallet.PaymasterAddress)
	}

	return &config.SmartWalletConfig{
		EthRpcUrl:             c.EthRpcUrl,
		EthWsUrl:              c.EthWsUrl,
		BundlerURL:            c.BundlerURL,
		FactoryAddress:        factoryAddr,
		EntrypointAddress:     entrypointAddr,
		ChainID:               c.ChainID,
		ControllerPrivateKey:  controllerKey,
		ControllerAddress:     controllerAddr,
		PaymasterAddress:      paymasterAddr,
		PaymasterOwnerAddress: controllerAddr,
	}, nil
}

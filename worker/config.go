package worker

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

type WorkerConfig struct {
	Environment   string `yaml:"environment"`
	ChainID       int64  `yaml:"chain_id"`
	ChainName     string `yaml:"chain_name"`
	ListenAddress string `yaml:"listen_address"`
	HealthAddress string `yaml:"health_address"`

	EthRpcUrl       string `yaml:"eth_rpc_url"`
	EthWsUrl        string `yaml:"eth_ws_url"`
	BundlerURL      string `yaml:"bundler_url"`
	BundlerProvider string `yaml:"bundler_provider"`
	AlchemyAPIKey   string `yaml:"alchemy_api_key"`

	// Legacy shared configuration, still parsed for existing deployments.
	// Workers no longer send UserOps or request sponsorship (#762). Configure
	// the policy and webhook secret on the gateway, which owns session grants.
	AlchemyPaymasterPolicyID string `yaml:"alchemy_paymaster_policy_id"`
	// DisableGasSponsorship is retained with the legacy sponsorship fields.
	DisableGasSponsorship bool `yaml:"disable_gas_sponsorship"`
	// GasManagerPolicyID is the legacy alias, accepted for the same reason the
	// gateway accepts it: an existing deployment must not silently lose
	// sponsorship on rename.
	GasManagerPolicyID      string `yaml:"gas_manager_policy_id"`
	GasManagerWebhookSecret string `yaml:"gas_manager_webhook_secret"`

	SmartWallet SmartWalletRaw `yaml:"smart_wallet"`
}

type SmartWalletRaw struct {
	ControllerPrivateKey string `yaml:"controller_private_key"`
	PaymasterAddress     string `yaml:"paymaster_address"`
	FactoryAddress       string `yaml:"factory_address"`
	EntrypointAddress    string `yaml:"entrypoint_address"`
}

func NewWorkerConfig(configPath string) (*WorkerConfig, error) {
	var cfg WorkerConfig
	if err := config.ReadYamlConfig(configPath, &cfg); err != nil {
		return nil, fmt.Errorf("loading worker config: %w", err)
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

	// paymaster_address must be specified explicitly per chain — see the
	// note on the removed config.DefaultPaymasterAddressHex. An empty
	// address means "no paymaster"; the smart wallet pays its own gas.
	paymasterAddr := common.HexToAddress(c.SmartWallet.PaymasterAddress)

	return &config.SmartWalletConfig{
		AlchemyPaymasterPolicyID: config.ResolveAlchemyPaymasterPolicyID(
			c.AlchemyPaymasterPolicyID, c.GasManagerPolicyID),
		DisableGasSponsorship:   c.DisableGasSponsorship,
		GasManagerWebhookSecret: config.ResolveGasManagerWebhookSecret(c.GasManagerWebhookSecret),

		EthRpcUrl:             c.EthRpcUrl,
		EthWsUrl:              c.EthWsUrl,
		BundlerURL:            c.BundlerURL,
		BundlerProvider:       c.BundlerProvider,
		AlchemyAPIKey:         c.AlchemyAPIKey,
		FactoryAddress:        factoryAddr,
		EntrypointAddress:     entrypointAddr,
		ChainID:               c.ChainID,
		ControllerPrivateKey:  controllerKey,
		ControllerAddress:     controllerAddr,
		PaymasterAddress:      paymasterAddr,
		PaymasterOwnerAddress: controllerAddr,
	}, nil
}

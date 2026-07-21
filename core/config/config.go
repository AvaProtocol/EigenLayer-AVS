package config

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v2"

	"github.com/Layr-Labs/eigensdk-go/chainio/clients/eth"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients/wallet"
	"github.com/Layr-Labs/eigensdk-go/chainio/txmgr"
	"github.com/Layr-Labs/eigensdk-go/crypto/bls"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	rpccalls "github.com/Layr-Labs/eigensdk-go/metrics/collectors/rpc_calls"
	"github.com/Layr-Labs/eigensdk-go/signerv2"
	"go.uber.org/zap"

	sdkutils "github.com/Layr-Labs/eigensdk-go/utils"

	pkglogger "github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
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

// NOTE: there is no DefaultPaymasterAddressHex. The paymaster contract
// is deployed per network (mainnet and testnet have different addresses),
// so silently defaulting any chain's paymaster to a single hardcoded
// value silently misroutes that chain's UserOps. The YAML must specify
// paymaster_address explicitly for every chain that wants sponsored
// transactions; chains that omit it run without a paymaster and rely on
// the smart wallet's own balance for gas.

// Config contains all of the configuration information for a credible squaring aggregators and challengers.
// Operators use a separate config. (see config-files/operator.anvil.yaml)
type Config struct {
	EcdsaPrivateKey           *ecdsa.PrivateKey
	BlsPrivateKey             *bls.PrivateKey
	Logger                    sdklogging.Logger
	EigenMetricsIpPortAddress string
	SentryDsn                 string
	// SentryEnvironment is the value reported as the `environment` tag on
	// every Sentry event. Defaults to "production" when unset so existing
	// deployments keep their current bucket. Feature-branch or staging
	// deployments should set this to "staging" (or "feature:<name>") to
	// keep their events out of the production issue board and to let
	// Sentry alerts gate on environment.
	SentryEnvironment string
	ServerName        string

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

	// REST rate limit overrides. Nil/zero = use the middleware
	// default (10 req/s, burst 50). Dev configs typically bump
	// these so an end-to-end test suite doesn't trip the bucket.
	RestRateLimitPerSecond float64
	RestRateLimitBurst     int

	// Partners are the registered delegated tenants permitted to call the
	// simulate family on behalf of their own end users. See PartnerConfig.
	Partners []PartnerConfig

	// PartnerAssertionAudience binds partner assertions to this gateway;
	// see the ConfigRaw field of the same name.
	PartnerAssertionAudience string

	// Account abstraction config
	SmartWallet *SmartWalletConfig

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

	// Moralis Web3 Data API key for token price lookup (optional)
	MoralisApiKey string `yaml:"moralis_api_key"`

	// Fee rates configuration for task execution pricing
	FeeRates *FeeRatesConfig

	// NotificationsSummary contains optional AI summarization settings for notifications
	NotificationsSummary NotificationsSummaryConfig

	// Gateway mode fields — set when chains[] is present in config.
	// When IsGateway is true, the aggregator acts as a multi-chain gateway
	// that routes chain-specific operations to per-chain workers via gRPC.
	IsGateway      bool
	DefaultChainID int64
	Chains         []*ChainConfig
}

// ChainConfig holds per-chain configuration for gateway mode.
// Each chain has a worker address for gRPC delegation and its own SmartWalletConfig.
type ChainConfig struct {
	ChainID     int64
	Name        string
	WorkerAddr  string
	SmartWallet *SmartWalletConfig
}

// FeeRatesConfig defines the fee structure for workflow execution.
// Three components: execution_fee (flat) + COGS (per-node) + value_fee (workflow-level tier %).
type FeeRatesConfig struct {
	// Flat per-execution platform fee (charged every run)
	ExecutionFeeUSD float64 // Default: $0.02

	// Value-capture tier percentages (% of tx value, workflow-level)
	Tier1FeePercentage float64 // Default: 0.03%
	Tier2FeePercentage float64 // Default: 0.09%
	Tier3FeePercentage float64 // Default: 0.18%

	// Maximum outstanding value fee balance (USD) before blocking execution.
	// Default 0 = block as soon as any value fee is outstanding (zero tolerance).
	CreditLimitUSD float64
}

// NotificationsSummaryConfig defines optional AI summarization settings for notifications.
// Only the "context-memory" provider is supported.
type NotificationsSummaryConfig struct {
	// Enabled determines whether AI summarization is active for notifications.
	// Set to true to enable summarization, false to disable.
	Enabled bool
	// Provider specifies the AI service to use for summarization.
	// Only "context-memory" is supported.
	Provider string
	// APIEndpoint is the URL of the context-memory API service.
	APIEndpoint string
	// APIKey is the authentication token for the context-memory API.
	APIKey string
}

type SmartWalletConfig struct {
	EthRpcUrl string
	EthWsUrl  string
	// BundlerURL is the self-hosted (Voltaire) bundler endpoint. Used only when
	// BundlerProvider is "self_hosted".
	BundlerURL string
	// BundlerProvider selects the bundler endpoint. "alchemy" (the default when
	// empty) derives the URL from AlchemyAPIKey + the chain's Alchemy subdomain;
	// "self_hosted" uses BundlerURL (our Voltaire bundler). The alchemy path never
	// falls back to BundlerURL — a missing key or unmapped chain is a hard error.
	BundlerProvider string
	// AlchemyAPIKey is the Alchemy app key for the alchemy provider. The bundler
	// URL is derived as https://<subdomain>.g.alchemy.com/v2/<AlchemyAPIKey>.
	AlchemyAPIKey     string
	FactoryAddress    common.Address
	EntrypointAddress common.Address
	// ChainID of the connected network (derived at runtime from RPC)
	ChainID int64

	ControllerPrivateKey  *ecdsa.PrivateKey
	ControllerAddress     common.Address // Derived from ControllerPrivateKey (for signature verification)
	PaymasterAddress      common.Address
	PaymasterOwnerAddress common.Address // Owner of the paymaster contract (for gas reimbursement)
	WhitelistAddresses    []common.Address

	// Maximum number of smart wallets allowed per EOA owner
	// Unlimited is NOT supported. If zero or negative, the default limit is applied.
	MaxWalletsPerOwner int
}

// Bundler provider identifiers for SmartWalletConfig.BundlerProvider.
const (
	BundlerProviderAlchemy    = "alchemy"
	BundlerProviderSelfHosted = "self_hosted"
)

// alchemyNetworkSubdomain maps a chain ID to its Alchemy JSON-RPC/bundler
// network subdomain (https://<subdomain>.g.alchemy.com/v2/<key>). Extend this
// when onboarding a new chain to the Alchemy bundler path — e.g. Unichain
// ("unichain-mainnet") or Robinhood Chain ("robinhood-mainnet").
var alchemyNetworkSubdomain = map[int64]string{
	1:        "eth-mainnet",
	11155111: "eth-sepolia",
	8453:     "base-mainnet",
	84532:    "base-sepolia",
	56:       "bnb-mainnet",
}

// ProviderName returns the effective bundler provider, defaulting to alchemy
// when BundlerProvider is empty. Safe to log (no secret).
func (c *SmartWalletConfig) ProviderName() string {
	p := strings.ToLower(strings.TrimSpace(c.BundlerProvider))
	if p == "" {
		return BundlerProviderAlchemy
	}
	return p
}

// ActiveBundlerURL returns the bundler endpoint for the configured provider.
// The provider defaults to alchemy. The alchemy path derives its URL from
// AlchemyAPIKey + the chain's Alchemy subdomain and NEVER falls back to
// BundlerURL; a missing key or an unmapped chain is a hard error (fail closed).
// The self_hosted path uses BundlerURL (our Voltaire bundler).
func (c *SmartWalletConfig) ActiveBundlerURL() (string, error) {
	switch c.ProviderName() {
	case BundlerProviderSelfHosted, "voltaire":
		if c.BundlerURL == "" {
			return "", fmt.Errorf("bundler_provider=self_hosted but bundler_url is empty (chain_id=%d)", c.ChainID)
		}
		return c.BundlerURL, nil
	case BundlerProviderAlchemy:
		if c.AlchemyAPIKey == "" {
			return "", fmt.Errorf("bundler_provider=alchemy but alchemy_api_key is empty (chain_id=%d); set alchemy_api_key or bundler_provider: self_hosted", c.ChainID)
		}
		subdomain, ok := alchemyNetworkSubdomain[c.ChainID]
		if !ok {
			return "", fmt.Errorf("bundler_provider=alchemy but chain_id=%d has no known Alchemy network subdomain; add it to alchemyNetworkSubdomain or set bundler_provider: self_hosted", c.ChainID)
		}
		return fmt.Sprintf("https://%s.g.alchemy.com/v2/%s", subdomain, c.AlchemyAPIKey), nil
	default:
		return "", fmt.Errorf("unknown bundler_provider %q (chain_id=%d); expected %q or %q", c.BundlerProvider, c.ChainID, BundlerProviderAlchemy, BundlerProviderSelfHosted)
	}
}

// BundlerConfigured reports whether a bundler endpoint resolves for this chain.
// Distinguishes wallet-op chains from connectivity-only rollouts (where no
// bundler is configured for either provider).
func (c *SmartWalletConfig) BundlerConfigured() bool {
	_, err := c.ActiveBundlerURL()
	return err == nil
}

type BackupConfig struct {
	Enabled         bool   // Whether periodic backups are enabled
	IntervalMinutes int    // Interval between backups in minutes
	BackupDir       string // Directory to store backups
}

// SmartWalletConfigRaw represents the raw YAML config for smart wallet operations.
// Used both in the top-level aggregator config and in per-chain gateway configs.
type SmartWalletConfigRaw struct {
	EthRpcUrl            string   `yaml:"eth_rpc_url"`
	EthWsUrl             string   `yaml:"eth_ws_url"`
	BundlerURL           string   `yaml:"bundler_url"`
	BundlerProvider      string   `yaml:"bundler_provider"`
	AlchemyAPIKey        string   `yaml:"alchemy_api_key"`
	FactoryAddress       string   `yaml:"factory_address"`
	EntrypointAddress    string   `yaml:"entrypoint_address"`
	ControllerPrivateKey string   `yaml:"controller_private_key"`
	PaymasterAddress     string   `yaml:"paymaster_address"`
	WhitelistAddresses   []string `yaml:"whitelist_addresses"`
	MaxWalletsPerOwner   int      `yaml:"max_wallets_per_owner"`
}

// ChainConfigRaw represents a per-chain entry in the gateway's chains[] config.
type ChainConfigRaw struct {
	ChainID     int64                `yaml:"chain_id"`
	Name        string               `yaml:"name"`
	WorkerAddr  string               `yaml:"worker_addr"`
	SmartWallet SmartWalletConfigRaw `yaml:"smart_wallet"`
}

// PartnerConfig is a registered partner (tenant) permitted to call
// delegated, no-fund operations — currently the simulate family
// (workflows:simulate / nodes:run / triggers:run) — on behalf of its own
// authenticated end users, without those users producing a wallet
// signature. Partners authenticate with a short-lived Ed25519-signed
// assertion (private_key_jwt style) whose `iss` claim equals ID and whose
// signature verifies against one of PublicKeys.
//
// Simulate moves no funds and skips wallet-ownership, so partner trust is
// sufficient for it; fund-moving operations (createTask/execute) are never
// authorized by a partner assertion alone. See PLAN_PARTNER_PAYMENTS.md.
type PartnerConfig struct {
	// ID is the partner identifier; it must equal the `iss` claim of the
	// partner's assertions (e.g. "studio").
	ID string `yaml:"id"`
	// PublicKeys are base64-encoded Ed25519 public keys (optionally
	// "ed25519:"-prefixed). Multiple entries allow key rotation — any
	// listed key may verify an assertion.
	PublicKeys []string `yaml:"public_keys"`
	// Scopes are the delegation scopes granted to this partner, e.g.
	// ["simulate"]. A request is allowed only if its required scope is
	// present here.
	Scopes []string `yaml:"scopes"`
	// Status gates the partner; only "active" partners are honored.
	Status string `yaml:"status"`
}

// These are read from configPath
type ConfigRaw struct {
	EcdsaPrivateKey   string              `yaml:"ecdsa_private_key"`
	Environment       sdklogging.LogLevel `yaml:"environment"`
	EthRpcUrl         string              `yaml:"eth_rpc_url"`
	EthWsUrl          string              `yaml:"eth_ws_url"`
	SentryDsn         string              `yaml:"sentry_dsn,omitempty"`
	SentryEnvironment string              `yaml:"sentry_environment,omitempty"`
	ServerName        string              `yaml:"server_name,omitempty"`

	RpcBindAddress  string `yaml:"rpc_bind_address"`
	HttpBindAddress string `yaml:"http_bind_address"`

	OperatorStateRetrieverAddr string `yaml:"operator_state_retriever_address"`
	AVSRegistryCoordinatorAddr string `yaml:"avs_registry_coordinator_address"`

	DbPath    string `yaml:"db_path"`
	JwtSecret string `yaml:"jwt_secret"`

	// REST rate-limit overrides; see Config.RestRateLimitPerSecond.
	RestRateLimitPerSecond float64 `yaml:"rest_rate_limit_per_second"`
	RestRateLimitBurst     int     `yaml:"rest_rate_limit_burst"`

	SmartWallet SmartWalletConfigRaw `yaml:"smart_wallet"`

	SocketPath string `yaml:"socket_path"`

	// List of approved operator addresses that can process tasks
	ApprovedOperators []string `yaml:"approved_operators"`

	Macros map[string]map[string]string `yaml:"macros"`

	// Tenderly HTTP Simulation API credentials (optional)
	TenderlyAccount   string `yaml:"tenderly_account"`
	TenderlyProject   string `yaml:"tenderly_project"`
	TenderlyAccessKey string `yaml:"tenderly_access_key"`

	// Moralis Web3 Data API key for token price lookup (optional)
	MoralisApiKey string `yaml:"moralis_api_key"`

	// Fee structure: execution_fee + COGS + value tiers
	// Pointer fields: nil = use default, explicit 0.0 = free tier
	FeeRates struct {
		ExecutionFeeUSD *float64 `yaml:"execution_fee_usd"` // $0.02 default
		CreditLimitUSD  *float64 `yaml:"credit_limit_usd"`  // $20 default
		Tiers           struct {
			Tier1 *float64 `yaml:"tier_1"` // 0.03% default
			Tier2 *float64 `yaml:"tier_2"` // 0.09% default
			Tier3 *float64 `yaml:"tier_3"` // 0.18% default
		} `yaml:"tiers"`
	} `yaml:"fee_rates"`

	// Notifications configuration block
	Notifications struct {
		Summary struct {
			Enabled     bool   `yaml:"enabled"`
			Provider    string `yaml:"provider"`
			APIEndpoint string `yaml:"api_endpoint"`
			APIKey      string `yaml:"api_key"`
		} `yaml:"summary"`
	} `yaml:"notifications"`

	// Gateway mode: per-chain worker configs. When present, aggregator runs as gateway.
	Chains []ChainConfigRaw `yaml:"chains"`

	// Partners registers delegated tenants permitted to call the simulate
	// family on behalf of their own users. See PartnerConfig.
	Partners []PartnerConfig `yaml:"partners"`

	// PartnerAssertionAudience, when set, is the value every partner
	// assertion's `aud` claim must contain — bind it to this gateway /
	// environment (e.g. "avs-gateway-staging") so a captured assertion
	// cannot be replayed against another environment. Empty disables the
	// check (not recommended in production).
	PartnerAssertionAudience string `yaml:"partner_assertion_audience"`
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

	logger, err := newLogger(configRaw.Environment, "aggregator")
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

	// Derive eth_ws_url from eth_rpc_url when not explicitly set. Both
	// endpoints typically live at the same host+path on every supported
	// provider (Dwellir, Tenderly, Alchemy, …) — only the scheme
	// differs. Letting one URL drive both halves the number of env
	// vars an operator has to keep in sync and avoids the easy mistake
	// of rotating one without the other. An explicit eth_ws_url still
	// wins when set, for the rare case where the WS endpoint really
	// is a separate URL.
	if configRaw.EthWsUrl == "" {
		configRaw.EthWsUrl = DeriveWsURL(configRaw.EthRpcUrl)
	}
	if configRaw.SmartWallet.EthWsUrl == "" {
		configRaw.SmartWallet.EthWsUrl = DeriveWsURL(configRaw.SmartWallet.EthRpcUrl)
	}

	// Only create WebSocket client if URL is provided
	var ethWsClient *eth.InstrumentedClient
	if configRaw.EthWsUrl != "" {
		ethWsClient, err = eth.NewInstrumentedClient(configRaw.EthWsUrl, rpcCallsCollector)
		if err != nil {
			// In CI environment or when URL is empty, gracefully handle WebSocket connection failures
			if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
				logger.Warnf("WebSocket client connection failed in CI environment, setting to nil: %v", err)
				ethWsClient = nil
			} else {
				// For local development, log warning but continue without WebSocket
				logger.Warnf("Cannot create ws ethclient (will continue without WebSocket support)", "err", err)
				ethWsClient = nil
			}
		}
	} else {
		logger.Info("No WebSocket URL configured, WebSocket client will be nil")
		ethWsClient = nil
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

	// Validate that the top-level smart_wallet is configured. This block is
	// shared by the aa package globals (factory + entrypoint addresses), the
	// startup paymaster probe, and any code path that explicitly takes the
	// aggregator's default chain context.
	//
	// In gateway mode there is no implicit fallback to chains[0] — the
	// operator picks which chain the top-level represents and sets it
	// explicitly. The previous "use first chain" magic silently routed every
	// top-level operation to mainnet (by chains[] convention) which made the
	// startup probe + cmd tools talk to the wrong chain by default.
	isGateway := len(configRaw.Chains) > 0
	if configRaw.SmartWallet.EthRpcUrl == "" {
		if isGateway {
			return nil, fmt.Errorf(
				"smart_wallet.eth_rpc_url is required at the top level even in gateway mode " +
					"(used by the aa package, startup probes, and cmd tools). Set an explicit " +
					"smart_wallet block — typically matching the chain you want as the aggregator's " +
					"default context (e.g. the AVS chain)")
		}
		return nil, fmt.Errorf(
			"smart_wallet.eth_rpc_url is required: this RPC URL is critical for aggregator " +
				"contract operations; without it, the aggregator cannot interact with the blockchain")
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
		// Name the field + the likely cause. The bare crypto error
		// ("invalid length, need 256 bits") gives no hint which key is at
		// fault. In practice this is almost always a missing/empty env var or
		// an unresolved ${{shared.*}} Railway reference, which decodes to the
		// wrong length. (Production incident: gateway crash-loop after a
		// per-tier controller key var was unset on the service.)
		panic(fmt.Errorf("smart_wallet.controller_private_key is not a valid 64-hex-char "+
			"private key — check the controller key env var for this service; an empty value or an "+
			"unresolved ${...}/${{shared.*}} reference produces this: %w", err))
	}

	// Enforce sane default for max wallets per owner (no unlimited allowed)
	if configRaw.SmartWallet.MaxWalletsPerOwner <= 0 {
		configRaw.SmartWallet.MaxWalletsPerOwner = DefaultMaxWalletsPerOwner
	}
	// Enforce hard upper bound to prevent abuse
	if configRaw.SmartWallet.MaxWalletsPerOwner > HardMaxWalletsPerOwner {
		configRaw.SmartWallet.MaxWalletsPerOwner = HardMaxWalletsPerOwner
	}

	config := &Config{
		EcdsaPrivateKey:   ecdsaPrivateKey,
		Logger:            logger,
		SentryDsn:         configRaw.SentryDsn,
		SentryEnvironment: configRaw.SentryEnvironment,
		ServerName:        configRaw.ServerName,
		EthWsRpcUrl:       configRaw.EthWsUrl,
		EthHttpRpcUrl:     configRaw.EthRpcUrl,
		EthHttpClient:     ethRpcClient,
		EthWsClient:       ethWsClient,

		Environment:                       configRaw.Environment,
		OperatorStateRetrieverAddr:        common.HexToAddress(configRaw.OperatorStateRetrieverAddr),
		AutomationRegistryCoordinatorAddr: common.HexToAddress(configRaw.AVSRegistryCoordinatorAddr),
		RpcBindAddress:                    configRaw.RpcBindAddress,
		HttpBindAddress:                   configRaw.HttpBindAddress,
		SignerFn:                          signerV2,
		TxMgr:                             txMgr,
		AggregatorAddress:                 aggregatorAddr,

		DbPath:    configRaw.DbPath,
		BackupDir: configRaw.DbPath + "_backup",
		JwtSecret: []byte(configRaw.JwtSecret),

		RestRateLimitPerSecond: configRaw.RestRateLimitPerSecond,
		RestRateLimitBurst:     configRaw.RestRateLimitBurst,

		Partners:                 configRaw.Partners,
		PartnerAssertionAudience: configRaw.PartnerAssertionAudience,

		SmartWallet: &SmartWalletConfig{
			EthRpcUrl:            configRaw.SmartWallet.EthRpcUrl,
			EthWsUrl:             configRaw.SmartWallet.EthWsUrl,
			BundlerURL:           configRaw.SmartWallet.BundlerURL,
			BundlerProvider:      configRaw.SmartWallet.BundlerProvider,
			AlchemyAPIKey:        configRaw.SmartWallet.AlchemyAPIKey,
			FactoryAddress:       common.HexToAddress(firstNonEmpty(configRaw.SmartWallet.FactoryAddress, DefaultFactoryProxyAddressHex)),
			EntrypointAddress:    common.HexToAddress(firstNonEmpty(configRaw.SmartWallet.EntrypointAddress, DefaultEntrypointAddressHex)),
			ChainID:              smartWalletChainId.Int64(), // Use smart wallet chain ID, not EigenLayer chain ID (prevents cross-chain configuration errors for Base aggregator)
			ControllerPrivateKey: controllerPrivateKey,
			PaymasterAddress:     common.HexToAddress(configRaw.SmartWallet.PaymasterAddress),
			WhitelistAddresses:   convertToAddressSlice(configRaw.SmartWallet.WhitelistAddresses),
			MaxWalletsPerOwner:   configRaw.SmartWallet.MaxWalletsPerOwner,
			// PaymasterOwnerAddress will be populated below by calling owner() on the paymaster contract
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

		// Pass through Moralis API key (from YAML or environment variable)
		MoralisApiKey: firstNonEmpty(configRaw.MoralisApiKey, os.Getenv("MORALIS_API_KEY")),

		// Initialize fee rates - use defaults if no YAML config provided
		FeeRates: loadFeeRatesFromConfig(configRaw.FeeRates),

		// Initialize notifications summary configuration (optional)
		NotificationsSummary: NotificationsSummaryConfig{
			Enabled:     configRaw.Notifications.Summary.Enabled,
			Provider:    configRaw.Notifications.Summary.Provider,
			APIEndpoint: configRaw.Notifications.Summary.APIEndpoint,
			APIKey:      configRaw.Notifications.Summary.APIKey,
		},
	}

	if config.SocketPath == "" {
		config.SocketPath = "/tmp/ap.sock"
	}

	// Derive the controller address from the controller private key
	// This is used for signature verification throughout the system
	if config.SmartWallet != nil && config.SmartWallet.ControllerPrivateKey != nil {
		config.SmartWallet.ControllerAddress = crypto.PubkeyToAddress(config.SmartWallet.ControllerPrivateKey.PublicKey)
		logger.Info("Controller address derived", "controller", config.SmartWallet.ControllerAddress.Hex())
	}

	// Fetch the paymaster owner address by calling owner() on the paymaster contract.
	// This is needed for gas reimbursement — we send ETH to the owner (EOA), not the
	// contract itself.
	//
	// The probe doubles as a sanity check on the (RPC, paymaster_address) pairing:
	// if the address has no contract code on the connected RPC, owner() returns an
	// empty result and unpacking fails. Treat that as a fatal startup error rather
	// than a warning — previously this was a logger.Warn that let the aggregator
	// boot with a mismatched config, and every UserOp downstream failed at
	// "no contract code at given address" (Sentry EIGENLAYER-AVS-1N/1M, user-reported
	// failure 2026-05-30 01:55 UTC on Sepolia). Fail-fast surfaces the same problem
	// at startup where it's diagnosable, not hours later on a real workflow.
	if config.SmartWallet != nil && config.SmartWallet.PaymasterAddress != (common.Address{}) {
		paymasterOwner, err := fetchPaymasterOwner(smartWalletRpcClient, config.SmartWallet.PaymasterAddress)
		if err != nil {
			return nil, fmt.Errorf(
				"paymaster %s is unreachable on RPC %s (owner() call failed: %w) — "+
					"verify the paymaster address is deployed on this chain, and that the "+
					"smart_wallet.eth_rpc_url points at the chain where the paymaster lives",
				config.SmartWallet.PaymasterAddress.Hex(),
				configRaw.SmartWallet.EthRpcUrl,
				err,
			)
		}
		config.SmartWallet.PaymasterOwnerAddress = paymasterOwner
		logger.Info("Paymaster owner address loaded", "paymaster", config.SmartWallet.PaymasterAddress, "owner", paymasterOwner.Hex())

		// The aggregator signs the paymaster hash with the controller key, so the
		// paymaster's verifyingSigner must equal the controller address or every
		// sponsored UserOp fails at signing time. Probe it at startup (alongside
		// owner()) so a key/paymaster-tier mismatch is fatal here, not silent until
		// the first user transfer (Sentry EIGENLAYER-AVS-1W).
		verifyingSigner, err := fetchPaymasterVerifyingSigner(smartWalletRpcClient, config.SmartWallet.PaymasterAddress)
		if err != nil {
			return nil, fmt.Errorf(
				"paymaster %s verifyingSigner() call failed on RPC %s: %w",
				config.SmartWallet.PaymasterAddress.Hex(), configRaw.SmartWallet.EthRpcUrl, err,
			)
		}
		if verifyingSigner != config.SmartWallet.ControllerAddress {
			return nil, fmt.Errorf(
				"paymaster %s verifyingSigner (%s) does not match controller address (%s) — "+
					"the controller_private_key and paymaster_address belong to different "+
					"network tiers (e.g. testnet controller paired with the mainnet paymaster); "+
					"set the controller key whose address equals the paymaster's verifyingSigner",
				config.SmartWallet.PaymasterAddress.Hex(),
				verifyingSigner.Hex(), config.SmartWallet.ControllerAddress.Hex(),
			)
		}
		logger.Info("Paymaster verifyingSigner verified", "paymaster", config.SmartWallet.PaymasterAddress, "verifyingSigner", verifyingSigner.Hex())
	}

	// Gateway mode: parse per-chain configs
	if isGateway {
		config.IsGateway = true
		config.DefaultChainID = configRaw.Chains[0].ChainID

		for _, chainRaw := range configRaw.Chains {
			chainCfg, err := parseChainConfig(chainRaw, logger)
			if err != nil {
				return nil, fmt.Errorf("parsing chain config for %s (chain_id=%d): %w",
					chainRaw.Name, chainRaw.ChainID, err)
			}
			config.Chains = append(config.Chains, chainCfg)
		}

		logger.Info("Gateway mode enabled",
			"num_chains", len(config.Chains),
			"default_chain_id", config.DefaultChainID)
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

// newLogger creates a zap logger that only prints stack traces at ERROR level,
// wrapped with SentryLogger to forward Error/Fatal calls to Sentry.
// By default, zap's development config prints stack traces at WARN, which is noisy
// for transient errors like RPC 500s. We override to ERROR-only.
// AddCallerSkip(2) accounts for both the sdklogging wrapper and the SentryLogger wrapper.
func newLogger(env sdklogging.LogLevel, serviceName string) (sdklogging.Logger, error) {
	var cfg zap.Config
	if env == sdklogging.Production {
		cfg = zap.NewProductionConfig()
	} else {
		cfg = zap.NewDevelopmentConfig()
	}
	cfg.DisableStacktrace = true
	zapLogger, err := sdklogging.NewZapLoggerByConfig(cfg, zap.AddCallerSkip(2), zap.AddStacktrace(zap.ErrorLevel))
	if err != nil {
		return nil, err
	}
	return pkglogger.NewSentryLogger(zapLogger, serviceName), nil
}

// DeriveWsURL turns an HTTP(S) RPC URL into the equivalent WebSocket URL
// by flipping the scheme. Returns "" when given "" (so callers can use
// it unconditionally — empty in, empty out, fall back to the existing
// "no WebSocket" code path).
//
// Every RPC provider we ship configs for (Dwellir, Tenderly, Alchemy,
// Infura, mainnet.base.org, etc.) serves HTTP and WebSocket from the
// same host + path with only the scheme changing. So:
//
//	https://api-ethereum-mainnet.n.dwellir.com/<key>
//	wss://api-ethereum-mainnet.n.dwellir.com/<key>
//
// are the same endpoint via different transports. Letting one URL drive
// both halves the env-var count an operator manages, and makes it
// impossible to rotate one without the other.
//
// If a provider ever splits the two (different host or different path
// for WS), set eth_ws_url explicitly — that value still wins.
func DeriveWsURL(rpcURL string) string {
	switch {
	case rpcURL == "":
		return ""
	case strings.HasPrefix(rpcURL, "https://"):
		return "wss://" + strings.TrimPrefix(rpcURL, "https://")
	case strings.HasPrefix(rpcURL, "http://"):
		return "ws://" + strings.TrimPrefix(rpcURL, "http://")
	default:
		// Not an http(s) URL — return as-is and let the WS client
		// surface a clear "scheme not supported" error rather than
		// silently mangling something we don't recognize.
		return rpcURL
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
		return fmt.Errorf("path %s does not exist", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Expand ${VAR} and $VAR references against the process environment
	// before parsing. This lets us commit configs with secret placeholders
	// (e.g. `controller_private_key: ${CONTROLLER_PRIVATE_KEY}`) and inject
	// the real values via Railway sealed env vars at runtime — keeping
	// production credentials out of the git repo.
	//
	// TrimSpace defensively against a class of bug we hit in production:
	// Railway env vars sometimes carry a trailing newline (when pasted
	// into the dashboard UI), which turns "https://rpc.example/key" into
	// "https://rpc.example/key\n" — malformed URL, opaque DNS lookup
	// failures. No env var consumed by these configs has meaningful
	// leading/trailing whitespace, so trimming is safe.
	expanded := os.Expand(string(b), func(key string) string {
		return strings.TrimSpace(os.Getenv(key))
	})

	if err := yaml.Unmarshal([]byte(expanded), o); err != nil {
		// A common cause is an env var that resolved to a value containing
		// YAML-special characters — e.g. an unresolved ${{shared.*}} Railway
		// reference left as literal "${{...}}" text after expansion.
		return fmt.Errorf("unable to parse YAML config %q (a substituted env var may "+
			"contain an unresolved ${{...}} reference or YAML-special characters): %w", path, err)
	}

	return nil
}

// loadFeeRatesFromConfig loads fee config from YAML with fallback to defaults.
// Pointer fields distinguish "missing" (nil → use default) from explicit zero (0.0 → free tier).
func loadFeeRatesFromConfig(configRates struct {
	ExecutionFeeUSD *float64 `yaml:"execution_fee_usd"`
	CreditLimitUSD  *float64 `yaml:"credit_limit_usd"`
	Tiers           struct {
		Tier1 *float64 `yaml:"tier_1"`
		Tier2 *float64 `yaml:"tier_2"`
		Tier3 *float64 `yaml:"tier_3"`
	} `yaml:"tiers"`
}) *FeeRatesConfig {
	getFloat64 := func(configValue *float64, hardcodedDefault float64) float64 {
		if configValue != nil {
			return *configValue
		}
		return hardcodedDefault
	}

	return &FeeRatesConfig{
		ExecutionFeeUSD:    getFloat64(configRates.ExecutionFeeUSD, 0.02),
		Tier1FeePercentage: getFloat64(configRates.Tiers.Tier1, 0.03),
		Tier2FeePercentage: getFloat64(configRates.Tiers.Tier2, 0.09),
		Tier3FeePercentage: getFloat64(configRates.Tiers.Tier3, 0.18),
		CreditLimitUSD:     getFloat64(configRates.CreditLimitUSD, 0.0),
	}
}

// GetDefaultFeeRatesConfig returns the default fee rates
func GetDefaultFeeRatesConfig() *FeeRatesConfig {
	return &FeeRatesConfig{
		ExecutionFeeUSD:    0.02,
		Tier1FeePercentage: 0.03,
		Tier2FeePercentage: 0.09,
		Tier3FeePercentage: 0.18,
		CreditLimitUSD:     0.0, // Zero = block as soon as any value fee is outstanding
	}
}

// paymasterCaller is the minimal contract-call surface needed to probe a
// paymaster's no-arg address getters (owner() and verifyingSigner()). Both
// *eth.InstrumentedClient (top-level smart wallet) and *ethclient.Client
// (per-chain dials) satisfy it.
type paymasterCaller interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// paymasterProbeTimeout bounds each startup paymaster RPC call so an
// unresponsive endpoint fails the boot fast instead of hanging indefinitely —
// which is the whole point of the startup probe.
const paymasterProbeTimeout = 15 * time.Second

// Paymaster getter ABIs are parsed once at package init (these are constant,
// so a parse error is a programming bug and panicking is appropriate, like
// regexp.MustCompile). callPaymasterAddressGetter reuses them per call.
var (
	paymasterOwnerABI           = mustParseAddressGetterABI("owner")
	paymasterVerifyingSignerABI = mustParseAddressGetterABI("verifyingSigner")
)

// mustParseAddressGetterABI parses an ABI exposing a single no-arg view
// function returning an address (owner(), verifyingSigner()).
func mustParseAddressGetterABI(method string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(
		`[{"inputs":[],"name":"` + method + `","outputs":[{"internalType":"address","name":"","type":"address"}],"stateMutability":"view","type":"function"}]`))
	if err != nil {
		panic(fmt.Sprintf("config: parsing paymaster %s() ABI: %v", method, err))
	}
	return parsed
}

// callPaymasterAddressGetter invokes a no-arg, address-returning view function
// on the paymaster and returns the result. A short timeout bounds the call so
// an unresponsive RPC fails fast at boot.
func callPaymasterAddressGetter(client paymasterCaller, paymasterAddress common.Address, parsedABI abi.ABI, method string) (common.Address, error) {
	calldata, err := parsedABI.Pack(method)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to pack %s() call: %w", method, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), paymasterProbeTimeout)
	defer cancel()

	result, err := client.CallContract(ctx, ethereum.CallMsg{To: &paymasterAddress, Data: calldata}, nil)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to call %s() on paymaster: %w", method, err)
	}

	var out common.Address
	if err := parsedABI.UnpackIntoInterface(&out, method, result); err != nil {
		return common.Address{}, fmt.Errorf("failed to unpack %s() result: %w", method, err)
	}
	return out, nil
}

// fetchPaymasterOwner calls owner() on the paymaster contract to get the owner
// address. This is used for gas reimbursement — ETH is sent to the owner (EOA),
// not the contract itself.
func fetchPaymasterOwner(client paymasterCaller, paymasterAddress common.Address) (common.Address, error) {
	return callPaymasterAddressGetter(client, paymasterAddress, paymasterOwnerABI, "owner")
}

// fetchPaymasterVerifyingSigner calls verifyingSigner() on the VerifyingPaymaster
// contract. This is the address whose ECDSA signature the paymaster accepts on
// sponsored UserOps — it MUST equal the aggregator's controller address, since
// the aggregator signs the paymaster hash with the controller key. owner() (the
// admin/reimbursement EOA) is a distinct role and is NOT a substitute for this
// check: a paymaster can have the right owner but a different verifyingSigner,
// which silently breaks every sponsored UserOp at signing time
// (Sentry EIGENLAYER-AVS-1W — mainnet paymaster paired with the testnet
// controller key).
func fetchPaymasterVerifyingSigner(client paymasterCaller, paymasterAddress common.Address) (common.Address, error) {
	return callPaymasterAddressGetter(client, paymasterAddress, paymasterVerifyingSignerABI, "verifyingSigner")
}

// parseChainConfig converts a raw YAML chain config into a runtime ChainConfig.
// Unlike the top-level SmartWalletConfig, we don't connect to the chain RPC here —
// that's the worker's responsibility. We just parse and validate the config fields.
func parseChainConfig(raw ChainConfigRaw, logger sdklogging.Logger) (*ChainConfig, error) {
	if raw.ChainID <= 0 {
		return nil, fmt.Errorf("chain_id must be positive")
	}
	if raw.WorkerAddr == "" {
		return nil, fmt.Errorf("worker_addr is required")
	}

	sw := raw.SmartWallet

	// Parse controller private key
	controllerPrivateKey, err := crypto.HexToECDSA(sw.ControllerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parsing controller_private_key: %w", err)
	}

	// Enforce sane defaults for max wallets per owner
	maxWallets := sw.MaxWalletsPerOwner
	if maxWallets <= 0 {
		maxWallets = DefaultMaxWalletsPerOwner
	}
	if maxWallets > HardMaxWalletsPerOwner {
		maxWallets = HardMaxWalletsPerOwner
	}

	// Derive WS URL from RPC URL if not explicit (same provider, same
	// path, scheme swap). See the top-level derivation block in
	// NewConfig for rationale.
	wsURL := sw.EthWsUrl
	if wsURL == "" {
		wsURL = DeriveWsURL(sw.EthRpcUrl)
	}

	chainCfg := &ChainConfig{
		ChainID:    raw.ChainID,
		Name:       raw.Name,
		WorkerAddr: raw.WorkerAddr,
		SmartWallet: &SmartWalletConfig{
			EthRpcUrl:            sw.EthRpcUrl,
			EthWsUrl:             wsURL,
			BundlerURL:           sw.BundlerURL,
			BundlerProvider:      sw.BundlerProvider,
			AlchemyAPIKey:        sw.AlchemyAPIKey,
			FactoryAddress:       common.HexToAddress(firstNonEmpty(sw.FactoryAddress, DefaultFactoryProxyAddressHex)),
			EntrypointAddress:    common.HexToAddress(firstNonEmpty(sw.EntrypointAddress, DefaultEntrypointAddressHex)),
			ChainID:              raw.ChainID,
			ControllerPrivateKey: controllerPrivateKey,
			ControllerAddress:    crypto.PubkeyToAddress(controllerPrivateKey.PublicKey),
			PaymasterAddress:     common.HexToAddress(sw.PaymasterAddress),
			WhitelistAddresses:   convertToAddressSlice(sw.WhitelistAddresses),
			MaxWalletsPerOwner:   maxWallets,
		},
	}

	// Probe paymaster on this chain's RPC. Catches mismatched
	// paymaster/RPC pairings at startup instead of waiting for the
	// first UserOp to fail (Sentry EIGENLAYER-AVS-1N/1M). Skip when no
	// bundler resolves for the configured provider: that signals a
	// connectivity-only rollout (e.g. BNB Phase 0.5 in avs-infra/chains/),
	// where wallet ops are intentionally disabled and the paymaster_address
	// is a placeholder.
	if chainCfg.SmartWallet.BundlerConfigured() && chainCfg.SmartWallet.EthRpcUrl != "" {
		rpcClient, err := ethclient.Dial(chainCfg.SmartWallet.EthRpcUrl)
		if err != nil {
			return nil, fmt.Errorf("dial RPC %s for chain %s (chain_id=%d): %w",
				chainCfg.SmartWallet.EthRpcUrl, raw.Name, raw.ChainID, err)
		}
		defer rpcClient.Close()

		paymasterOwner, err := fetchPaymasterOwner(rpcClient, chainCfg.SmartWallet.PaymasterAddress)
		if err != nil {
			return nil, fmt.Errorf(
				"chain %s (chain_id=%d): paymaster %s is unreachable on RPC %s "+
					"(owner() call failed: %w) — verify the paymaster address is deployed "+
					"on this chain, and that the chain's eth_rpc_url points at the same chain "+
					"as the paymaster",
				raw.Name, raw.ChainID,
				chainCfg.SmartWallet.PaymasterAddress.Hex(),
				chainCfg.SmartWallet.EthRpcUrl,
				err,
			)
		}
		chainCfg.SmartWallet.PaymasterOwnerAddress = paymasterOwner

		// The controller key must match this paymaster's verifyingSigner, or
		// sponsored UserOps on this chain fail at signing time. This is the
		// per-chain analogue of the top-level probe — it catches a controller
		// key from the wrong network tier (Sentry EIGENLAYER-AVS-1W: the
		// gateway's testnet controller paired with the mainnet paymaster on
		// Ethereum + Base).
		verifyingSigner, err := fetchPaymasterVerifyingSigner(rpcClient, chainCfg.SmartWallet.PaymasterAddress)
		if err != nil {
			return nil, fmt.Errorf(
				"chain %s (chain_id=%d): paymaster %s verifyingSigner() call failed on RPC %s: %w",
				raw.Name, raw.ChainID, chainCfg.SmartWallet.PaymasterAddress.Hex(),
				chainCfg.SmartWallet.EthRpcUrl, err,
			)
		}
		if verifyingSigner != chainCfg.SmartWallet.ControllerAddress {
			return nil, fmt.Errorf(
				"chain %s (chain_id=%d): paymaster %s verifyingSigner (%s) does not match "+
					"controller address (%s) — the controller_private_key and paymaster_address "+
					"belong to different network tiers; set the controller key whose address "+
					"equals the paymaster's verifyingSigner",
				raw.Name, raw.ChainID, chainCfg.SmartWallet.PaymasterAddress.Hex(),
				verifyingSigner.Hex(), chainCfg.SmartWallet.ControllerAddress.Hex(),
			)
		}
	}

	logger.Info("Parsed chain config",
		"chain_id", chainCfg.ChainID,
		"name", chainCfg.Name,
		"worker_addr", chainCfg.WorkerAddr,
		"bundler_provider", chainCfg.SmartWallet.ProviderName(),
		"controller", chainCfg.SmartWallet.ControllerAddress.Hex(),
		"paymaster_owner", chainCfg.SmartWallet.PaymasterOwnerAddress.Hex())

	return chainCfg, nil
}

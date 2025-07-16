package operator

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/apconfig"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
	"github.com/AvaProtocol/EigenLayer-AVS/metrics"
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	rpccalls "github.com/Layr-Labs/eigensdk-go/metrics/collectors/rpc_calls"
	"github.com/Layr-Labs/eigensdk-go/nodeapi"
	"github.com/Layr-Labs/eigensdk-go/signerv2"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	gocron "github.com/go-co-op/gocron/v2"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Layr-Labs/eigensdk-go/chainio/clients"
	sdkelcontracts "github.com/Layr-Labs/eigensdk-go/chainio/clients/elcontracts"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients/eth"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients/wallet"
	"github.com/Layr-Labs/eigensdk-go/chainio/txmgr"
	"github.com/Layr-Labs/eigensdk-go/crypto/bls"
	sdkecdsa "github.com/Layr-Labs/eigensdk-go/crypto/ecdsa"

	"github.com/Layr-Labs/eigensdk-go/logging"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	sdkmetrics "github.com/Layr-Labs/eigensdk-go/metrics"
	sdktypes "github.com/Layr-Labs/eigensdk-go/types"

	//"github.com/AvaProtocol/EigenLayer-AVS/aggregator"
	cstaskmanager "github.com/AvaProtocol/EigenLayer-AVS/contracts/bindings/AutomationTaskManager"

	// insecure for local dev
	blssignerV1 "github.com/Layr-Labs/cerberus-api/pkg/api/v1"
	blscrypto "github.com/Layr-Labs/eigensdk-go/crypto/bls"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/version"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	triggerengine "github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/ipfetcher"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/timekeeper"
)

const AVS_NAME = "ap-avs"

type OperatorConfig struct {
	// used to set the logger level (true = info, false = debug)
	Production                    bool   `yaml:"production"`
	OperatorAddress               string `yaml:"operator_address"`
	OperatorStateRetrieverAddress string `yaml:"operator_state_retriever_address"`
	AVSRegistryCoordinatorAddress string `yaml:"avs_registry_coordinator_address"`
	EthRpcUrl                     string `yaml:"eth_rpc_url"`
	EthWsUrl                      string `yaml:"eth_ws_url"`
	EcdsaPrivateKeyStorePath      string `yaml:"ecdsa_private_key_store_path"`
	AggregatorServerIpPortAddress string `yaml:"aggregator_server_ip_port_address"`
	EigenMetricsIpPortAddress     string `yaml:"eigen_metrics_ip_port_address"`
	EnableMetrics                 bool   `yaml:"enable_metrics"`
	NodeApiIpPortAddress          string `yaml:"node_api_ip_port_address"`
	EnableNodeApi                 bool   `yaml:"enable_node_api"`

	DbPath string `yaml:"db_path"`

	PublicMetricsPort int32

	// Usually we don't need this, but on testnet, our target chain might be
	// differen from the chain where EigenLayer contract is deployed.
	// EigenLayer contracts are deployed on Holesky, but on holesky there isn't
	// much tooling around it: no official rpc bundler or erc4337 explorer, no
	// uniswap etc
	//
	// Therefore on testnet we will need this option when running in Holesky
	TargetChain struct {
		EthRpcUrl string `yaml:"eth_rpc_url"`
		EthWsUrl  string `yaml:"eth_ws_url"`
	} `yaml:"target_chain"`

	// Only one of bls option is needed: key or remote signer. when using remote signer, we also don't need the password in the env
	// the password of remote signer is the password we set with cerberus api
	BlsPrivateKeyStorePath string `yaml:"bls_private_key_store_path"`
	BlsRemoteSigner        struct {
		GrpcUrl string `yaml:"grpc_url"`
		// Publickey return from cerberus import/generation key
		PublicKey       string `yaml:"public_key"`
		Password        string `yaml:"password"`
		TLSCertFilePath string `yaml:"tls_cert_file_path"`
	} `yaml:"bls_remote_signer"`

	EnabledFeatures struct {
		EventTrigger bool `yaml:"event_trigger"`
	} `yaml:"enabled_features"`

	// Event processing safety limits - protect operator from overload
	EventSafety struct {
		MaxTotalEventsPerBlock    uint32 `yaml:"max_total_events_per_block"`     // Across all queries (default: 1000)
		MaxEventsPerQueryPerBlock uint32 `yaml:"max_events_per_query_per_block"` // Per individual query (default: 500)
	} `yaml:"event_safety"`
}

type Operator struct {
	config      *OperatorConfig
	logger      logging.Logger
	ethClient   *eth.InstrumentedClient
	ethWsClient *eth.InstrumentedClient
	txManager   *txmgr.SimpleTxManager

	metricsReg       *prometheus.Registry
	metrics          metrics.MetricsGenerator
	nodeApi          *nodeapi.NodeApi
	avsWriter        *chainio.AvsWriter
	avsReader        *chainio.AvsReader
	eigenlayerReader *sdkelcontracts.ChainReader
	eigenlayerWriter *sdkelcontracts.ChainWriter

	// either keypair or RemoteSigner need to be defined
	blsKeypair      *bls.KeyPair
	blsRemoteSigner blssignerV1.SignerClient

	operatorId   sdktypes.OperatorId
	operatorAddr common.Address
	// Through the passpharese of operator ecdsa, we can compute the private key
	operatorEcdsaPrivateKey *ecdsa.PrivateKey

	// signerAddress match operatorAddr unless the operator use alias key
	signerAddress common.Address

	// receive new tasks in this chan (typically from listening to onchain event)
	newTaskCreatedChan chan *cstaskmanager.ContractAutomationTaskManagerNewTaskCreated

	// rpc client to send signed task responses to aggregator
	nodeRpcClient  avsproto.NodeClient
	aggregatorConn *grpc.ClientConn

	// needed when opting in to avs (allow this service manager contract to slash operator)
	credibleSquaringServiceManagerAddr common.Address

	// contract that hold our configuration. Currently only alias key mapping
	apConfigAddr common.Address

	elapsing *timekeeper.Elapsing

	publicIP string

	scheduler    gocron.Scheduler
	eventTrigger *triggerengine.EventTrigger
	blockTrigger *triggerengine.BlockTrigger
	timeTrigger  *triggerengine.TimeTrigger

	// Error debouncing to prevent log spam - track last time we logged specific error types
	lastPingErrorTime   time.Time
	lastStreamErrorTime time.Time
	lastPingErrorType   string
	lastStreamErrorType string

	// Success message debouncing to prevent log spam
	lastPingSuccessTime time.Time
}

// validateRPCEndpoint checks if the RPC endpoint is accessible
func validateRPCEndpoint(rpcURL string, logger logging.Logger) error {
	logger.Infof("Validating RPC endpoint: %s", rpcURL)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Create a simple JSON-RPC request to test the endpoint
	jsonRPCPayload := `{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}`
	logger.Infof("Sending JSON-RPC request: %s", jsonRPCPayload)

	// Test JSON-RPC connectivity with POST request
	resp, err := client.Post(rpcURL, "application/json", strings.NewReader(jsonRPCPayload))
	if err != nil {
		logger.Errorf("RPC endpoint validation failed - connection error: %v", err)
		return fmt.Errorf("RPC endpoint %s is not accessible: %w", rpcURL, err)
	}
	defer resp.Body.Close()

	// Read response body for debugging
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		logger.Warnf("Could not read response body: %v", readErr)
	} else {
		logger.Infof("RPC response body: %s", string(body))
	}

	// Check if the HTTP status code indicates success (2xx range)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Errorf("RPC endpoint validation failed - HTTP error: %s (status: %d)", rpcURL, resp.StatusCode)
		if len(body) > 0 {
			logger.Errorf("Response body: %s", string(body))
		}
		return fmt.Errorf("RPC endpoint %s returned HTTP error status: %d", rpcURL, resp.StatusCode)
	}

	logger.Infof("RPC endpoint validation successful: %s (status: %d)", rpcURL, resp.StatusCode)
	return nil
}

func RunWithConfig(configPath string) {
	operator, e := NewOperatorFromConfigFile(configPath)
	if e != nil {
		panic(fmt.Errorf("cannot create operator. %w", e))
	}

	operator.Start(context.Background())
}

func NewOperatorFromConfigFile(configPath string) (*Operator, error) {
	nodeConfig := OperatorConfig{}
	err := config.ReadYamlConfig(configPath, &nodeConfig)

	if err != nil {
		panic(fmt.Errorf("failed to parse config file: %s\nMake sure it exists and is a valid yaml file %w", configPath, err))
	}

	return NewOperatorFromConfig(nodeConfig)
}

// take the config in core (which is shared with aggregator and challenger)
func NewOperatorFromConfig(c OperatorConfig) (*Operator, error) {
	elapsing := timekeeper.NewElapsing()

	var logLevel logging.LogLevel
	if c.Production {
		logLevel = sdklogging.Production
	} else {
		logLevel = sdklogging.Development
	}
	logger, err := sdklogging.NewZapLogger(logLevel)
	if err != nil {
		return nil, err
	}

	// Log concise configuration for startup
	logger.Infof("🚀 Starting Operator %s (v%s) on chain %s", c.OperatorAddress, version.Get(), "auto-detect")

	// Validate RPC endpoints before proceeding
	if err := validateRPCEndpoint(c.EthRpcUrl, logger); err != nil {
		logger.Errorf("RPC endpoint validation failed: %v", err)
		return nil, fmt.Errorf("RPC endpoint validation failed: %w", err)
	}
	reg := prometheus.NewRegistry()
	eigenMetrics := sdkmetrics.NewEigenMetrics(AVS_NAME, c.EigenMetricsIpPortAddress, reg, logger)
	avsAndEigenMetrics := metrics.NewAvsAndEigenMetrics(AVS_NAME, strings.ToLower(c.OperatorAddress), version.Get(), eigenMetrics, reg)

	// Validate that metrics was created successfully - this should never be nil in production
	if avsAndEigenMetrics == nil {
		logger.Errorf("❌ CRITICAL: Failed to create metrics - this indicates a serious initialization problem")
		return nil, fmt.Errorf("failed to create metrics - this indicates a serious initialization problem")
	}

	// Setup Node Api
	nodeApi := nodeapi.NewNodeApi(AVS_NAME, version.Get(), c.NodeApiIpPortAddress, logger)

	// Starting operator message removed - already logged above

	var ethRpcClient *eth.InstrumentedClient
	var ethWsClient *eth.InstrumentedClient

	logger.Debug("initialize rpc call collector")
	rpcCallsCollector := rpccalls.NewCollector(AVS_NAME, reg)
	if c.EnableMetrics {
		ethRpcClient, err = eth.NewInstrumentedClient(c.EthRpcUrl, rpcCallsCollector)
		if err != nil {
			logger.Errorf("Cannot connect to RPC endpoint", "url", c.EthRpcUrl, "err", err)
			return nil, fmt.Errorf("failed to connect to RPC endpoint %s: %w", c.EthRpcUrl, err)
		}
		ethWsClient, err = eth.NewInstrumentedClient(c.EthWsUrl, rpcCallsCollector)
		if err != nil {
			logger.Errorf("Cannot connect to WebSocket RPC endpoint", "url", c.EthWsUrl, "err", err)
			return nil, fmt.Errorf("failed to connect to WebSocket RPC endpoint %s: %w", c.EthWsUrl, err)
		}
	} else {
		ethRpcClient, err = eth.NewInstrumentedClient(c.EthRpcUrl, rpcCallsCollector)
		if err != nil {
			logger.Errorf("Cannot connect to RPC endpoint", "url", c.EthRpcUrl, "err", err)
			return nil, fmt.Errorf("failed to connect to RPC endpoint %s: %w", c.EthRpcUrl, err)
		}
		ethWsClient, err = eth.NewInstrumentedClient(c.EthWsUrl, rpcCallsCollector)
		if err != nil {
			logger.Errorf("Cannot connect to WebSocket RPC endpoint", "url", c.EthWsUrl, "err", err)
			return nil, fmt.Errorf("failed to connect to WebSocket RPC endpoint %s: %w", c.EthWsUrl, err)
		}
	}

	var blsRemoteSigner blssignerV1.SignerClient
	var blsKeyPair *bls.KeyPair
	if c.BlsRemoteSigner.GrpcUrl != "" {
		logger.Debug("initialize remote signer", "grpc_url", c.BlsRemoteSigner.GrpcUrl)
		logger.Info("creating signer client", "url", c.BlsRemoteSigner.GrpcUrl, "publickey", c.BlsRemoteSigner.PublicKey)
		creds := insecure.NewCredentials()
		if c.BlsRemoteSigner.TLSCertFilePath != "" {
			creds, err = credentials.NewClientTLSFromFile(c.BlsRemoteSigner.TLSCertFilePath, "")
			if err != nil {
				return nil, err
			}
		}
		cerberusConn, err := grpc.NewClient(
			c.BlsRemoteSigner.GrpcUrl, grpc.WithTransportCredentials(creds),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create new BLS remote signer client: %w", err)
		}
		blsRemoteSigner = blssignerV1.NewSignerClient(cerberusConn)
	} else {
		blsKeyPassword, ok := os.LookupEnv("OPERATOR_BLS_KEY_PASSWORD")
		if !ok {
			logger.Warnf("OPERATOR_BLS_KEY_PASSWORD env var not set. using empty string")
		}
		blsKeyPair, err = bls.ReadPrivateKeyFromFile(c.BlsPrivateKeyStorePath, blsKeyPassword)
		if err != nil {
			logger.Errorf("Cannot parse bls private key: %s err: %w", c.BlsPrivateKeyStorePath, err)
			return nil, err
		}
	}

	chainId, err := ethRpcClient.ChainID(context.Background())
	if err != nil {
		logger.Error("Cannot get chainId", "err", err)
		return nil, err
	}
	logger.Infof("🔗 Connected to EigenLayer on chain %d", chainId)

	// Create a temporary operator instance to call PopulateKnownConfigByChainID
	tempOperator := &Operator{
		config: &c,
		logger: logger,
	}

	// Apply chain-specific configuration and default addresses
	if err := tempOperator.PopulateKnownConfigByChainID(chainId); err != nil {
		logger.Errorf("Failed to populate chain-specific configuration: %v", err)
		return nil, fmt.Errorf("failed to populate chain-specific configuration: %w", err)
	}

	ecdsaKeyPassword := loadECDSAPassword()

	signerV2, signerAddress, err := signerv2.SignerFromConfig(signerv2.Config{
		KeystorePath: c.EcdsaPrivateKeyStorePath,
		Password:     ecdsaKeyPassword,
	}, chainId)

	if err != nil {
		panic(err)
	}

	chainioConfig := clients.BuildAllConfig{
		EthHttpUrl:                 c.EthRpcUrl,
		EthWsUrl:                   c.EthWsUrl,
		RegistryCoordinatorAddr:    c.AVSRegistryCoordinatorAddress,
		OperatorStateRetrieverAddr: c.OperatorStateRetrieverAddress,
		AvsName:                    AVS_NAME,
		PromMetricsIpPortAddress:   c.EigenMetricsIpPortAddress,
	}

	// EigenLayer SDK configuration logging removed - details available in debug mode

	operatorEcdsaPrivateKey, err := sdkecdsa.ReadKey(
		c.EcdsaPrivateKeyStorePath,
		ecdsaKeyPassword,
	)

	if err != nil {
		return nil, err
	}

	sdkClients, err := clients.BuildAll(chainioConfig, operatorEcdsaPrivateKey, logger)
	if err != nil {
		logger.Errorf("❌ Failed to build EigenLayer SDK clients: %v", err)
		return nil, fmt.Errorf("failed to build EigenLayer SDK clients: %w", err)
	}
	skWallet, err := wallet.NewPrivateKeyWallet(ethRpcClient, signerV2, signerAddress, logger)
	if err != nil {
		panic(err)
	}
	txMgr := txmgr.NewSimpleTxManager(skWallet, ethRpcClient, logger, signerAddress)

	avsWriter, err := chainio.BuildAvsWriter(
		txMgr, common.HexToAddress(c.AVSRegistryCoordinatorAddress),
		common.HexToAddress(c.OperatorStateRetrieverAddress), ethRpcClient, logger,
	)
	if err != nil {
		logger.Errorf("❌ Cannot create AvsWriter: %v", err)
		return nil, fmt.Errorf("failed to create AvsWriter: %w", err)
	}

	avsReader, err := chainio.BuildAvsReader(
		common.HexToAddress(c.AVSRegistryCoordinatorAddress),
		common.HexToAddress(c.OperatorStateRetrieverAddress),
		ethRpcClient, logger)
	if err != nil {
		logger.Errorf("❌ Cannot create AvsReader: %v", err)
		return nil, fmt.Errorf("failed to create AvsReader: %w", err)
	}
	// avsSubscriber, err := chainio.BuildAvsSubscriber(common.HexToAddress(c.AVSRegistryCoordinatorAddress),
	// 	common.HexToAddress(c.OperatorStateRetrieverAddress), ethWsClient, logger,
	// )
	// if err != nil {
	// 	logger.Error("Cannot create AvsSubscriber", "err", err)
	// 	return nil, err
	// }

	// We must register the economic metrics separately because they are exported metrics (from jsonrpc or subgraph calls)
	// and not instrumented metrics: see https://prometheus.io/docs/instrumenting/writing_clientlibs/#overall-structure
	quorumNames := map[sdktypes.QuorumNum]string{
		0: "quorum0",
	}
	economicMetricsCollector := metrics.NewMetricsOnlyEconomicCollector(
		sdkClients.ElChainReader, sdkClients.AvsRegistryChainReader,
		AVS_NAME, logger, common.HexToAddress(c.OperatorAddress), quorumNames)
	reg.MustRegister(economicMetricsCollector)

	operator := &Operator{
		config:      &c,
		logger:      logger,
		metricsReg:  reg,
		metrics:     avsAndEigenMetrics,
		nodeApi:     nodeApi,
		ethClient:   ethRpcClient,
		ethWsClient: ethWsClient,
		avsWriter:   avsWriter,
		avsReader:   avsReader,

		// avsSubscriber:                      avsSubscriber,
		eigenlayerReader: sdkClients.ElChainReader,
		eigenlayerWriter: sdkClients.ElChainWriter,

		blsKeypair:      blsKeyPair,
		blsRemoteSigner: blsRemoteSigner,

		operatorAddr:  common.HexToAddress(c.OperatorAddress),
		signerAddress: signerAddress,

		//nodeRpcClient: nodeRpcClient,
		//aggregatorConn:      aggregatorConn,

		newTaskCreatedChan:                 make(chan *cstaskmanager.ContractAutomationTaskManagerNewTaskCreated),
		credibleSquaringServiceManagerAddr: common.HexToAddress(c.AVSRegistryCoordinatorAddress),
		operatorId:                         [32]byte{0}, // this is set below
		operatorEcdsaPrivateKey:            operatorEcdsaPrivateKey,

		// Copy apConfigAddr from tempOperator (set by PopulateKnownConfigByChainID)
		apConfigAddr: tempOperator.apConfigAddr,

		txManager: txMgr,
		elapsing:  elapsing,
	}

	logger.Infof("🔌 Connecting to aggregator %s", c.AggregatorServerIpPortAddress)
	operator.retryConnect()

	// OperatorId is set in contract during registration so we get it after registering operator.
	operatorId, err := sdkClients.AvsRegistryChainReader.GetOperatorId(&bind.CallOpts{}, operator.operatorAddr)
	if err != nil {
		logger.Error("Cannot get operator id", "err", err)
		return nil, err
	}
	operator.operatorId = operatorId
	if operator.blsKeypair != nil {
		logger.Debug("Operator info",
			"operatorId", operatorId,
			"operatorAddr", c.OperatorAddress,
			"signerAddr", operator.signerAddress,
			"operatorG1Pubkey", operator.blsKeypair.GetPubKeyG1(),
			"operatorG2Pubkey", operator.blsKeypair.GetPubKeyG2(),
			"prmMetricsEndpoint", fmt.Sprintf("%s/metrics/", operator.config.EigenMetricsIpPortAddress),
		)
	} else {
		logger.Debug("Operator info",
			"operatorId", operatorId,
			"operatorAddr", c.OperatorAddress,
			"signerAddr", operator.signerAddress,
			"remoteSignerUrl", operator.config.BlsRemoteSigner.GrpcUrl,
			"remoteSignerPubKey", operator.config.BlsRemoteSigner.PublicKey,
			"prmMetricsEndpoint", fmt.Sprintf("%s/metrics/", operator.config.EigenMetricsIpPortAddress),
		)
	}

	return operator, nil
}

// main entry function to bootstrap an operator
func (o *Operator) Start(ctx context.Context) error {
	if o.signerAddress.Cmp(o.operatorAddr) != 0 {
		// Ensure alias key is correctly bind to operator address
		o.logger.Infof("checking operator alias address. operator: %s alias %s", o.operatorAddr, o.signerAddress)
		o.logger.Infof("APConfig contract address: %s", o.apConfigAddr.Hex())

		apConfigContract, err := apconfig.GetContract(o.config.EthRpcUrl, o.apConfigAddr)
		if err != nil {
			o.logger.Errorf("❌ Failed to get APConfig contract at %s: %v", o.apConfigAddr.Hex(), err)
			return fmt.Errorf("failed to get APConfig contract: %w", err)
		}

		// Create timeout context for the contract call (30 seconds)
		timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		// Create proper call options with timeout
		callOpts := &bind.CallOpts{
			Pending: false,
			Context: timeoutCtx,
		}

		o.logger.Infof("Calling GetAlias on APConfig contract for operator %s...", o.operatorAddr.Hex())
		aliasAddress, err := apConfigContract.GetAlias(callOpts, o.operatorAddr)
		if err != nil {
			o.logger.Errorf("❌ Failed to get alias for operator %s from APConfig contract", o.operatorAddr.Hex())
			o.logger.Errorf("   Error: %v", err)
			o.logger.Infof("🔧 SOLUTION: You need to declare/update your alias key mapping in the APConfig contract")
			o.logger.Infof("   Run: ./out/ap operator declareAlias --config=%s --address=%s", "config/operator-ethereum.yaml", "/path/to/your/alias/key.json")
			return fmt.Errorf("failed to get alias for operator %s: %w", o.operatorAddr.Hex(), err)
		}

		o.logger.Infof("Retrieved alias address from contract: %s", aliasAddress.Hex())
		if o.signerAddress.Cmp(aliasAddress) == 0 {
			o.logger.Infof("✅ Confirmed operator %s matches alias %s", o.operatorAddr, o.signerAddress)
		} else {
			o.logger.Errorf("❌ ALIAS MISMATCH:")
			o.logger.Errorf("   Expected alias (from your key): %s", o.signerAddress.Hex())
			o.logger.Errorf("   Actual alias (from contract):  %s", aliasAddress.Hex())
			o.logger.Infof("🔧 SOLUTION: Update your alias key mapping in the APConfig contract")
			o.logger.Infof("   Run: ./out/ap operator declareAlias --config=%s --address=%s", "config/operator-ethereum.yaml", "/path/to/your/correct/alias/key.json")
			return fmt.Errorf("ECDSA private key doesn't match the declared alias address. Expected: %s, Got: %s", o.signerAddress.Hex(), aliasAddress.Hex())
		}
	}

	operatorIsRegistered, err := o.avsReader.IsOperatorRegistered(&bind.CallOpts{}, o.operatorAddr)
	if err != nil {
		o.logger.Error("Error checking if operator is registered", "err", err)
		return err
	}
	if !operatorIsRegistered {
		// We bubble the error all the way up instead of using logger.Fatal because logger.Fatal prints a huge stack trace
		// that hides the actual error message. This error msg is more explicit and doesn't require showing a stack trace to the user.
		return fmt.Errorf("operator is not registered. Registering operator using the operator-cli before starting operator")
	}

	o.logger.Infof("✅ Operator ready - starting service")

	if o.config.EnableNodeApi {
		o.nodeApi.Start()
	}

	defer o.aggregatorConn.Close()
	return o.runWorkLoop(ctx)
}

func (o *Operator) retryConnect() error {
	// grpc client
	var opts []grpc.DialOption
	opts = append(opts,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.ClientAuth{
			EcdsaPrivateKey: o.operatorEcdsaPrivateKey,
			SignerAddr:      o.operatorAddr,
		}),
	)
	o.logger.Info("🔗 Attempting to connect to aggregator service",
		"aggregator_address", o.config.AggregatorServerIpPortAddress,
		"operator", o.config.OperatorAddress)

	var err error
	o.aggregatorConn, err = grpc.NewClient(o.config.AggregatorServerIpPortAddress, opts...)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") {
			o.logger.Error("❌ Cannot create gRPC client for aggregator",
				"aggregator_address", o.config.AggregatorServerIpPortAddress,
				"operator", o.config.OperatorAddress,
				"raw_error", err)
		} else {
			o.logger.Error("❌ Failed to create gRPC client for aggregator",
				"aggregator_address", o.config.AggregatorServerIpPortAddress,
				"operator", o.config.OperatorAddress,
				"raw_error", err)
		}
		return err
	}

	o.nodeRpcClient = avsproto.NewNodeClient(o.aggregatorConn)

	// Actually test the connection by making a health check call
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, healthErr := o.nodeRpcClient.HealthCheck(ctx, &avspb.HealthCheckRequest{
		OperatorAddress: o.config.OperatorAddress,
	})

	if healthErr != nil {
		if strings.Contains(healthErr.Error(), "connection refused") {
			o.logger.Error("❌ Cannot connect to aggregator - service appears to be down",
				"aggregator_address", o.config.AggregatorServerIpPortAddress,
				"operator", o.config.OperatorAddress,
				"raw_error", healthErr)
		} else if strings.Contains(healthErr.Error(), "no such host") || strings.Contains(healthErr.Error(), "name resolution") {
			o.logger.Error("❌ Cannot resolve aggregator hostname",
				"aggregator_address", o.config.AggregatorServerIpPortAddress,
				"operator", o.config.OperatorAddress,
				"raw_error", healthErr)
		} else {
			o.logger.Error("❌ Failed to establish connection to aggregator",
				"aggregator_address", o.config.AggregatorServerIpPortAddress,
				"operator", o.config.OperatorAddress,
				"raw_error", healthErr)
		}
		// Close the connection since we couldn't actually connect
		o.aggregatorConn.Close()
		return healthErr
	}

	o.logger.Info("✅ Successfully connected to aggregator service",
		"aggregator_address", o.config.AggregatorServerIpPortAddress,
		"operator", o.config.OperatorAddress)
	return nil
}

// Optimistic get public ip address of the operator
// the IP address is used in combination with
func (o *Operator) GetPublicIP() string {
	if o.publicIP == "" {
		var err error
		o.publicIP, err = ipfetcher.GetIP()
		if err != nil {
			// We will retry and eventually successful, the public ip isn't
			// being used widely in our operation, only for metric scrape
			o.logger.Errorf("error fetching public ip address %v", err)
		}
	}

	return o.publicIP
}

func (c *OperatorConfig) GetPublicMetricPort() int32 {
	// If we had port from env, use it, if not, we parse the port from config
	if c.PublicMetricsPort > 0 {
		return c.PublicMetricsPort
	}

	port := os.Getenv("PUBLIC_METRICS_PORT")
	if port == "" {
		parts := strings.Split(c.EigenMetricsIpPortAddress, ":")
		if len(parts) != 2 {
			panic(fmt.Errorf("EigenMetricsIpPortAddress: %s in operator config file is malform", c.EigenMetricsIpPortAddress))
		}

		port = parts[1]
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		// Just use default port if parsing fails
		portNum = 8080
	}

	c.PublicMetricsPort = int32(portNum)
	return c.PublicMetricsPort
}

// GetMaxTotalEventsPerBlock returns the configured limit with default fallback
func (c *OperatorConfig) GetMaxTotalEventsPerBlock() uint32 {
	if c.EventSafety.MaxTotalEventsPerBlock > 0 {
		return c.EventSafety.MaxTotalEventsPerBlock
	}
	return 1000 // Default: 1000 events per block across all queries
}

// GetMaxEventsPerQueryPerBlock returns the configured limit with default fallback
func (c *OperatorConfig) GetMaxEventsPerQueryPerBlock() uint32 {
	if c.EventSafety.MaxEventsPerQueryPerBlock > 0 {
		return c.EventSafety.MaxEventsPerQueryPerBlock
	}
	return 500 // Default: 500 events per query per block
}

func (o *Operator) GetSignature(ctx context.Context, message []byte) (*blscrypto.Signature, error) {
	if o.blsRemoteSigner != nil {
		data, e := signer.Byte32Digest(message)
		if e != nil {
			return nil, fmt.Errorf("error generate 32bytes digest for bls signature: %w", e)
		}

		sigResp, err := o.blsRemoteSigner.SignGeneric(
			ctx,
			&blssignerV1.SignGenericRequest{
				PublicKey: o.config.BlsRemoteSigner.PublicKey,
				Password:  o.config.BlsRemoteSigner.Password,
				Data:      data[:],
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to sign data: %w", err)
		}
		sig := new(blscrypto.Signature)
		g := sig.Deserialize(sigResp.Signature)
		// we will need G2Point to verify this signature
		return &blscrypto.Signature{
			G1Point: g,
		}, nil
	}

	sig := signer.SignBlsMessage(o.blsKeypair, message)
	if sig == nil {
		return nil, fmt.Errorf("failed to generate digest for bls sign: %v", message)
	}

	return sig, nil
}

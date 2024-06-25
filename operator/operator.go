package operator

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"os"

	"google.golang.org/grpc"

	"github.com/AvaProtocol/ap-avs/core/chainio"
	"github.com/AvaProtocol/ap-avs/metrics"
	"github.com/Layr-Labs/eigensdk-go/metrics/collectors/economic"
	rpccalls "github.com/Layr-Labs/eigensdk-go/metrics/collectors/rpc_calls"
	"github.com/Layr-Labs/eigensdk-go/nodeapi"
	"github.com/Layr-Labs/eigensdk-go/signerv2"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

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
	sdkutils "github.com/Layr-Labs/eigensdk-go/utils"

	//"github.com/AvaProtocol/ap-avs/aggregator"
	cstaskmanager "github.com/AvaProtocol/ap-avs/contracts/bindings/AutomationTaskManager"

	// insecure for local dev

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"google.golang.org/grpc/credentials/insecure"
)

const AVS_NAME = "ap-avs"

// TODO: inject via builder flag
const SEM_VER = "0.0.2"
const AppName = "ap-avs-operator"

type OperatorConfig struct {
	// used to set the logger level (true = info, false = debug)
	Production                    bool   `yaml:"production"`
	OperatorAddress               string `yaml:"operator_address"`
	OperatorStateRetrieverAddress string `yaml:"operator_state_retriever_address"`
	AVSRegistryCoordinatorAddress string `yaml:"avs_registry_coordinator_address"`
	EthRpcUrl                     string `yaml:"eth_rpc_url"`
	EthWsUrl                      string `yaml:"eth_ws_url"`
	BlsPrivateKeyStorePath        string `yaml:"bls_private_key_store_path"`
	EcdsaPrivateKeyStorePath      string `yaml:"ecdsa_private_key_store_path"`
	AggregatorServerIpPortAddress string `yaml:"aggregator_server_ip_port_address"`
	EigenMetricsIpPortAddress     string `yaml:"eigen_metrics_ip_port_address"`
	EnableMetrics                 bool   `yaml:"enable_metrics"`
	NodeApiIpPortAddress          string `yaml:"node_api_ip_port_address"`
	EnableNodeApi                 bool   `yaml:"enable_node_api"`
}

type Operator struct {
	config      OperatorConfig
	logger      logging.Logger
	ethClient   eth.Client
	ethWsClient eth.Client

	// TODO(samlaf): remove both avsWriter and eigenlayerWrite from operator
	// they are only used for registration, so we should make a special registration package
	// this way, auditing this operator code makes it obvious that operators don't need to
	// write to the chain during the course of their normal operations
	// writing to the chain should be done via the cli only
	metricsReg       *prometheus.Registry
	metrics          metrics.Metrics
	nodeApi          *nodeapi.NodeApi
	avsWriter        *chainio.AvsWriter
	avsReader        chainio.AvsReaderer
	eigenlayerReader sdkelcontracts.ELReader
	eigenlayerWriter sdkelcontracts.ELWriter
	blsKeypair       *bls.KeyPair

	operatorId   sdktypes.OperatorId
	operatorAddr common.Address
	// Through the passpharese of operator ecdsa, we can compute the private key
	operatorEcdsaPrivateKey *ecdsa.PrivateKey

	// receive new tasks in this chan (typically from listening to onchain event)
	newTaskCreatedChan chan *cstaskmanager.ContractAutomationTaskManagerNewTaskCreated
	// rpc client to send signed task responses to aggregator
	aggregatorRpcClient avsproto.AggregatorClient
	aggregatorConn      *grpc.ClientConn
	// needed when opting in to avs (allow this service manager contract to slash operator)
	credibleSquaringServiceManagerAddr common.Address
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
	err := sdkutils.ReadYamlConfig(configPath, &nodeConfig)

	fmt.Printf("loaded config: %v\n", nodeConfig)
	if err != nil {
		panic(fmt.Errorf("failed to parse config file: %w\nMake sure %s is exist and a valid yaml file %w.", configPath, err))
	}

	return NewOperatorFromConfig(nodeConfig)
}

// take the config in core (which is shared with aggregator and challenger)
func NewOperatorFromConfig(c OperatorConfig) (*Operator, error) {
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
	reg := prometheus.NewRegistry()
	eigenMetrics := sdkmetrics.NewEigenMetrics(AVS_NAME, c.EigenMetricsIpPortAddress, reg, logger)
	avsAndEigenMetrics := metrics.NewAvsAndEigenMetrics(AVS_NAME, eigenMetrics, reg)

	// Setup Node Api
	nodeApi := nodeapi.NewNodeApi(AVS_NAME, SEM_VER, c.NodeApiIpPortAddress, logger)

	var ethRpcClient, ethWsClient eth.Client
	if c.EnableMetrics {
		rpcCallsCollector := rpccalls.NewCollector(AVS_NAME, reg)
		ethRpcClient, err = eth.NewInstrumentedClient(c.EthRpcUrl, rpcCallsCollector)
		if err != nil {
			logger.Errorf("Cannot create http ethclient", "err", err)
			return nil, err
		}
		ethWsClient, err = eth.NewInstrumentedClient(c.EthWsUrl, rpcCallsCollector)
		if err != nil {
			logger.Errorf("Cannot create ws ethclient", "err", err)
			return nil, err
		}
	} else {
		ethRpcClient, err = eth.NewClient(c.EthRpcUrl)
		if err != nil {
			logger.Errorf("Cannot create http ethclient", "err", err)
			return nil, err
		}
		ethWsClient, err = eth.NewClient(c.EthWsUrl)
		if err != nil {
			logger.Errorf("Cannot create ws ethclient", "err", err)
			return nil, err
		}
	}

	blsKeyPassword, ok := os.LookupEnv("OPERATOR_BLS_KEY_PASSWORD")
	if !ok {
		logger.Warnf("OPERATOR_BLS_KEY_PASSWORD env var not set. using empty string")
	}
	blsKeyPair, err := bls.ReadPrivateKeyFromFile(c.BlsPrivateKeyStorePath, blsKeyPassword)
	if err != nil {
		logger.Errorf("Cannot parse bls private key: %s err: %w", c.BlsPrivateKeyStorePath, err)
		return nil, err
	}
	// TODO(samlaf): should we add the chainId to the config instead?
	// this way we can prevent creating a signer that signs on mainnet by mistake
	// if the config says chainId=5, then we can only create a goerli signer
	chainId, err := ethRpcClient.ChainID(context.Background())
	if err != nil {
		logger.Error("Cannot get chainId", "err", err)
		return nil, err
	}

	ecdsaKeyPassword, ok := os.LookupEnv("OPERATOR_ECDSA_KEY_PASSWORD")
	if !ok {
		logger.Warnf("OPERATOR_ECDSA_KEY_PASSWORD env var not set. using empty string")
	}

	signerV2, _, err := signerv2.SignerFromConfig(signerv2.Config{
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
	operatorEcdsaPrivateKey, err := sdkecdsa.ReadKey(
		c.EcdsaPrivateKeyStorePath,
		ecdsaKeyPassword,
	)

	if err != nil {
		return nil, err
	}
	sdkClients, err := clients.BuildAll(chainioConfig, operatorEcdsaPrivateKey, logger)
	if err != nil {
		panic(err)
	}
	skWallet, err := wallet.NewPrivateKeyWallet(ethRpcClient, signerV2, common.HexToAddress(c.OperatorAddress), logger)
	if err != nil {
		panic(err)
	}
	txMgr := txmgr.NewSimpleTxManager(skWallet, ethRpcClient, logger, common.HexToAddress(c.OperatorAddress))

	avsWriter, err := chainio.BuildAvsWriter(
		txMgr, common.HexToAddress(c.AVSRegistryCoordinatorAddress),
		common.HexToAddress(c.OperatorStateRetrieverAddress), ethRpcClient, logger,
	)
	if err != nil {
		logger.Error("Cannot create AvsWriter", "err", err)
		return nil, err
	}

	avsReader, err := chainio.BuildAvsReader(
		common.HexToAddress(c.AVSRegistryCoordinatorAddress),
		common.HexToAddress(c.OperatorStateRetrieverAddress),
		ethRpcClient, logger)
	if err != nil {
		logger.Error("Cannot create AvsReader", "err", err)
		return nil, err
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
	economicMetricsCollector := economic.NewCollector(
		sdkClients.ElChainReader, sdkClients.AvsRegistryChainReader,
		AVS_NAME, logger, common.HexToAddress(c.OperatorAddress), quorumNames)
	reg.MustRegister(economicMetricsCollector)

	// grpc client
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	logger.Infof("Connect to aggregator %s", c.AggregatorServerIpPortAddress)
	aggregatorConn, err := grpc.NewClient(c.AggregatorServerIpPortAddress, opts...)
	if err != nil {
		panic(err)
	}
	aggregatorRpcClient := avsproto.NewAggregatorClient(aggregatorConn)
	//aggregatorRpcClient, err := NewAggregatorRpcClient(c.AggregatorServerIpPortAddress, logger, avsAndEigenMetrics)
	//if err != nil {
	//	logger.Error("Cannot create AggregatorRpcClient. Is aggregator running?", "err", err)
	//	return nil, err
	//}

	operator := &Operator{
		config:      c,
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
		blsKeypair:       blsKeyPair,
		operatorAddr:     common.HexToAddress(c.OperatorAddress),

		aggregatorRpcClient: aggregatorRpcClient,
		aggregatorConn:      aggregatorConn,

		newTaskCreatedChan:                 make(chan *cstaskmanager.ContractAutomationTaskManagerNewTaskCreated),
		credibleSquaringServiceManagerAddr: common.HexToAddress(c.AVSRegistryCoordinatorAddress),
		operatorId:                         [32]byte{0}, // this is set below
		operatorEcdsaPrivateKey:            operatorEcdsaPrivateKey,
	}

	// OperatorId is set in contract during registration so we get it after registering operator.
	operatorId, err := sdkClients.AvsRegistryChainReader.GetOperatorId(&bind.CallOpts{}, operator.operatorAddr)
	if err != nil {
		logger.Error("Cannot get operator id", "err", err)
		return nil, err
	}
	operator.operatorId = operatorId
	logger.Info("Operator info",
		"operatorId", operatorId,
		"operatorAddr", c.OperatorAddress,
		"operatorG1Pubkey", operator.blsKeypair.GetPubKeyG1(),
		"operatorG2Pubkey", operator.blsKeypair.GetPubKeyG2(),
	)

	return operator, nil

}

func (o *Operator) Start(ctx context.Context) error {
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

	o.logger.Infof("Starting operator.")

	if o.config.EnableNodeApi {
		o.nodeApi.Start()
	}

	defer o.aggregatorConn.Close()
	return o.runWorkLoop(ctx)
}

// // Takes a NewTaskCreatedLog struct as input and returns a TaskResponseHeader struct.
// // The TaskResponseHeader struct is the struct that is signed and sent to the contract as a task response.
// func (o *Operator) ProcessNewTaskCreatedLog(newTaskCreatedLog *cstaskmanager.ContractAutomationTaskManagerNewTaskCreated) *cstaskmanager.IAutomationTaskManagerTaskResponse {
// 	o.logger.Debug("Received new task", "task", newTaskCreatedLog)
// 	o.logger.Info("Received new task",
// 		"numberToBeSquared", newTaskCreatedLog.Task.NumberToBeSquared,
// 		"taskIndex", newTaskCreatedLog.TaskIndex,
// 		"taskCreatedBlock", newTaskCreatedLog.Task.TaskCreatedBlock,
// 		"quorumNumbers", newTaskCreatedLog.Task.QuorumNumbers,
// 		"QuorumThresholdPercentage", newTaskCreatedLog.Task.QuorumThresholdPercentage,
// 	)
// 	numberSquared := big.NewInt(0).Exp(newTaskCreatedLog.Task.NumberToBeSquared, big.NewInt(2), nil)
// 	taskResponse := &cstaskmanager.IAutomationTaskManagerTaskResponse{
// 		ReferenceTaskIndex: newTaskCreatedLog.TaskIndex,
// 		NumberSquared:      numberSquared,
// 	}
// 	return taskResponse
// }
//
// func (o *Operator) SignTaskResponse(taskResponse *cstaskmanager.IAutomationTaskManagerTaskResponse) (*aggregator.SignedTaskResponse, error) {
// 	taskResponseHash, err := core.GetTaskResponseDigest(taskResponse)
// 	if err != nil {
// 		o.logger.Error("Error getting task response header hash. skipping task (this is not expected and should be investigated)", "err", err)
// 		return nil, err
// 	}
// 	blsSignature := o.blsKeypair.SignMessage(taskResponseHash)
// 	signedTaskResponse := &aggregator.SignedTaskResponse{
// 		TaskResponse: *taskResponse,
// 		BlsSignature: *blsSignature,
// 		OperatorId:   o.operatorId,
// 	}
// 	o.logger.Debug("Signed task response", "signedTaskResponse", signedTaskResponse)
// 	return signedTaskResponse, nil
// }

package aggregator

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/AvaProtocol/ap-avs/aggregator/types"
	"github.com/AvaProtocol/ap-avs/core"
	"github.com/AvaProtocol/ap-avs/core/chainio"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients"
	sdkclients "github.com/Layr-Labs/eigensdk-go/chainio/clients"
	blsagg "github.com/Layr-Labs/eigensdk-go/services/bls_aggregation"
	sdktypes "github.com/Layr-Labs/eigensdk-go/types"

	"github.com/AvaProtocol/ap-avs/storage"

	cstaskmanager "github.com/AvaProtocol/ap-avs/contracts/bindings/AutomationTaskManager"
)

const (
	// number of blocks after which a task is considered expired
	// this hardcoded here because it's also hardcoded in the contracts, but should
	// ideally be fetched from the contracts
	taskChallengeWindowBlock = 100
	blockTimeSeconds         = 12 * time.Second
	avsName                  = "oak-avs"
)

func RunWithConfig(configPath string) error {
	nodeConfig, err := config.NewConfig(configPath)
	if err != nil {
		panic(fmt.Errorf("failed to parse config file: %w\nMake sure %s is exist and a valid yaml file %w.", configPath, err))
	}
	fmt.Printf("loaded config: %v\n", nodeConfig)

	aggregator, err := NewAggregator(nodeConfig)
	if err != nil {
		panic(fmt.Errorf("cannot initialize aggregrator from config: %w", err))
	}

	return aggregator.Start(context.Background())
}

// block number by calling the getOperatorState() function of the BLSOperatorStateRetriever.sol contract.
type Aggregator struct {
	logger    logging.Logger
	avsWriter chainio.AvsWriterer
	// aggregation related fields
	blsAggregationService blsagg.BlsAggregationService
	tasks                 map[types.TaskIndex]cstaskmanager.IAutomationTaskManagerTask
	tasksMu               sync.RWMutex
	taskResponses         map[types.TaskIndex]map[sdktypes.TaskResponseDigest]cstaskmanager.IAutomationTaskManagerTaskResponse
	taskResponsesMu       sync.RWMutex

	config *config.Config
	db     storage.Storage

	operatorPool *OperatorPool
}

// NewAggregator creates a new Aggregator with the provided config.
func NewAggregator(c *config.Config) (*Aggregator, error) {
	avsReader, err := chainio.BuildAvsReaderFromConfig(c)
	if err != nil {
		c.Logger.Error("Cannot create avsReader", "err", err)
		return nil, err
	}

	avsWriter, err := chainio.BuildAvsWriterFromConfig(c)
	if err != nil {
		c.Logger.Errorf("Cannot create avsWriter", "err", err)
		return nil, err
	}

	go func() {
		chainioConfig := sdkclients.BuildAllConfig{
			EthHttpUrl:                 c.EthHttpRpcUrl,
			EthWsUrl:                   c.EthWsRpcUrl,
			RegistryCoordinatorAddr:    c.AutomationRegistryCoordinatorAddr.String(),
			OperatorStateRetrieverAddr: c.OperatorStateRetrieverAddr.String(),
			AvsName:                    avsName,
			PromMetricsIpPortAddress:   ":9090",
		}
		clients, err := clients.BuildAll(chainioConfig, c.EcdsaPrivateKey, c.Logger)
		if err != nil {
			c.Logger.Errorf("Cannot create sdk clients", "err", err)
			panic(err)
			//return nil, err
		}

		fmt.Println("avsReader", avsReader, "clients", clients)

	}()

	// TODO: These are erroring out and we don't need them now yet
	// operatorPubkeysService := oppubkeysserv.NewOperatorPubkeysServiceInMemory(context.Background(), clients.AvsRegistryChainSubscriber, clients.AvsRegistryChainReader, c.Logger)
	//avsRegistryService := avsregistry.NewAvsRegistryServiceChainCaller(avsReader, operatorPubkeysService, c.Logger)
	// blsAggregationService := blsagg.NewBlsAggregatorService(avsRegistryService, c.Logger)

	return &Aggregator{
		logger:    c.Logger,
		avsWriter: avsWriter,

		//blsAggregationService: blsAggregationService,

		tasks:         make(map[types.TaskIndex]cstaskmanager.IAutomationTaskManagerTask),
		taskResponses: make(map[types.TaskIndex]map[sdktypes.TaskResponseDigest]cstaskmanager.IAutomationTaskManagerTaskResponse),

		config: c,

		operatorPool: &OperatorPool{},
	}, nil
}

// Open and setup our database
func (agg *Aggregator) initDB(ctx context.Context) error {
	var err error
	agg.db, err = storage.New(&storage.Config{
		Path: agg.config.DbPath,
	})

	if err != nil {
		panic(err)
	}

	agg.operatorPool.db = agg.db

	return agg.db.Setup()
}

func (agg *Aggregator) Start(ctx context.Context) error {
	agg.logger.Infof("Starting aggregator")

	agg.logger.Infof("Initialize Storagre")
	agg.initDB(ctx)

	agg.logger.Infof("Starting rpc server.")
	go agg.startRpcServer(ctx)

	agg.logger.Infof("Starting http server.")
	go agg.startHttpServer(ctx)

	// Setup wait signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan bool, 1)
	go func() {
		<-sigs
		done <- true
	}()

	<-done
	agg.logger.Infof("Shutting down.")

	// Shutdown the db
	// TODO: handle ongoing client and fanout closing
	agg.db.Close()

	return nil
}

func (agg *Aggregator) sendAggregatedResponseToContract(blsAggServiceResp blsagg.BlsAggregationServiceResponse) {
	// TODO: check if blsAggServiceResp contains an err
	if blsAggServiceResp.Err != nil {
		agg.logger.Error("BlsAggregationServiceResponse contains an error", "err", blsAggServiceResp.Err)
		// panicing to help with debugging (fail fast), but we shouldn't panic if we run this in production
		panic(blsAggServiceResp.Err)
	}
	nonSignerPubkeys := []cstaskmanager.BN254G1Point{}
	for _, nonSignerPubkey := range blsAggServiceResp.NonSignersPubkeysG1 {
		nonSignerPubkeys = append(nonSignerPubkeys, core.ConvertToBN254G1Point(nonSignerPubkey))
	}
	quorumApks := []cstaskmanager.BN254G1Point{}
	for _, quorumApk := range blsAggServiceResp.QuorumApksG1 {
		quorumApks = append(quorumApks, core.ConvertToBN254G1Point(quorumApk))
	}
	nonSignerStakesAndSignature := cstaskmanager.IBLSSignatureCheckerNonSignerStakesAndSignature{
		NonSignerPubkeys:             nonSignerPubkeys,
		QuorumApks:                   quorumApks,
		ApkG2:                        core.ConvertToBN254G2Point(blsAggServiceResp.SignersApkG2),
		Sigma:                        core.ConvertToBN254G1Point(blsAggServiceResp.SignersAggSigG1.G1Point),
		NonSignerQuorumBitmapIndices: blsAggServiceResp.NonSignerQuorumBitmapIndices,
		QuorumApkIndices:             blsAggServiceResp.QuorumApkIndices,
		TotalStakeIndices:            blsAggServiceResp.TotalStakeIndices,
		NonSignerStakeIndices:        blsAggServiceResp.NonSignerStakeIndices,
	}

	agg.logger.Info("Threshold reached. Sending aggregated response onchain.",
		"taskIndex", blsAggServiceResp.TaskIndex,
	)
	agg.tasksMu.RLock()
	task := agg.tasks[blsAggServiceResp.TaskIndex]
	agg.tasksMu.RUnlock()
	agg.taskResponsesMu.RLock()
	taskResponse := agg.taskResponses[blsAggServiceResp.TaskIndex][blsAggServiceResp.TaskResponseDigest]
	agg.taskResponsesMu.RUnlock()
	_, err := agg.avsWriter.SendAggregatedResponse(context.Background(), task, taskResponse, nonSignerStakesAndSignature)
	if err != nil {
		agg.logger.Error("Aggregator failed to respond to task", "err", err)
	}
}

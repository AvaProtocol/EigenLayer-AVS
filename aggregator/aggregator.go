package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"github.com/getsentry/sentry-go"
	"github.com/AvaProtocol/ap-avs/version" // Ensure version is imported


	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/ethclient"

	sdkclients "github.com/Layr-Labs/eigensdk-go/chainio/clients"
	blsagg "github.com/Layr-Labs/eigensdk-go/services/bls_aggregation"
	sdktypes "github.com/Layr-Labs/eigensdk-go/types"
	"github.com/allegro/bigcache/v3"

	cstaskmanager "github.com/AvaProtocol/EigenLayer-AVS/contracts/bindings/AutomationTaskManager"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/types"
	"github.com/AvaProtocol/EigenLayer-AVS/core"
	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/version"

	"github.com/AvaProtocol/EigenLayer-AVS/core/backup"
	"github.com/AvaProtocol/EigenLayer-AVS/core/migrator"
	"github.com/AvaProtocol/EigenLayer-AVS/migrations"
)

const (
	// number of blocks after which a task is considered expired
	// this hardcoded here because it's also hardcoded in the contracts, but should
	// ideally be fetched from the contracts
	taskChallengeWindowBlock = 100
	blockTimeSeconds         = 12 * time.Second
	avsName                  = "oak-avs"
)

type AggregatorStatus string

const (
	initStatus     AggregatorStatus = "init"
	runningStatus  AggregatorStatus = "running"
	shutdownStatus AggregatorStatus = "shutdown"
)

func RunWithConfig(configPath string) error {
	nodeConfig, err := config.NewConfig(configPath)
	if err != nil {
		panic(fmt.Errorf("Failed to parse config file: %s\nMake sure it is exist and a valid yaml file %w.", configPath, err))
	}

	aggregator, err := NewAggregator(nodeConfig)
	if err != nil {
		panic(fmt.Errorf("Cannot initialize aggregrator from config: %w", err))
	}

	return aggregator.Start(context.Background())
}

// block number by calling the getOperatorState() function of the BLSOperatorStateRetriever.sol contract.
type Aggregator struct {
	logger    logging.Logger
	avsWriter *chainio.AvsWriter

	// aggregation related fields
	blsAggregationService blsagg.BlsAggregationService
	tasks                 map[types.TaskIndex]cstaskmanager.IAutomationTaskManagerTask
	tasksMu               sync.RWMutex
	taskResponses         map[types.TaskIndex]map[sdktypes.TaskResponseDigest]cstaskmanager.IAutomationTaskManagerTaskResponse
	taskResponsesMu       sync.RWMutex

	config *config.Config
	db     storage.Storage

	ethRpcClient *ethclient.Client
	chainID      *big.Int

	operatorPool *OperatorPool

	// task engines handles trigger scheduling and send distribute checks to
	// operator to checks
	engine *taskengine.Engine
	// upon a task condition is met, taskengine will schedule it in our queue to
	// be executed.
	queue  *apqueue.Queue
	worker *apqueue.Worker

	status AggregatorStatus

	cache *bigcache.BigCache

	backup   *backup.Service
	migrator *migrator.Migrator
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
		clients, err := sdkclients.BuildAll(chainioConfig, c.EcdsaPrivateKey, c.Logger)
		if err != nil {
			c.Logger.Error("Cannot create sdk clients", "err", err)
			panic(err)
		}
		c.Logger.Info("create avsrrader and client", "avsReader", avsReader, "clients", clients)
	}()

	// TODO: These are erroring out and we don't need them now yet
	// operatorPubkeysService := oppubkeysserv.NewOperatorPubkeysServiceInMemory(context.Background(), clients.AvsRegistryChainSubscriber, clients.AvsRegistryChainReader, c.Logger)
	//avsRegistryService := avsregistry.NewAvsRegistryServiceChainCaller(avsReader, operatorPubkeysService, c.Logger)
	// blsAggregationService := blsagg.NewBlsAggregatorService(avsRegistryService, c.Logger)

	cache, err := bigcache.New(context.Background(), bigcache.Config{
		// number of shards (must be a power of 2)
		Shards: 1024,

		// time after which entry can be evicted
		LifeWindow: 120 * time.Minute,

		// Interval between removing expired entries (clean up).
		// If set to <= 0 then no action is performed.
		// Setting to < 1 second is counterproductive — bigcache has a one second resolution.
		CleanWindow: 5 * time.Minute,

		// rps * lifeWindow, used only in initial memory allocation
		MaxEntriesInWindow: 1000 * 10 * 60,

		// max entry size in bytes, used only in initial memory allocation
		MaxEntrySize: 500,

		// prints information about additional memory allocation
		Verbose: true,

		// cache will not allocate more memory than this limit, value in MB
		// if value is reached then the oldest entries can be overridden for the new ones
		// 0 value means no size limit
		HardMaxCacheSize: 8192,

		// callback fired when the oldest entry is removed because of its expiration time or no space left
		// for the new entry, or because delete was called. A bitmask representing the reason will be returned.
		// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
		OnRemove: nil,

		// OnRemoveWithReason is a callback fired when the oldest entry is removed because of its expiration time or no space left
		// for the new entry, or because delete was called. A constant representing the reason will be passed through.
		// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
		// Ignored if OnRemove is specified.
		OnRemoveWithReason: nil,
	})
	if err != nil {
		panic("cannot initialize cache storage")
	}

	return &Aggregator{
		logger:    c.Logger,
		avsWriter: avsWriter,

		//blsAggregationService: blsAggregationService,

		tasks:         make(map[types.TaskIndex]cstaskmanager.IAutomationTaskManagerTask),
		taskResponses: make(map[types.TaskIndex]map[sdktypes.TaskResponseDigest]cstaskmanager.IAutomationTaskManagerTaskResponse),

		config: c,

		operatorPool: &OperatorPool{},
		status:       initStatus,

		cache: cache,
	}, nil
}

// Open and setup our database
func (agg *Aggregator) initDB(ctx context.Context) error {
	var err error
	agg.db, err = storage.NewWithPath(agg.config.DbPath)

	if err != nil {
		panic(err)
	}

	agg.operatorPool.db = agg.db

	return agg.db.Setup()
}

// initialize agg config and state
func (agg *Aggregator) init() {
	var err error

	agg.ethRpcClient, err = ethclient.Dial(agg.config.EthHttpRpcUrl)
	if err != nil {
		panic(err)
	}

	agg.chainID, err = agg.ethRpcClient.ChainID(context.Background())
	if err != nil {
		panic(err)
	}

	if agg.chainID.Cmp(config.MainnetChainID) == 0 {
		config.CurrentChainEnv = config.EthereumEnv
	} else {
		config.CurrentChainEnv = config.HoleskyEnv
	agg.initSentry() // Initialize Sentry

	}

	// Setup account abstraction config
	aa.SetFactoryAddress(agg.config.SmartWallet.FactoryAddress)
	aa.SetEntrypointAddress(agg.config.SmartWallet.EntrypointAddress)
}

func (agg *Aggregator) migrate() {
	agg.backup = backup.NewService(agg.logger, agg.db, agg.config.BackupDir)
	agg.migrator = migrator.NewMigrator(agg.db, agg.backup, migrations.Migrations)
	if err := agg.migrator.Run(); err != nil {
		agg.logger.Fatalf("failed to run migrations", "error", err)
	}
}

func (agg *Aggregator) Start(ctx context.Context) error {
	agg.logger.Infof("Starting aggregator %s", version.Get())

	agg.init()

	agg.logger.Infof("Initialize Storage")
	if err := agg.initDB(ctx); err != nil {
		agg.logger.Fatalf("failed to initialize storage", "error", err)
	}

	agg.migrate()

	agg.logger.Infof("Starting Task engine")
	agg.startTaskEngine(ctx)

	agg.logger.Infof("Starting rpc server")
	agg.startRpcServer(ctx)

	agg.logger.Info("Starting repl")
	agg.startRepl()

	agg.logger.Infof("Starting http server")
	agg.startHttpServer(ctx)
	agg.status = runningStatus

	// Setup wait signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan bool, 1)
	go func() {
		<-sigs
		done <- true
	}()

	<-done
	agg.logger.Infof("Shutting down...")

	// TODO: handle ongoing client and fanout closing
	agg.status = shutdownStatus
	agg.stopRepl()
	agg.stopTaskEngine()

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

func (agg *Aggregator) IsShutdown() bool {
	return agg.status == shutdownStatus
}
package aggregator

import (
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/AvaProtocol/ap-avs/version"
)


func (agg *Aggregator) initSentry() {
	sentryDSN := os.Getenv("SENTRY_DSN")
	if sentryDSN == "" {
		agg.logger.Info("SENTRY_DSN not found, Sentry integration is disabled.")
		return
	}

	sentryEnv := os.Getenv("SENTRY_ENVIRONMENT")
	if sentryEnv == "" {
		sentryEnv = string(agg.config.Environment)
		agg.logger.Infof("SENTRY_ENVIRONMENT not set, falling back to config environment: %s", sentryEnv)
	}


	err := sentry.Init(sentry.ClientOptions{
		Dsn:              sentryDSN,
		Release:          version.Get() + "@" + version.Commit(),
		Environment:      sentryEnv,
		TracesSampleRate: 1.0, // Configure sample rate as needed
	})
	if err != nil {
		agg.logger.Errorf("Sentry initialization failed: %v", err)
		return
	}
	agg.logger.Infof("Sentry initialized successfully for environment: %s", sentryEnv)

}

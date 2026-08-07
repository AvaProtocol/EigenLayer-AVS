package worker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/ethclient"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type Worker struct {
	config         *WorkerConfig
	smartWalletCfg *config.SmartWalletConfig
	rpcClient      *ethclient.Client
	wsClient       *ethclient.Client
	tokenService   *taskengine.TokenEnrichmentService
	grpcServer     *grpc.Server
	logger         sdklogging.Logger
}

func RunWithConfig(configPath string) error {
	cfg, err := NewWorkerConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading worker config: %w", err)
	}

	w, err := New(cfg)
	if err != nil {
		return fmt.Errorf("creating worker: %w", err)
	}

	return w.Start(context.Background())
}

func New(cfg *WorkerConfig) (*Worker, error) {
	// Stack traces at Error, never at Warn.
	//
	// sdklogging.NewZapLogger builds zap's DEVELOPMENT config outside
	// production, and zap attaches a stack trace to every Warn when
	// Development is set. So a boot notice that is merely informative — a
	// worker running unsponsored, say — prints a dozen frames and reads like a
	// crash, which is exactly how it was read. The gateway already pins this
	// to Error (core/config/config.go); the worker had been left on the
	// default because nothing warned at boot until it did.
	//
	// AddCallerSkip(1) is what NewZapLogger passes, and is kept so log lines
	// still name their real call site rather than this constructor.
	zapConfig := zap.NewDevelopmentConfig()
	if cfg.Environment == "production" {
		zapConfig = zap.NewProductionConfig()
	}
	logger, err := sdklogging.NewZapLoggerByConfig(zapConfig,
		zap.AddCallerSkip(1), zap.AddStacktrace(zap.ErrorLevel))
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}

	smartWalletCfg, err := cfg.ToSmartWalletConfig()
	if err != nil {
		return nil, fmt.Errorf("building smart wallet config: %w", err)
	}

	return &Worker{
		config:         cfg,
		smartWalletCfg: smartWalletCfg,
		logger:         logger,
	}, nil
}

func (w *Worker) Start(ctx context.Context) error {
	// Read sponsorship off the SmartWalletConfig the send path will actually
	// use — w.smartWalletCfg, built once in NewWorker — rather than deriving a
	// second answer from the raw config. Two sources for one question is the
	// shape of the bug this change exists to fix, and it would also re-parse
	// the controller key on every call.
	sponsorshipPolicy := w.smartWalletCfg.SponsorshipPolicyID()

	w.logger.Info("Starting chain worker",
		"chain_id", w.config.ChainID,
		"chain_name", w.config.ChainName,
		"listen_address", w.config.ListenAddress,
		"health_address", w.config.HealthAddress,
		"sponsorship_configured", sponsorshipPolicy != "",
	)

	// Say which of the three states this worker is in, once, at boot — rather
	// than letting it surface later as a user's withdrawal failing.
	switch {
	case w.smartWalletCfg.DisableGasSponsorship:
		w.logger.Info("Chain worker runs self-funded: sponsorship disabled by config",
			"chain_id", w.config.ChainID, "chain_name", w.config.ChainName,
			"hint", "expected for local/development — the policy's webhook points at the production gateway")
	case sponsorshipPolicy == "":
		w.logger.Warn("Chain worker will send unsponsored operations",
			"chain_id", w.config.ChainID, "chain_name", w.config.ChainName,
			"bundler_provider", w.smartWalletCfg.ProviderName(),
			"hint", "needs alchemy_paymaster_policy_id, gas_manager_webhook_secret, and bundler_provider: alchemy")
	}

	// Connect to chain RPC
	var err error
	w.rpcClient, err = ethclient.Dial(w.config.EthRpcUrl)
	if err != nil {
		return fmt.Errorf("connecting to RPC %s: %w", w.config.EthRpcUrl, err)
	}

	if w.config.EthWsUrl != "" {
		w.wsClient, err = ethclient.Dial(w.config.EthWsUrl)
		if err != nil {
			w.logger.Warn("Failed to connect to WebSocket RPC, will fall back to polling",
				"ws_url", w.config.EthWsUrl,
				"error", err,
			)
		}
	}

	// Initialize token enrichment service for this chain
	w.tokenService, err = taskengine.NewTokenEnrichmentService(w.rpcClient, w.logger)
	if err != nil {
		w.logger.Warn("Failed to initialize token enrichment service", "error", err)
	}

	// Start HTTP health endpoint
	go w.startHealthServer()

	// Start gRPC server
	lis, err := net.Listen("tcp", w.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", w.config.ListenAddress, err)
	}

	w.grpcServer = grpc.NewServer()
	avsproto.RegisterChainWorkerServer(w.grpcServer, &Server{
		worker: w,
	})

	w.logger.Info("Chain worker gRPC server listening",
		"address", w.config.ListenAddress,
	)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		w.logger.Info("Shutting down chain worker")
		w.grpcServer.GracefulStop()
	}()

	return w.grpcServer.Serve(lis)
}

func (w *Worker) startHealthServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		fmt.Fprintf(rw, `{"status":"OK","chain_id":%d,"chain_name":"%s"}`,
			w.config.ChainID, w.config.ChainName)
	})

	server := &http.Server{
		Addr:    w.config.HealthAddress,
		Handler: mux,
	}

	w.logger.Info("Health endpoint listening",
		"address", w.config.HealthAddress,
	)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		w.logger.Error("Health server error", "error", err)
	}
}

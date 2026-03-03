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
	logLevel := sdklogging.Development
	if cfg.Environment == "production" {
		logLevel = sdklogging.Production
	}
	logger, err := sdklogging.NewZapLogger(logLevel)
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
	w.logger.Info("Starting chain worker",
		"chain_id", w.config.ChainID,
		"chain_name", w.config.ChainName,
		"listen_address", w.config.ListenAddress,
		"health_address", w.config.HealthAddress,
	)

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

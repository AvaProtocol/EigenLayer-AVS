package cmd

import (
	"fmt"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/worker"
	"github.com/spf13/cobra"
)

var (
	runWorkerCmd = &cobra.Command{
		Use:   "worker",
		Short: "Run chain worker process",
		Long: `Start a chain worker that handles chain-specific operations for a single chain.

The worker connects to the chain RPC and bundler, and exposes a gRPC service
for the gateway to delegate operations like UserOp execution, nonce queries,
and smart wallet address derivation.

Use --config=path-to-your-config-file to specify the worker config.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := worker.RunWithConfig(config); err != nil {
				fmt.Fprintf(os.Stderr, "worker failed: %v\n", err)
				os.Exit(1)
			}
		},
	}
)

func init() {
	runWorkerCmd.Flags().StringVar(&config, "config", "./config/worker.yaml", "path to worker config file")
	rootCmd.AddCommand(runWorkerCmd)
}

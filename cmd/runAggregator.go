package cmd

import (
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator"

	"github.com/spf13/cobra"
)

var (
	runAggregatorCmd = &cobra.Command{
		Use:   "aggregator",
		Short: "Run aggregator",
		Long: `Initialize and run aggregator.

Use --config=path-to-your-config-file. default is=./config/aggregator.yaml `,
		Run: func(cmd *cobra.Command, args []string) {
			aggregator.RunWithConfig(config)
		},
	}
)

func init() {
	runAggregatorCmd.Flags().StringVar(&config, "config", "./config/aggregator.yaml", "path to aggregator config file")
	rootCmd.AddCommand(runAggregatorCmd)
}

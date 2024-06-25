package cmd

import (
	"github.com/AvaProtocol/ap-avs/aggregator"

	"github.com/spf13/cobra"
)

var (
	aggregratorConfig = "./config/aggregator.yaml"

	runAggregatorCmd = &cobra.Command{
		Use:   "aggregator",
		Short: "Run aggregator",
		Long: `Initialize and run aggregator.

Use --config=path-to-your-config-file. default is=./config/aggregator.yaml `,
		Run: func(cmd *cobra.Command, args []string) {
			aggregator.RunWithConfig(aggregratorConfig)
		},
	}
)

func init() {
	registerCmd.Flags().StringVar(&config, "config", "./config/aggregator.yaml", "path to aggregrator config file")
	rootCmd.AddCommand(runAggregatorCmd)
}

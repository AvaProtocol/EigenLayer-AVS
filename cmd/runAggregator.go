/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/OAK-Foundation/avs-mvp/aggregator"
)

var (
	config = "./config/aggregator.yaml"

	runAggregatorCmd = &cobra.Command{
		Use:   "run-aggregator",
		Short: "Run aggregator",
		Long: `Initialize and run aggregator.

Use --config=path-to-your-config-file. default is=./config/aggregator.yaml `,
		Run: func(cmd *cobra.Command, args []string) {
			aggregator.RunWithConfig(config)
		},
	}
)

func init() {
	registerCmd.Flags().StringVar(&config, "config", "config/aggregator.yaml", "path to aggregrator config file")
	rootCmd.AddCommand(runAggregatorCmd)
}

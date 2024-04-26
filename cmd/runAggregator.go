/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/OAK-Foundation/oak-avs/aggregator"
)

// runAggregatorCmd represents the runAggregator command
var runAggregatorCmd = &cobra.Command{
	Use:   "runAggregator",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("runAggregator called")

		aggregator.Run(args)
	},
}

func init() {
	rootCmd.AddCommand(runAggregatorCmd)
}

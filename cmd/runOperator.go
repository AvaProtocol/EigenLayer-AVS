/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/EigenLayer-AVS/operator"
)

var (
	runOperatorCmd = &cobra.Command{
		Use:   "operator",
		Short: "start operator process",
		Long: `use --debug to dump more log.
Make sure to run "operator register" before starting up operator`,
		Run: func(cmd *cobra.Command, args []string) {
			operator.RunWithConfig(config)
		},
	}
)

func init() {
	rootCmd.AddCommand(runOperatorCmd)
}

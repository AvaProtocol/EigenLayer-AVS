/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/ap-avs/operator"
)

var (
	statusCmd = &cobra.Command{
		Use:   "status",
		Short: "report operator status",
		Long:  `Report status of your operator with OAK AVS`,
		Run: func(cmd *cobra.Command, args []string) {
			operator.Status(config)
		},
	}
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

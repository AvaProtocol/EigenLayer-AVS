/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/OAK-Foundation/oak-avs/operator"
)

var (
	runOperatorCmd = &cobra.Command{
		Use:   "operator",
		Short: "start operator process",
		Long: `use --debug to dump more log.
Make sure to run "operator register" before starting up operator`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("run oak-avs-operator with config", config)
			operator.RunWithConfig(config)
		},
	}
)

func init() {
	rootCmd.AddCommand(runOperatorCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// runOperatorCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// runOperatorCmd.Flags().StringVar(&ecdsaKeyPass, "ecdsa-key-password", "", "ECDSA pass")
	// runOperatorCmd.Flags().StringVar(&blsKeyPass, "bls-key-password", "", "ECDSA pass")
	// runOperatorCmd.Flags().StringVar(&registerAction, "register-action", "opt-in", "Action to perform such as opt-in, opt-out, status")
	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// runOperatorCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// runOperatorCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

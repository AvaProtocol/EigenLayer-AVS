/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// flag for register command
	ecdsaKeyPass   string
	blsKeyPass     string
	registerAction string

	registerCmd = &cobra.Command{
		Use:   "register",
		Short: "Register your operator with Oak AVS",
		Long: `The registration requires that an operator already register on Eigenlayer
	and to have a minimun amount of staked delegated`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Call Register")
		},
	}
)

func init() {
	rootCmd.AddCommand(registerCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// registerCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	registerCmd.Flags().StringVar(&ecdsaKeyPass, "ecdsa-key-password", "", "ECDSA pass")
	registerCmd.Flags().StringVar(&blsKeyPass, "bls-key-password", "", "ECDSA pass")
	registerCmd.Flags().StringVar(&registerAction, "register-action", "opt-in", "Action to perform such as opt-in, opt-out, status")
}

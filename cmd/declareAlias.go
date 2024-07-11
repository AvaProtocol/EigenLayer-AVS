/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/ap-avs/operator"
)

// declareAliasCmd represents the declareAlias command
var declareAliasCmd = &cobra.Command{
	Use:   "declare-alias",
	Short: "Declare an alias ecdsa key file for the operator address",
	Long: `Declare an alias ecdsa key file for the operator address

After creating an alias key, they key can be declare as
an alias for the operator address`,
	Run: func(cmd *cobra.Command, args []string) {
		operator.DeclareAlias(config, aliasKeyOption.Filename)
	},
}

func init() {
	rootCmd.AddCommand(declareAliasCmd)

	declareAliasCmd.Flags().StringVarP(&(aliasKeyOption.Filename), "name", "n", "alias-ecdsa.key.json", "absolute or relative file path to alias ECDSA key to declare alias")
	declareAliasCmd.MarkPersistentFlagRequired("name")
}

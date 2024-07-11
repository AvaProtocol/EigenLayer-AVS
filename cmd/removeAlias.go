/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/ap-avs/operator"
)

// removeAliasCmd represents the removeAlias command
var removeAliasCmd = &cobra.Command{
	Use:   "remove-alias",
	Short: "Unbind alias address from your operator",
	Long: `Unbind alias key from your operator address

After removal, you will either need to setup another alias key, or to use your operator ECDSA key.

When removing alias, you can run it with alias key
`,
	Run: func(cmd *cobra.Command, args []string) {
		operator.RemoveAlias(config)
	},
}

func init() {
	rootCmd.AddCommand(removeAliasCmd)
}

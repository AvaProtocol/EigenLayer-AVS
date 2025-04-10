/*
Copyright © 2024 Ava Protocol
*/
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/EigenLayer-AVS/operator"
)

var (
	aliasKeyOption = operator.CreateAliasKeyOption{}
)

// createAliasKeyCmd represents the createAliasKey command
var createAliasKeyCmd = &cobra.Command{
	Use:   "create-alias-key",
	Short: "Create an ECDSA private key only for AP AVS operation",
	Long: `Generate an ECDSA private key to use for AP AVS operation.

Instead of using the operator's ECDSA private key to interact with
Ava Protocol AVS, you can generate an alias key and use this key to
interact with Ava Protocol operator. You will still need the EigenLayer
Operator ECDSA key to register or deregister from the AVS. But once
you registered, you don't need that operator key anymore`,
	Run: func(cmd *cobra.Command, args []string) {
		operator.CreateOrImportAliasKey(aliasKeyOption)
	},
}

func init() {
	rootCmd.AddCommand(createAliasKeyCmd)

	createAliasKeyCmd.Flags().StringVarP(&(aliasKeyOption.PrivateKey), "ecdsa-private-key", "k", "", "a private key start with 0x to import as alias key")

	createAliasKeyCmd.Flags().StringVarP(&(aliasKeyOption.Filename), "name", "n", "alias-ecdsa.key.json", "absolute or relative file path to save your ECDSA key to")
	createAliasKeyCmd.MarkPersistentFlagRequired("name")
}

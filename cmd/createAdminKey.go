package cmd

import (
	"fmt"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator"

	"github.com/spf13/cobra"
)

var (
	apiKeyOption = aggregator.CreateApiKeyOption{}
	createApiKey = &cobra.Command{
		Use:   "create-api-key",
		Short: "Create a long live JWT key to interact with userdata of AVS",
		Long: `Create a JWT key that allows one to manage user tasks. This key cannot control operator aspect, only user storage such as tasks management.

The --subject flag must be a 0x-prefixed EOA address. The auth layer treats the JWT subject as the owner address and derives a smart wallet from it (see aggregator/auth.go), so any non-address subject will fail authentication.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := aggregator.CreateAdminKey(config, apiKeyOption); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
	}
)

func init() {
	createApiKey.Flags().StringArrayVar(&(apiKeyOption.Roles), "role", []string{}, "Role for API Key")
	createApiKey.Flags().StringVarP(&(apiKeyOption.Subject), "subject", "s", "", "owner EOA address (0x...) bound to this API key; required")
	rootCmd.AddCommand(createApiKey)
}

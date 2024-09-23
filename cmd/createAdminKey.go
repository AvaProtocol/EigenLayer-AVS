package cmd

import (
	"github.com/AvaProtocol/ap-avs/aggregator"

	"github.com/spf13/cobra"
)

var (
	apiKeyOption = aggregator.CreateApiKeyOption{}
	createApiKey = &cobra.Command{
		Use:   "create-api-key",
		Short: "Create a long live JWT key to interact with userdata of AVS",
		Long:  `Create an JWT key that allow one to manage user tasks. This key cannot control operator aspect, only user storage such as tasks management`,
		Run: func(cmd *cobra.Command, args []string) {
			aggregator.CreateAdminKey(config, apiKeyOption)
		},
	}
)

func init() {
	createApiKey.Flags().StringArrayVar(&(apiKeyOption.Roles), "role", []string{}, "Role for API Key")
	createApiKey.Flags().StringVarP(&(apiKeyOption.Subject), "subject", "s", "admin", "subject name to be use for jwt api key")
	rootCmd.AddCommand(createApiKey)
}

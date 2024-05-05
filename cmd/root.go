package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var (
	config  = "./config/operator.yaml"
	rootCmd = &cobra.Command{
		Use:   "avs-mvp",
		Short: "OAK AVS CLI",
		Long: `OAK CLI to run and interact with EigenLayer service.
Each sub command can be use for a single service

Such as "avs-mvp run-operator" or "avs-mvp run-aggregrator" and so on 
`,
	}
)

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolP("analytic", "t", false, "send back telemetry to Oak")
	rootCmd.PersistentFlags().StringVarP(&config, "config", "c", "config/operator.yaml", "Path to config file")
}

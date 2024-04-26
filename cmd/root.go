package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "avs-mvp",
	Short: "OAK AVS CLI",
	Long: `OAK CLI to run and interact with EigenLayer service.
Each sub command can be use for a single service

Such as "avs-mvp run-operator" or "avs-mvp run-aggregrator" and so on 
`,
	// We may consider a default command to run operator as well
	// Run: func(cmd *cobra.Command, args []string) { },
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Our global cli flag
	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.evm-automation.yaml)")

	// enable analytic to send back telemetry
	rootCmd.Flags().BoolP("analytic", "t", false, "send back telemetry to Oak")
}

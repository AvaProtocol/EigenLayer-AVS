package cmd

import (
	"github.com/spf13/cobra"

	"github.com/AvaProtocol/EigenLayer-AVS/operator"
)

var (
	registerCmd = &cobra.Command{
		Use:   "register",
		Short: "Register your operator with Oak AVS",
		Long: `The registration requires that an operator already register on Eigenlayer
	and to have a minimun amount of staked delegated`,
		Run: func(cmd *cobra.Command, args []string) {
			operator.RegisterToAVS(config)
		},
	}
)

func init() {
	rootCmd.AddCommand(registerCmd)
}

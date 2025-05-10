/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/AvaProtocol/EigenLayer-AVS/version"
)

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "get version",
	Long:  `get version of the binary`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s\n", version.Get())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

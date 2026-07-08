package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var Version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "rebuno",
		Short: "Rebuno — kernel-authoritative execution runtime for AI agents",
	}
	root.AddCommand(versionCmd(), serverCmd(), devCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("rebuno " + Version)
		},
	}
}

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var Version = "dev"

var (
	baseURL string
	apiKey  string
)

func main() {
	root := &cobra.Command{
		Use:   "rebuno",
		Short: "Rebuno — kernel-authoritative execution runtime for AI agents",
	}

	defaultURL := os.Getenv("REBUNO_KERNEL_URL")
	if defaultURL == "" {
		defaultURL = "http://localhost:8080"
	}
	root.PersistentFlags().StringVar(&baseURL, "url", defaultURL, "Kernel HTTP URL")
	root.PersistentFlags().StringVar(&apiKey, "api-key", os.Getenv("REBUNO_API_KEY"), "Bearer token for auth")

	root.AddCommand(
		versionCmd(),
		serverCmd(),
		devCmd(),
	)
	addInspectCommands(root)

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

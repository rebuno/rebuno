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
		Short: "Execution runtime for AI agents",
	}
	root.AddCommand(versionCmd(), serverCmd(), devCmd(),
		bindKernelURL(policyCmd()), bindKernelURL(agentCmd()),
		bindKernelURL(execCmd()), bindKernelURL(approvalCmd()))
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func bindKernelURL(cmd *cobra.Command) *cobra.Command {
	cmd.PersistentFlags().StringVar(&kernelBaseURL, "url", kernelURL(), "Kernel base URL ($REBUNO_URL)")
	return cmd
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

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
)

func agentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Inspect and manage registered agents",
	}
	cmd.AddCommand(agentListCmd(), agentGetCmd(), agentAddCmd(), agentRemoveCmd())
	return cmd
}

func agentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "ls",
		Aliases:      []string{"list"},
		Short:        "List registered agents",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var agents []domain.Agent
			if err := kernelClient().do(cmd.Context(), http.MethodGet, "/v0/agents", nil, &agents); err != nil {
				return err
			}
			if len(agents) == 0 {
				fmt.Println("  no agents registered; use 'rebuno agent add <config.yaml>'")
				return nil
			}
			fmt.Printf("  %-20s %-34s %s\n", "ID", "WEBHOOK", "POLICY")
			for _, a := range agents {
				policy := "none"
				if a.PolicyBundle != "" {
					policy = fmt.Sprintf("%d bytes", len(a.PolicyBundle))
				}
				fmt.Printf("  %-20s %-34s %s\n", a.ID, a.WebhookURL, policy)
			}
			return nil
		},
	}
}

func agentGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "get <id>",
		Short:        "Show an agent and its policy bundle",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var a domain.Agent
			if err := kernelClient().do(cmd.Context(), http.MethodGet, "/v0/agents/"+url.PathEscape(args[0]), nil, &a); err != nil {
				return err
			}
			fmt.Printf("  id          %s\n", a.ID)
			fmt.Printf("  webhook     %s\n", a.WebhookURL)
			fmt.Printf("  registered  %s\n", a.RegisteredAt.Format(time.RFC3339))
			if a.PolicyBundle == "" {
				fmt.Println("  policy      none (permissive)")
				return nil
			}
			fmt.Printf("  policy      (%d bytes)\n", len(a.PolicyBundle))
			for _, line := range strings.Split(strings.TrimRight(a.PolicyBundle, "\n"), "\n") {
				fmt.Printf("    │ %s\n", line)
			}
			return nil
		},
	}
}

func agentAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "add <config.yaml>",
		Short:        "Register agents from a provisioning manifest",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			agents, err := loadAgentConfig(args[0])
			if err != nil {
				return err
			}
			c := kernelClient()
			for _, a := range agents {
				if err := registerAgentOverHTTP(cmd.Context(), c, a); err != nil {
					return fmt.Errorf("register agent %q: %w", a.ID, err)
				}
				fmt.Printf("  registered %s\n", a.ID)
			}
			return nil
		},
	}
}

func registerAgentOverHTTP(ctx context.Context, c *client, a domain.Agent) error {
	req := api.AgentRegistrationRequest{ID: a.ID, WebhookURL: a.WebhookURL, Secret: a.Secret}
	if err := c.do(ctx, http.MethodPost, "/v0/agents", req, nil); err != nil {
		return err
	}
	if a.PolicyBundle == "" {
		return nil
	}
	return c.do(ctx, http.MethodPost, "/v0/policies/"+url.PathEscape(a.ID), api.LoadPolicyRequest{Bundle: a.PolicyBundle}, nil)
}

func agentRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "rm <id>",
		Aliases:      []string{"delete"},
		Short:        "Delete an agent",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := kernelClient().do(cmd.Context(), http.MethodDelete, "/v0/agents/"+url.PathEscape(args[0]), nil, nil); err != nil {
				return err
			}
			fmt.Printf("  deleted %s\n", args[0])
			return nil
		},
	}
}

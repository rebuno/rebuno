package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func approvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approval",
		Short: "Review steps waiting on a human decision",
	}
	cmd.AddCommand(approvalListCmd(), approvalGetCmd(), approvalDecideCmd("grant"), approvalDecideCmd("deny"))
	return cmd
}

func approvalListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "ls",
		Aliases:      []string{"list"},
		Short:        "List approvals still pending",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var approvals []domain.Approval
			if err := kernelClient().do(cmd.Context(), http.MethodGet, "/v0/approvals", nil, &approvals); err != nil {
				return err
			}
			if len(approvals) == 0 {
				fmt.Println("  no approvals pending")
				return nil
			}
			fmt.Printf("  %-*s %-*s %-30s %s\n", shortIDLen, "ID", shortIDLen, "EXECUTION", "STEP", "EXPIRES")
			for _, a := range approvals {
				fmt.Printf("  %-*s %-*s %-30s %s\n", shortIDLen, shortID(a.ID), shortIDLen, shortID(a.ExecutionID), truncate(a.StepID, 30), remaining(a.TimeoutAt))
			}
			return nil
		},
	}
}

func approvalGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "get <id>",
		Short:        "Show one approval",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := kernelClient()
			id, err := resolveApprovalID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var a domain.Approval
			if err := c.do(cmd.Context(), http.MethodGet, "/v0/approvals/"+id.String(), nil, &a); err != nil {
				return err
			}
			fmt.Printf("  id         %s\n", a.ID)
			fmt.Printf("  execution  %s\n", a.ExecutionID)
			fmt.Printf("  step       %s\n", a.StepID)
			fmt.Printf("  status     %s\n", a.Status)
			fmt.Printf("  timeout    %s (%s)\n", a.TimeoutAt.Format(time.RFC3339), remaining(a.TimeoutAt))
			if a.Message != "" {
				fmt.Printf("  message    %s\n", a.Message)
			}
			if len(a.Approvers) > 0 && string(a.Approvers) != "null" {
				fmt.Printf("  approvers  %s\n", oneLine(a.Approvers, 200))
			}
			if a.DecidedBy != "" {
				fmt.Printf("  decided    %s by %s\n", a.Status, a.DecidedBy)
			}
			if a.Rationale != "" {
				fmt.Printf("  rationale  %s\n", a.Rationale)
			}
			return nil
		},
	}
}

func approvalDecideCmd(decision string) *cobra.Command {
	short, past := "Grant a pending approval", "granted"
	if decision == "deny" {
		short, past = "Deny a pending approval", "denied"
	}
	var by, reason string
	cmd := &cobra.Command{
		Use:          decision + " <id>",
		Short:        short,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if by == "" {
				by = os.Getenv("USER")
			}
			if by == "" {
				return fmt.Errorf("--by is required when $USER is unset")
			}
			c := kernelClient()
			id, err := resolveApprovalID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var req any = kernel.GrantApprovalRequest{DecidedBy: by, Rationale: reason}
			if decision == "deny" {
				req = kernel.DenyApprovalRequest{DecidedBy: by, Rationale: reason}
			}
			if err := c.do(cmd.Context(), http.MethodPost, "/v0/approvals/"+id.String()+"/"+decision, req, nil); err != nil {
				return err
			}
			fmt.Printf("  %s %s by %s\n", past, shortID(id), by)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&by, "by", "", "Who is deciding (default $USER)")
	f.StringVar(&reason, "reason", "", "Rationale recorded with the decision")
	return cmd
}

func resolveApprovalID(cmd *cobra.Command, c *client, arg string) (uuid.UUID, error) {
	if id, err := uuid.Parse(arg); err == nil {
		return id, nil
	}
	var approvals []domain.Approval
	if err := c.do(cmd.Context(), http.MethodGet, "/v0/approvals", nil, &approvals); err != nil {
		return uuid.Nil, err
	}
	var matches []uuid.UUID
	for _, a := range approvals {
		if strings.HasPrefix(a.ID.String(), arg) {
			matches = append(matches, a.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return uuid.Nil, fmt.Errorf("no pending approval matching %q", arg)
	default:
		return uuid.Nil, fmt.Errorf("%q is ambiguous (%d matches)", arg, len(matches))
	}
}

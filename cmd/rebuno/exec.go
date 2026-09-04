package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

const watchPollInterval = 300 * time.Millisecond

func execCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Create and inspect executions",
	}
	cmd.AddCommand(execListCmd(), execCreateCmd(), execGetCmd(), execWatchCmd(), execEventsCmd(), execCancelCmd())
	return cmd
}

func execListCmd() *cobra.Command {
	var agentID, status string
	var limit int
	cmd := &cobra.Command{
		Use:          "ls",
		Aliases:      []string{"list"},
		Short:        "List executions, newest first",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if agentID != "" {
				q.Set("agent_id", agentID)
			}
			if status != "" {
				q.Set("status", status)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			path := "/v0/executions"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page domain.ExecutionPage
			if err := kernelClient().do(cmd.Context(), http.MethodGet, path, nil, &page); err != nil {
				return err
			}
			if len(page.Executions) == 0 {
				fmt.Println("  no executions; use 'rebuno exec create <agent> [json]'")
				return nil
			}
			fmt.Printf("  %-*s %-20s %-11s %s\n", shortIDLen, "ID", "AGENT", "STATUS", "AGE")
			for _, e := range page.Executions {
				fmt.Printf("  %-*s %-20s %-11s %s\n", shortIDLen, shortID(e.ID), e.AgentID, e.Status, age(e.CreatedAt))
			}
			if page.NextCursor != "" {
				fmt.Println("  … only the most recent page is shown")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&agentID, "agent", "", "Only executions for this agent")
	f.StringVar(&status, "status", "", "Only executions in this status (pending, running, blocked, completed, failed, cancelled)")
	f.IntVar(&limit, "limit", 0, "Maximum executions to list")
	return cmd
}

func execCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <agent-id> [json-input]",
		Short: "Start an execution",
		Long: "Start an execution against a registered agent. The input defaults to {}\n" +
			"and must be valid JSON; quote it so your shell keeps it in one piece.",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Unquoted JSON arrives shell-split; rejoin so the check sees it whole.
			input := strings.TrimSpace(strings.Join(args[1:], " "))
			if input == "" {
				input = "{}"
			}
			if !json.Valid([]byte(input)) {
				return fmt.Errorf("input is not valid JSON: %s", input)
			}
			req := api.CreateExecutionRequest{AgentID: args[0], Input: json.RawMessage(input)}
			var exec domain.Execution
			if err := kernelClient().do(cmd.Context(), http.MethodPost, "/v0/executions", req, &exec); err != nil {
				return err
			}
			fmt.Printf("  created %s (%s); follow with 'rebuno exec watch %s'\n", shortID(exec.ID), exec.Status, shortID(exec.ID))
			return nil
		},
	}
}

func execGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "get <id>",
		Short:        "Show an execution's status and output",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := kernelClient()
			id, err := resolveExecID(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			var e domain.Execution
			if err := c.do(cmd.Context(), http.MethodGet, "/v0/executions/"+id.String(), nil, &e); err != nil {
				return err
			}
			fmt.Printf("  id       %s\n", e.ID)
			fmt.Printf("  agent    %s\n", e.AgentID)
			fmt.Printf("  status   %s\n", e.Status)
			fmt.Printf("  created  %s (%s ago)\n", e.CreatedAt.Format(time.RFC3339), age(e.CreatedAt))
			if len(e.Output) > 0 {
				fmt.Printf("  output   %s\n", oneLine(e.Output, 200))
			}
			if e.FailureReason != "" {
				fmt.Printf("  failure  %s\n", e.FailureReason)
			}
			return nil
		},
	}
}

func execWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch <id>",
		Short: "Tail an execution's events until it finishes",
		Long: "Tail an execution's events until it reaches a terminal status. Exits\n" +
			"non-zero if the execution failed or was cancelled.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := kernelClient()
			id, err := resolveExecID(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			return watchExecution(cmd.Context(), c, id)
		},
	}
}

func execEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events <id>",
		Short: "Print the full event log with expanded payloads",
		Args:  cobra.ExactArgs(1),

		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := kernelClient()
			id, err := resolveExecID(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			return dumpEvents(cmd.Context(), c, id)
		},
	}
}

func execCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "cancel <id>",
		Short:        "Cancel a running execution",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := kernelClient()
			id, err := resolveExecID(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if err := c.do(cmd.Context(), http.MethodPost, "/v0/executions/"+id.String()+"/cancel", nil, nil); err != nil {
				return err
			}
			fmt.Printf("  cancelled %s\n", shortID(id))
			return nil
		},
	}
}

func getEvents(ctx context.Context, c *client, id uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	path := fmt.Sprintf("/v0/executions/%s/events?after_seq=%d&limit=%d", id, afterSeq, limit)
	var events []domain.Event
	err := c.do(ctx, http.MethodGet, path, nil, &events)
	return events, err
}

func dumpEvents(ctx context.Context, c *client, id uuid.UUID) error {
	const batch = 100
	var after int64
	var total int
	for {
		events, err := getEvents(ctx, c, id, after, batch)
		if err != nil {
			return err
		}
		for _, ev := range events {
			fmt.Printf("  [%d] %s\n", ev.EventSeq, ev.Type)
			fmt.Println(indentJSON(ev.Payload))
			after = ev.EventSeq
			total++
		}
		if len(events) < batch {
			break
		}
	}
	fmt.Printf("  --- %d event(s) ---\n", total)
	return nil
}

func watchExecution(ctx context.Context, c *client, id uuid.UUID) error {
	var after int64
	ticker := time.NewTicker(watchPollInterval)
	defer ticker.Stop()
	for {
		events, err := getEvents(ctx, c, id, after, 100)
		if err != nil {
			return err
		}
		for _, ev := range events {
			fmt.Printf("  [%d] %-26s %s\n", ev.EventSeq, ev.Type, oneLine(ev.Payload, 100))
			after = ev.EventSeq
		}
		var e domain.Execution
		if err := c.do(ctx, http.MethodGet, "/v0/executions/"+id.String(), nil, &e); err != nil {
			return err
		}
		if e.Status.IsTerminal() {
			fmt.Printf("  --- %s ---\n", e.Status)
			if e.Status != domain.ExecutionCompleted {
				os.Exit(1)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func resolveExecID(ctx context.Context, c *client, arg string) (uuid.UUID, error) {
	if id, err := uuid.Parse(arg); err == nil {
		return id, nil
	}
	path := "/v0/executions?limit=" + strconv.Itoa(kernel.MaxListExecutionsLimit)
	var page domain.ExecutionPage
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return uuid.Nil, err
	}
	var matches []uuid.UUID
	for _, e := range page.Executions {
		if strings.HasPrefix(e.ID.String(), arg) {
			matches = append(matches, e.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return uuid.Nil, fmt.Errorf("no execution matching %q", arg)
	default:
		return uuid.Nil, fmt.Errorf("%q is ambiguous (%d matches)", arg, len(matches))
	}
}

// Shorter collides: the leading hex of a UUIDv7 advances only every ~65s.
const shortIDLen = 18

func shortID(id uuid.UUID) string { return id.String()[:shortIDLen] }

func age(t time.Time) string { return shortDuration(time.Since(t)) }

func remaining(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	return shortDuration(d)
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func oneLine(b []byte, max int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func indentJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "      ", "  "); err != nil {
		return "      " + string(b)
	}
	return "      " + buf.String()
}

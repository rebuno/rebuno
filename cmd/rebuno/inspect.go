package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

var noColor = os.Getenv("NO_COLOR") != ""

func color(code, s string) string {
	if noColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func statusColor(status string) string {
	switch status {
	case "pending":
		return color("33", status) // yellow
	case "running":
		return color("36", status) // cyan
	case "blocked":
		return color("35", status) // magenta
	case "completed":
		return color("32", status) // green
	case "failed":
		return color("31", status) // red
	case "cancelled":
		return color("90", status) // gray
	default:
		return status
	}
}

type executionSummary struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	AgentID   string `json:"agent_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type execution struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	AgentID   string            `json:"agent_id"`
	Labels    map[string]string `json:"labels,omitempty"`
	Input     json.RawMessage   `json:"input,omitempty"`
	Output    json.RawMessage   `json:"output,omitempty"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
}

type event struct {
	ID            string          `json:"id"`
	ExecutionID   string          `json:"execution_id"`
	StepID        string          `json:"step_id,omitempty"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	Timestamp     string          `json:"timestamp"`
	Payload       json.RawMessage `json:"payload"`
	CausationID   string          `json:"causation_id"`
	CorrelationID string          `json:"correlation_id"`
	Sequence      int64           `json:"sequence"`
}

type listExecutionsResponse struct {
	Executions []executionSummary `json:"executions"`
	NextCursor string             `json:"next_cursor"`
}

type listEventsResponse struct {
	Events         []event `json:"events"`
	LatestSequence int64   `json:"latest_sequence"`
}

func doRequest(method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	return httpClient.Do(req)
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("HTTP %d (%s): %s", resp.StatusCode, errResp.Code, errResp.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func decodeJSON(resp *http.Response, target any) error {
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func printExecution(e *execution) {
	fmt.Printf("ID:        %s\n", e.ID)
	fmt.Printf("Status:    %s\n", statusColor(e.Status))
	fmt.Printf("Agent:     %s\n", e.AgentID)
	if len(e.Labels) > 0 {
		fmt.Printf("Labels:    %v\n", e.Labels)
	}
	fmt.Printf("Created:   %s\n", e.CreatedAt)
	fmt.Printf("Updated:   %s\n", e.UpdatedAt)
	if len(e.Input) > 0 && string(e.Input) != "null" {
		fmt.Printf("Input:     %s\n", string(e.Input))
	}
	if len(e.Output) > 0 && string(e.Output) != "null" {
		fmt.Printf("Output:    %s\n", string(e.Output))
	}
}

func printEventRow(e *event) {
	ts := e.Timestamp
	if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
		ts = t.Format("15:04:05.000")
	}
	stepInfo := ""
	if e.StepID != "" {
		stepInfo = fmt.Sprintf(" step=%s", e.StepID)
	}
	fmt.Printf("[%d] %s %s%s %s\n", e.Sequence, ts, e.Type, stepInfo, string(e.Payload))
}

func healthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check kernel connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest("GET", "/v0/health", nil)
			if err != nil {
				return fmt.Errorf("cannot reach kernel at %s: %w", baseURL, err)
			}
			defer resp.Body.Close()
			if err := checkStatus(resp); err != nil {
				return err
			}
			fmt.Printf("%s Kernel is healthy at %s\n", color("32", "OK"), baseURL)
			return nil
		},
	}
}

func executionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "executions",
		Short: "List executions",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, _ := cmd.Flags().GetString("status")
			agent, _ := cmd.Flags().GetString("agent")
			limit, _ := cmd.Flags().GetInt("limit")

			path := fmt.Sprintf("/v0/executions?limit=%d", limit)
			if status != "" {
				path += "&status=" + status
			}
			if agent != "" {
				path += "&agent_id=" + agent
			}

			resp, err := doRequest("GET", path, nil)
			if err != nil {
				return err
			}
			var result listExecutionsResponse
			if err := decodeJSON(resp, &result); err != nil {
				return err
			}

			if len(result.Executions) == 0 {
				fmt.Println("No executions found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tAGENT\tCREATED\tUPDATED")
			for _, e := range result.Executions {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					e.ID, statusColor(e.Status), e.AgentID, e.CreatedAt, e.UpdatedAt)
			}
			return w.Flush()
		},
	}
	cmd.Flags().String("status", "", "Filter by status")
	cmd.Flags().String("agent", "", "Filter by agent ID")
	cmd.Flags().Int("limit", 50, "Maximum number of results")
	return cmd
}

func executionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "execution [id]",
		Short: "Show execution detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest("GET", "/v0/executions/"+args[0], nil)
			if err != nil {
				return err
			}
			var e execution
			if err := decodeJSON(resp, &e); err != nil {
				return err
			}
			printExecution(&e)
			return nil
		},
	}
}

func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events [execution_id]",
		Short: "Show event log for an execution",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tail, _ := cmd.Flags().GetBool("tail")
			limit, _ := cmd.Flags().GetInt("limit")

			if tail {
				return tailEvents(args[0])
			}

			path := fmt.Sprintf("/v0/executions/%s/events?limit=%d", args[0], limit)
			resp, err := doRequest("GET", path, nil)
			if err != nil {
				return err
			}
			var result listEventsResponse
			if err := decodeJSON(resp, &result); err != nil {
				return err
			}

			if len(result.Events) == 0 {
				fmt.Println("No events found.")
				return nil
			}

			for i := range result.Events {
				printEventRow(&result.Events[i])
			}
			return nil
		},
	}
	cmd.Flags().Bool("tail", false, "Follow live events via SSE")
	cmd.Flags().Int("limit", 100, "Maximum number of events")
	return cmd
}

func tailEvents(execID string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	url := baseURL + "/v0/executions/" + execID + "/stream"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// No timeout for long-lived SSE connection
	sseClient := &http.Client{}
	resp, err := sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SSE error HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType, data string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, ":") {
			continue // heartbeat or comment
		}
		if strings.HasPrefix(line, "retry:") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			continue
		}
		if line == "" && data != "" {
			// End of SSE message — dispatch
			var ev event
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				printEventRow(&ev)
			}

			// Exit on terminal events
			if eventType == "execution.completed" || eventType == "execution.failed" || eventType == "execution.cancelled" {
				return nil
			}

			eventType = ""
			data = ""
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func createCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new execution",
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, _ := cmd.Flags().GetString("agent")
			inputStr, _ := cmd.Flags().GetString("input")
			labels, _ := cmd.Flags().GetStringSlice("label")

			body := map[string]any{
				"agent_id": agent,
			}

			if inputStr != "" {
				var input json.RawMessage
				if err := json.Unmarshal([]byte(inputStr), &input); err != nil {
					return fmt.Errorf("invalid --input JSON: %w", err)
				}
				body["input"] = input
			}

			if len(labels) > 0 {
				labelMap := make(map[string]string)
				for _, l := range labels {
					parts := strings.SplitN(l, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid label %q (expected key=value)", l)
					}
					labelMap[parts[0]] = parts[1]
				}
				body["labels"] = labelMap
			}

			resp, err := doRequest("POST", "/v0/executions", body)
			if err != nil {
				return err
			}
			var e execution
			if err := decodeJSON(resp, &e); err != nil {
				return err
			}
			printExecution(&e)
			return nil
		},
	}
	cmd.Flags().String("agent", "", "Agent ID (required)")
	_ = cmd.MarkFlagRequired("agent")
	cmd.Flags().String("input", "", "Input JSON")
	cmd.Flags().StringSlice("label", nil, "Labels as key=value (repeatable)")
	return cmd
}

func cancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel [id]",
		Short: "Cancel an execution",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest("POST", "/v0/executions/"+args[0]+"/cancel", nil)
			if err != nil {
				return err
			}
			var e execution
			if err := decodeJSON(resp, &e); err != nil {
				return err
			}
			fmt.Printf("Execution %s cancelled.\n", e.ID)
			return nil
		},
	}
}

func signalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "signal [id]",
		Short: "Send a signal to an execution",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sigType, _ := cmd.Flags().GetString("type")
			payloadStr, _ := cmd.Flags().GetString("payload")

			body := map[string]any{
				"signal_type": sigType,
			}

			if payloadStr != "" {
				var payload json.RawMessage
				if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
					return fmt.Errorf("invalid --payload JSON: %w", err)
				}
				body["payload"] = payload
			}

			resp, err := doRequest("POST", "/v0/executions/"+args[0]+"/signal", body)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if err := checkStatus(resp); err != nil {
				return err
			}
			fmt.Println("Signal sent.")
			return nil
		},
	}
	cmd.Flags().String("type", "", "Signal type (required)")
	_ = cmd.MarkFlagRequired("type")
	cmd.Flags().String("payload", "", "Signal payload JSON")
	return cmd
}

func addInspectCommands(root *cobra.Command) {
	root.AddCommand(
		healthCmd(),
		executionsCmd(),
		executionCmd(),
		eventsCmd(),
		createCmd(),
		cancelCmd(),
		signalCmd(),
	)
}

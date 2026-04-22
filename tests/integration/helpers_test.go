//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rebuno/rebuno/internal/domain"
)

func truncateAll(ctx context.Context, pool *pgxpool.Pool) error {
	tables := []string{"events", "checkpoints", "signals", "sessions", "runners", "executions"}
	for _, t := range tables {
		if _, err := pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s", t)); err != nil {
			return fmt.Errorf("truncate %s: %w", t, err)
		}
	}
	return nil
}

func cleanupExecution(t *testing.T, pool *pgxpool.Pool, execID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// CASCADE handles events, checkpoints, signals, sessions
		_, _ = pool.Exec(ctx, "DELETE FROM executions WHERE id = $1", execID)
	})
}

type httpClient struct {
	baseURL string
	client  *http.Client
}

func newHTTPClient(baseURL string) *httpClient {
	return &httpClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (hc *httpClient) postJSON(t *testing.T, path string, body any) (int, json.RawMessage) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp, err := hc.client.Post(hc.baseURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, json.RawMessage(respBody)
}

func (hc *httpClient) getJSON(t *testing.T, path string) (int, json.RawMessage) {
	t.Helper()
	resp, err := hc.client.Get(hc.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, json.RawMessage(body)
}

func createExecution(t *testing.T, hc *httpClient, pool *pgxpool.Pool, agentID string, input json.RawMessage) string {
	t.Helper()
	status, body := hc.postJSON(t, "/v0/executions", map[string]any{
		"agent_id": agentID,
		"input":    input,
	})
	if status != http.StatusCreated {
		t.Fatalf("create execution: status %d, body: %s", status, body)
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	cleanupExecution(t, pool, resp.ID)
	return resp.ID
}

type sseEvent struct {
	Type string
	Data string
}

type sseClient struct {
	resp   *http.Response
	reader *bufio.Reader
	cancel context.CancelFunc
}

func connectAgentSSE(t *testing.T, baseURL, agentID, consumerID string) *sseClient {
	t.Helper()
	url := fmt.Sprintf("%s/v0/agents/stream?agent_id=%s&consumer_id=%s", baseURL, agentID, consumerID)
	return connectSSE(t, url)
}

func connectRunnerSSE(t *testing.T, baseURL, runnerID, consumerID string, capabilities []string) *sseClient {
	t.Helper()
	caps := strings.Join(capabilities, ",")
	url := fmt.Sprintf("%s/v0/runners/stream?runner_id=%s&consumer_id=%s&capabilities=%s",
		baseURL, runnerID, consumerID, caps)
	return connectSSE(t, url)
}

func connectSSE(t *testing.T, url string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("create SSE request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{
		Timeout: 0, // no timeout for SSE
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connect SSE %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("SSE connect status %d: %s", resp.StatusCode, body)
	}

	sc := &sseClient{
		resp:   resp,
		reader: bufio.NewReader(resp.Body),
		cancel: cancel,
	}
	t.Cleanup(sc.close)
	return sc
}

func (sc *sseClient) readEvent(t *testing.T, timeout time.Duration) sseEvent {
	t.Helper()
	type result struct {
		event sseEvent
		err   error
	}
	ch := make(chan result, 1)

	go func() {
		for {
			evt, err := sc.readNextEvent()
			if err != nil {
				ch <- result{err: err}
				return
			}
			if evt.Type == "" && evt.Data == "" {
				continue
			}
			ch <- result{event: evt}
			return
		}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read SSE event: %v", r.err)
		}
		return r.event
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for SSE event after %v", timeout)
		return sseEvent{}
	}
}

func (sc *sseClient) readNextEvent() (sseEvent, error) {
	var eventType, data string

	for {
		line, err := sc.reader.ReadString('\n')
		if err != nil {
			return sseEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			if eventType != "" || data != "" {
				return sseEvent{Type: eventType, Data: data}, nil
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			if data != "" {
				data += "\n"
			}
			data += strings.TrimPrefix(line, "data: ")
		}
	}
}

func (sc *sseClient) close() {
	sc.cancel()
	sc.resp.Body.Close()
}

func getEvents(t *testing.T, hc *httpClient, execID string) []domain.Event {
	t.Helper()
	status, body := hc.getJSON(t, fmt.Sprintf("/v0/executions/%s/events?limit=1000", execID))
	if status != http.StatusOK {
		t.Fatalf("get events: status %d, body: %s", status, body)
	}

	var resp struct {
		Events []domain.Event `json:"events"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	return resp.Events
}

func getExecutionStatus(t *testing.T, hc *httpClient, execID string) domain.ExecutionStatus {
	t.Helper()
	status, body := hc.getJSON(t, fmt.Sprintf("/v0/executions/%s", execID))
	if status != http.StatusOK {
		t.Fatalf("get execution: status %d, body: %s", status, body)
	}

	var exec domain.Execution
	if err := json.Unmarshal(body, &exec); err != nil {
		t.Fatalf("decode execution response: %v", err)
	}
	return exec.Status
}

func waitForStatus(t *testing.T, hc *httpClient, execID string, want domain.ExecutionStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := getExecutionStatus(t, hc, execID)
		if got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := getExecutionStatus(t, hc, execID)
	t.Fatalf("execution %s: wanted status %q, got %q after %v", execID, want, got, timeout)
}

func eventTypes(events []domain.Event) []domain.EventType {
	types := make([]domain.EventType, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

func containsEventType(events []domain.Event, et domain.EventType) bool {
	for _, e := range events {
		if e.Type == et {
			return true
		}
	}
	return false
}

func requireEventTrail(t *testing.T, events []domain.Event, expected []domain.EventType) {
	t.Helper()
	got := eventTypes(events)
	if len(got) != len(expected) {
		t.Fatalf("event trail length mismatch:\n  got:    %v\n  wanted: %v", got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("event trail mismatch at index %d:\n  got:    %v\n  wanted: %v", i, got, expected)
		}
	}
}

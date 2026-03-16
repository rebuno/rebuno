//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestCancelExecution(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-cancel"
	consumerID := "consumer-cancel-1"

	// 1. Connect agent SSE
	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	// 2. Create execution
	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-cancel"}`))

	// 3. Agent receives execution.assigned
	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	// 4. Cancel via POST /v0/executions/{id}/cancel
	status, body := hc.postJSON(t, "/v0/executions/"+execID+"/cancel", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("cancel execution: status %d, body: %s", status, body)
	}

	// 5. Verify execution status is cancelled
	waitForStatus(t, hc, execID, domain.ExecutionCancelled, 5*time.Second)

	// 6. Verify event trail includes execution.cancelled
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventExecutionCancelled) {
		t.Fatalf("expected execution.cancelled in event trail: %v", eventTypes(events))
	}

	// 7. Verify that cancelling again returns a conflict error
	status, _ = hc.postJSON(t, "/v0/executions/"+execID+"/cancel", map[string]any{})
	if status != http.StatusConflict {
		t.Fatalf("expected 409 for double cancel, got %d", status)
	}
}

func TestCancelPendingExecution(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	// Create execution with no agent connected — stays pending.
	execID := createExecution(t, hc, testPool, "agent-cancel-pending-no-sse", json.RawMessage(`{"task":"cancel-pending"}`))

	// Verify it's pending (no agent SSE to pick it up).
	waitForStatus(t, hc, execID, domain.ExecutionPending, 3*time.Second)

	// Cancel from pending state.
	status, body := hc.postJSON(t, "/v0/executions/"+execID+"/cancel", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("cancel pending execution: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCancelled, 3*time.Second)

	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventExecutionCancelled) {
		t.Fatalf("expected execution.cancelled in event trail: %v", eventTypes(events))
	}
}

func TestCancelBlockedExecution(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-cancel-blocked"
	consumerID := "consumer-cancel-blocked"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"cancel-blocked"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		ExecutionID string `json:"execution_id"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// Block the execution by submitting a wait intent.
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":        "wait",
			"signal_type": "approval",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit wait intent: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionBlocked, 5*time.Second)

	// Cancel from blocked state.
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/cancel", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("cancel blocked execution: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCancelled, 5*time.Second)

	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventExecutionBlocked) {
		t.Fatalf("expected execution.blocked in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionCancelled) {
		t.Fatalf("expected execution.cancelled in event trail: %v", eventTypes(events))
	}
}

func TestCancelNonexistentExecution(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	status, _ := hc.postJSON(t, "/v0/executions/nonexistent-id-12345/cancel", map[string]any{})
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent execution cancel, got %d", status)
	}
}

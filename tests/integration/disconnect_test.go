//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestAgentDisconnect(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-disconnect"
	consumerID := "consumer-disconnect"

	// 1. Connect agent → create execution → verify running
	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-disconnect"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)

	// 2. Close SSE connection (simulates agent disconnect)
	sse.close()

	// 3. Wait for status = pending (HandleAgentDisconnect resets it)
	waitForStatus(t, hc, execID, domain.ExecutionPending, 15*time.Second)

	// 4. Verify agent.timeout event
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventAgentTimeout) {
		t.Fatalf("expected agent.timeout in event trail: %v", eventTypes(events))
	}

	// 5. Reconnect → receives reassignment for same execution
	sse2 := connectAgentSSE(t, ts.URL, agentID, consumerID+"-2")

	evt2 := sse2.readEvent(t, 10*time.Second)
	if evt2.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned on reconnect, got %q", evt2.Type)
	}

	var claim struct {
		ExecutionID string `json:"execution_id"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt2.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claim.ExecutionID != execID {
		t.Fatalf("expected reassignment for %s, got %s", execID, claim.ExecutionID)
	}

	// 6. Complete → verify completed
	hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "reconnected"},
		},
	})

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

func TestAgentDisconnectWhileBlocked(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-disconnect-blocked"
	consumerID := "consumer-disconnect-blocked"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"disconnect-blocked"}`))

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

	// Block the execution by waiting for a signal.
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":        "wait",
			"signal_type": "user_input",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit wait intent: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionBlocked, 5*time.Second)

	// Disconnect agent while blocked.
	sse.close()

	// Blocked executions should stay blocked (not reset to pending).
	// Wait a bit and verify it's still blocked.
	time.Sleep(2 * time.Second)
	got := getExecutionStatus(t, hc, execID)
	if got != domain.ExecutionBlocked {
		t.Fatalf("expected execution to remain blocked after disconnect, got %q", got)
	}

	// agent.timeout event should still be recorded.
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventAgentTimeout) {
		t.Fatalf("expected agent.timeout in event trail: %v", eventTypes(events))
	}

	// The signal should still be able to resume the execution even without an agent.
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "user_input",
		"payload":     map[string]any{"text": "hello"},
	})
	if status != http.StatusOK {
		t.Fatalf("send signal: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)
}

func TestAgentDisconnectAfterCompletion(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-disconnect-completed"
	consumerID := "consumer-disconnect-completed"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"disconnect-completed"}`))

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

	// Complete the execution.
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "done"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

	// Disconnect after completion — should not change state or cause errors.
	sse.close()

	time.Sleep(1 * time.Second)
	got := getExecutionStatus(t, hc, execID)
	if got != domain.ExecutionCompleted {
		t.Fatalf("expected execution to remain completed after disconnect, got %q", got)
	}
}

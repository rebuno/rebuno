//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestSignalBlockAndResume(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-signal"
	consumerID := "consumer-signal-1"

	// 1. Connect agent SSE
	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	// 2. Create execution
	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-signal"}`))

	// 3. Agent receives execution.assigned
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

	// 4. Agent submits wait intent with signal_type
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":        "wait",
			"signal_type": "human_approval",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit wait intent: status %d, body: %s", status, body)
	}

	var intentResult domain.IntentResult
	if err := json.Unmarshal(body, &intentResult); err != nil {
		t.Fatalf("decode intent result: %v", err)
	}
	if !intentResult.Accepted {
		t.Fatalf("wait intent not accepted: %s", intentResult.Error)
	}

	// 5. Verify execution is blocked
	waitForStatus(t, hc, execID, domain.ExecutionBlocked, 5*time.Second)

	// 6. Send signal via POST /v0/executions/{id}/signal
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "human_approval",
		"payload":     map[string]any{"approved": true},
	})
	if status != http.StatusOK {
		t.Fatalf("send signal: status %d, body: %s", status, body)
	}

	// 7. Execution should resume (status = running)
	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)

	// 8. Agent receives signal.received via SSE
	sigEvt := sse.readEvent(t, 5*time.Second)
	if sigEvt.Type != "signal.received" {
		t.Fatalf("expected signal.received SSE event, got %q", sigEvt.Type)
	}

	// 9. Agent completes execution
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "approved and done"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	// 10. Verify execution is completed
	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

	// 11. Verify event trail includes signal-related events
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventExecutionBlocked) {
		t.Fatalf("expected execution.blocked in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventSignalReceived) {
		t.Fatalf("expected signal.received in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionResumed) {
		t.Fatalf("expected execution.resumed in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionCompleted) {
		t.Fatalf("expected execution.completed in event trail: %v", eventTypes(events))
	}
}

func TestSignalToTerminalExecution(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-signal-terminal"
	consumerID := "consumer-signal-terminal"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"signal-terminal"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	// Cancel the execution to make it terminal.
	status, body := hc.postJSON(t, "/v0/executions/"+execID+"/cancel", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("cancel: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionCancelled, 5*time.Second)

	// Sending a signal to a terminal execution should fail.
	status, _ = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "human_approval",
		"payload":     map[string]any{"approved": true},
	})
	if status != http.StatusConflict {
		t.Fatalf("expected 409 for signal to terminal execution, got %d", status)
	}
}

func TestSignalWrongType(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-signal-wrong"
	consumerID := "consumer-signal-wrong"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"signal-wrong-type"}`))

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

	// Block waiting for "approval" signal type.
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

	// Send a signal with a different type — should be accepted (stored) but NOT resume.
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "wrong_type",
		"payload":     map[string]any{"data": "irrelevant"},
	})
	if status != http.StatusOK {
		t.Fatalf("send wrong signal: status %d, body: %s", status, body)
	}

	// Execution should still be blocked — the signal type didn't match.
	time.Sleep(500 * time.Millisecond)
	got := getExecutionStatus(t, hc, execID)
	if got != domain.ExecutionBlocked {
		t.Fatalf("expected execution to remain blocked after wrong signal type, got %q", got)
	}

	// Now send the correct signal type — should resume.
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "approval",
		"payload":     map[string]any{"approved": true},
	})
	if status != http.StatusOK {
		t.Fatalf("send correct signal: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)
}

func TestSignalWithNullPayload(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-signal-null"
	consumerID := "consumer-signal-null"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"signal-null-payload"}`))

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

	// Block on a signal.
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":        "wait",
			"signal_type": "nudge",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit wait intent: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionBlocked, 5*time.Second)

	// Send signal with null payload — should still resume.
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "nudge",
		"payload":     nil,
	})
	if status != http.StatusOK {
		t.Fatalf("send signal with null payload: status %d, body: %s", status, body)
	}
	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)
}

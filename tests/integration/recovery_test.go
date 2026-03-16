//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestStartupRecovery(t *testing.T) {
	agentID := "agent-recovery"
	consumerID := "consumer-recovery"

	// 1. Start server A → connect agent → create execution → verify running
	tsA := startServer(t, testPool)
	hcA := newHTTPClient(tsA.URL)

	sseA := connectAgentSSE(t, tsA.URL, agentID, consumerID)

	execID := createExecution(t, hcA, testPool, agentID, json.RawMessage(`{"task":"test-recovery"}`))

	evt := sseA.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	waitForStatus(t, hcA, execID, domain.ExecutionRunning, 5*time.Second)

	// 2. Shut down server A
	sseA.close()
	tsA.Shutdown()

	// 3. Simulate reaper: clean up stale session + reset execution to pending
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := testPool.Exec(ctx, "DELETE FROM sessions WHERE execution_id = $1", execID)
	if err != nil {
		t.Fatalf("cleanup sessions: %v", err)
	}
	_, err = testPool.Exec(ctx, "UPDATE executions SET status = 'pending' WHERE id = $1", execID)
	if err != nil {
		t.Fatalf("reset execution status: %v", err)
	}

	// 4. Start server B with same DB
	tsB := startServer(t, testPool)
	hcB := newHTTPClient(tsB.URL)
	waitForServer(t, tsB.URL, 5*time.Second)

	// 5. Connect agent to server B → receives reassignment
	sseB := connectAgentSSE(t, tsB.URL, agentID, consumerID+"-b")

	evtB := sseB.readEvent(t, 10*time.Second)
	if evtB.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned on server B, got %q", evtB.Type)
	}

	var claim struct {
		ExecutionID string `json:"execution_id"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evtB.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claim.ExecutionID != execID {
		t.Fatalf("expected reassignment for %s, got %s", execID, claim.ExecutionID)
	}

	// 6. Complete → verify completed
	hcB.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "recovered"},
		},
	})

	waitForStatus(t, hcB, execID, domain.ExecutionCompleted, 5*time.Second)
}

func TestRecoveryBlockedExecution(t *testing.T) {
	agentID := "agent-recovery-blocked"
	consumerID := "consumer-recovery-blocked"

	// 1. Start server A, connect agent, create execution, block it.
	tsA := startServer(t, testPool)
	hcA := newHTTPClient(tsA.URL)

	sseA := connectAgentSSE(t, tsA.URL, agentID, consumerID)

	execID := createExecution(t, hcA, testPool, agentID, json.RawMessage(`{"task":"recovery-blocked"}`))

	evt := sseA.readEvent(t, 5*time.Second)
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

	// Block the execution.
	status, body := hcA.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":        "wait",
			"signal_type": "recovery_signal",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit wait intent: status %d, body: %s", status, body)
	}
	waitForStatus(t, hcA, execID, domain.ExecutionBlocked, 5*time.Second)

	// 2. Shut down server A.
	sseA.close()
	tsA.Shutdown()

	// 3. Simulate reaper: clean up stale session (but leave execution as blocked).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := testPool.Exec(ctx, "DELETE FROM sessions WHERE execution_id = $1", execID)
	if err != nil {
		t.Fatalf("cleanup sessions: %v", err)
	}

	// 4. Start server B with same DB.
	tsB := startServer(t, testPool)
	hcB := newHTTPClient(tsB.URL)
	waitForServer(t, tsB.URL, 5*time.Second)

	// 5. Blocked execution should remain blocked — NOT be reassigned yet.
	got := getExecutionStatus(t, hcB, execID)
	if got != domain.ExecutionBlocked {
		t.Fatalf("expected blocked execution to remain blocked after recovery, got %q", got)
	}

	// 6. Send the awaited signal via server B — should resume.
	status, body = hcB.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "recovery_signal",
		"payload":     map[string]any{"data": "recovered"},
	})
	if status != http.StatusOK {
		t.Fatalf("send signal: status %d, body: %s", status, body)
	}
	waitForStatus(t, hcB, execID, domain.ExecutionRunning, 5*time.Second)
}

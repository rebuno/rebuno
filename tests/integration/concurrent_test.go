//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestConcurrentExecutions(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-concurrent"
	consumerID := "consumer-concurrent"

	// 1. Connect one agent
	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	// 2. Create 5 executions
	const count = 5
	execIDs := make([]string, count)
	for i := 0; i < count; i++ {
		execIDs[i] = createExecution(t, hc, testPool, agentID, json.RawMessage(fmt.Sprintf(`{"task":"concurrent","index":%d}`, i)))
	}

	// 3. Collect all 5 execution.assigned events
	type assignment struct {
		ExecutionID string `json:"execution_id"`
		SessionID   string `json:"session_id"`
	}
	assignments := make(map[string]assignment)
	for i := 0; i < count; i++ {
		evt := sse.readEvent(t, 10*time.Second)
		if evt.Type != "execution.assigned" {
			t.Fatalf("expected execution.assigned, got %q", evt.Type)
		}
		var a assignment
		if err := json.Unmarshal([]byte(evt.Data), &a); err != nil {
			t.Fatalf("decode assignment: %v", err)
		}
		assignments[a.ExecutionID] = a
	}

	if len(assignments) != count {
		t.Fatalf("expected %d unique assignments, got %d", count, len(assignments))
	}

	// 4. Verify all execution IDs are represented
	for _, execID := range execIDs {
		if _, ok := assignments[execID]; !ok {
			t.Fatalf("execution %s was not assigned", execID)
		}
	}

	// 5. Complete all 5
	for _, execID := range execIDs {
		a := assignments[execID]
		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   a.SessionID,
			"intent": map[string]any{
				"type":   "complete",
				"output": map[string]string{"result": "done"},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("complete execution %s: status %d, body: %s", execID, status, body)
		}
	}

	// 6. Verify all 5 completed with correct event trails
	for _, execID := range execIDs {
		waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

		events := getEvents(t, hc, execID)
		if !containsEventType(events, domain.EventExecutionCreated) {
			t.Fatalf("execution %s: missing execution.created", execID)
		}
		if !containsEventType(events, domain.EventExecutionStarted) {
			t.Fatalf("execution %s: missing execution.started", execID)
		}
		if !containsEventType(events, domain.EventExecutionCompleted) {
			t.Fatalf("execution %s: missing execution.completed", execID)
		}
	}
}

// TestConcurrentToolInvocationConflict verifies that an agent cannot invoke
// a second tool while another tool step is already active.
func TestConcurrentToolInvocationConflict(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-tool-conflict"
	consumerID := "consumer-conflict-1"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-conflict"}`))

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

	// Start first tool invocation
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "local.slow",
			"arguments": map[string]string{
				"action": "sleep",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit first intent: status %d, body: %s", status, body)
	}

	var firstResult domain.IntentResult
	if err := json.Unmarshal(body, &firstResult); err != nil {
		t.Fatalf("decode first intent result: %v", err)
	}
	if !firstResult.Accepted {
		t.Fatalf("first intent not accepted: %s", firstResult.Error)
	}

	// Try to invoke a second tool while the first is still active
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "local.fast",
			"arguments": map[string]string{
				"action": "echo",
			},
		},
	})
	// Should fail with a conflict error (not 200 OK, or 200 with accepted=false)
	if status == http.StatusOK {
		var secondResult domain.IntentResult
		if err := json.Unmarshal(body, &secondResult); err == nil && secondResult.Accepted {
			t.Fatalf("expected second tool invocation to be rejected while first is active")
		}
	}
	// Any non-success is acceptable — the kernel should prevent concurrent steps.

	// Complete the first step so we can cleanly finish
	status, body = hc.postJSON(t, "/v0/agents/step-result", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"step_id":      firstResult.StepID,
		"success":      true,
		"data":         map[string]string{"result": "done"},
	})
	if status != http.StatusOK {
		t.Fatalf("submit step result: status %d, body: %s", status, body)
	}

	// Read the tool.result SSE
	toolEvt := sse.readEvent(t, 5*time.Second)
	if toolEvt.Type != "tool.result" {
		t.Fatalf("expected tool.result, got %q", toolEvt.Type)
	}

	// Complete the execution
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "conflict handled"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

// TestConcurrentExecutionsMixedOutcomes verifies that multiple concurrent
// executions can have different outcomes (some complete, some fail, some cancel)
// without interfering with each other.
func TestConcurrentExecutionsMixedOutcomes(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-mixed"
	consumerID := "consumer-mixed-1"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	// Create 3 executions
	exec1 := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"complete-me"}`))
	exec2 := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"fail-me"}`))
	exec3 := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"cancel-me"}`))

	type assignment struct {
		ExecutionID string `json:"execution_id"`
		SessionID   string `json:"session_id"`
	}
	assignments := make(map[string]assignment)
	for i := 0; i < 3; i++ {
		evt := sse.readEvent(t, 10*time.Second)
		if evt.Type != "execution.assigned" {
			t.Fatalf("expected execution.assigned, got %q", evt.Type)
		}
		var a assignment
		if err := json.Unmarshal([]byte(evt.Data), &a); err != nil {
			t.Fatalf("decode assignment: %v", err)
		}
		assignments[a.ExecutionID] = a
	}

	// Complete exec1
	a1 := assignments[exec1]
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": exec1,
		"session_id":   a1.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "success"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("complete exec1: status %d, body: %s", status, body)
	}

	// Fail exec2
	a2 := assignments[exec2]
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": exec2,
		"session_id":   a2.SessionID,
		"intent": map[string]any{
			"type":  "fail",
			"error": "intentional failure",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("fail exec2: status %d, body: %s", status, body)
	}

	// Cancel exec3
	status, body = hc.postJSON(t, "/v0/executions/"+exec3+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel exec3: status %d, body: %s", status, body)
	}

	// Verify each execution reached the correct terminal state
	waitForStatus(t, hc, exec1, domain.ExecutionCompleted, 5*time.Second)
	waitForStatus(t, hc, exec2, domain.ExecutionFailed, 5*time.Second)
	waitForStatus(t, hc, exec3, domain.ExecutionCancelled, 5*time.Second)

	// Verify each has the correct terminal event
	events1 := getEvents(t, hc, exec1)
	if !containsEventType(events1, domain.EventExecutionCompleted) {
		t.Fatalf("exec1: expected execution.completed")
	}

	events2 := getEvents(t, hc, exec2)
	if !containsEventType(events2, domain.EventExecutionFailed) {
		t.Fatalf("exec2: expected execution.failed")
	}

	events3 := getEvents(t, hc, exec3)
	if !containsEventType(events3, domain.EventExecutionCancelled) {
		t.Fatalf("exec3: expected execution.cancelled")
	}
}

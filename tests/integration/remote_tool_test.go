//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestRemoteToolHappyPath(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-remote-tool"
	agentConsumer := "agent-consumer-1"
	runnerID := "runner-web"
	runnerConsumer := "runner-consumer-1"

	// 1. Connect agent SSE + runner SSE (capability: web.search)
	agentSSE := connectAgentSSE(t, ts.URL, agentID, agentConsumer)
	_ = connectRunnerSSE(t, ts.URL, runnerID, runnerConsumer, []string{"web.search"})

	// Poll until the runner's capability is registered in the hub.
	{
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ts.RunnerHub.HasCapability("web.search") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !ts.RunnerHub.HasCapability("web.search") {
			t.Fatal("runner did not register web.search capability in time")
		}
	}

	// 2. Create execution → agent receives assignment
	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-remote"}`))

	evt := agentSSE.readEvent(t, 5*time.Second)
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

	// 3. Agent submits remote invoke_tool
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "web.search",
			"remote":  true,
			"arguments": map[string]string{
				"query": "rebuno integration test",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit intent: status %d, body: %s", status, body)
	}

	var intentResult domain.IntentResult
	if err := json.Unmarshal(body, &intentResult); err != nil {
		t.Fatalf("decode intent result: %v", err)
	}
	if !intentResult.Accepted {
		t.Fatalf("intent not accepted: %s", intentResult.Error)
	}
	stepID := intentResult.StepID

	// 4. Execution stays running (tool calls no longer block)

	// 5. Runner reports step started
	status, body = hc.postJSON(t, "/v0/runners/steps/"+stepID+"/started", map[string]any{
		"execution_id": execID,
		"runner_id":    runnerID,
	})
	if status != http.StatusOK {
		t.Fatalf("step started: status %d, body: %s", status, body)
	}

	// 6. Runner submits result
	now := time.Now()
	status, body = hc.postJSON(t, "/v0/runners/"+runnerID+"/results", map[string]any{
		"job_id":       stepID, // use step_id as job_id for simplicity
		"execution_id": execID,
		"step_id":      stepID,
		"success":      true,
		"data":         map[string]any{"results": []string{"result1", "result2"}},
		"started_at":   now.Add(-time.Second),
		"completed_at": now,
	})
	if status != http.StatusOK {
		t.Fatalf("submit runner result: status %d, body: %s", status, body)
	}

	// 7. Agent receives tool.result via SSE
	toolEvt := agentSSE.readEvent(t, 5*time.Second)
	if toolEvt.Type != "tool.result" {
		t.Fatalf("expected tool.result, got %q", toolEvt.Type)
	}

	// 8. Agent completes
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"status": "search done"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	// 9. Verify status = completed
	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

	// 10. Verify event trail includes step.dispatched and step.started
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventStepDispatched) {
		t.Fatalf("expected step.dispatched in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventStepStarted) {
		t.Fatalf("expected step.started in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionCompleted) {
		t.Fatalf("expected execution.completed in event trail: %v", eventTypes(events))
	}
}

// TestRemoteToolRunnerFailure verifies that when a runner reports failure
// for a remote tool step, the agent receives a tool.result with error status
// and can still complete the execution.
func TestRemoteToolRunnerFailure(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-remote-fail"
	agentConsumer := "agent-consumer-fail-1"
	runnerID := "runner-flaky"
	runnerConsumer := "runner-consumer-fail-1"

	agentSSE := connectAgentSSE(t, ts.URL, agentID, agentConsumer)
	_ = connectRunnerSSE(t, ts.URL, runnerID, runnerConsumer, []string{"flaky.operation"})

	// Wait for runner capability registration
	{
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ts.RunnerHub.HasCapability("flaky.operation") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !ts.RunnerHub.HasCapability("flaky.operation") {
			t.Fatal("runner did not register flaky.operation capability in time")
		}
	}

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-remote-fail"}`))

	evt := agentSSE.readEvent(t, 5*time.Second)
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

	// Agent submits remote tool invocation
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "flaky.operation",
			"remote":  true,
			"arguments": map[string]string{
				"mode": "fail",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit intent: status %d, body: %s", status, body)
	}

	var intentResult domain.IntentResult
	if err := json.Unmarshal(body, &intentResult); err != nil {
		t.Fatalf("decode intent result: %v", err)
	}
	if !intentResult.Accepted {
		t.Fatalf("intent not accepted: %s", intentResult.Error)
	}
	stepID := intentResult.StepID

	// Runner reports failure (non-retryable)
	now := time.Now()
	status, body = hc.postJSON(t, "/v0/runners/"+runnerID+"/results", map[string]any{
		"job_id":       stepID,
		"execution_id": execID,
		"step_id":      stepID,
		"success":      false,
		"retryable":    false,
		"error":        "runner crashed: segfault",
		"started_at":   now.Add(-time.Second),
		"completed_at": now,
	})
	if status != http.StatusOK {
		t.Fatalf("submit runner result: status %d, body: %s", status, body)
	}

	// Agent receives tool.result with failure
	toolEvt := agentSSE.readEvent(t, 5*time.Second)
	if toolEvt.Type != "tool.result" {
		t.Fatalf("expected tool.result, got %q", toolEvt.Type)
	}

	var toolResult struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolEvt.Data), &toolResult); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if toolResult.Status != "failed" {
		t.Fatalf("expected tool result status 'failed', got %q", toolResult.Status)
	}

	// Agent completes the execution despite tool failure
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "handled failure"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

	// Verify event trail includes step.failed
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventStepFailed) {
		t.Fatalf("expected step.failed in event trail: %v", eventTypes(events))
	}
}

// TestRemoteToolNoRunnerAvailable verifies that when no runner with the required
// capability is connected, the intent is still accepted (job is enqueued),
// and the execution can still be cancelled.
func TestRemoteToolNoRunnerAvailable(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-no-runner"
	agentConsumer := "agent-consumer-no-runner"

	agentSSE := connectAgentSSE(t, ts.URL, agentID, agentConsumer)
	// Deliberately do NOT connect any runner

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-no-runner"}`))

	evt := agentSSE.readEvent(t, 5*time.Second)
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

	// Agent submits remote tool invocation — no runner has the capability
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "nonexistent.tool",
			"remote":  true,
			"arguments": map[string]string{
				"data": "test",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit intent: status %d, body: %s", status, body)
	}

	// Intent should still be accepted (job is enqueued for when a runner connects)
	var intentResult domain.IntentResult
	if err := json.Unmarshal(body, &intentResult); err != nil {
		t.Fatalf("decode intent result: %v", err)
	}
	if !intentResult.Accepted {
		t.Fatalf("intent should be accepted even without runner: %s", intentResult.Error)
	}

	// Verify execution is still running (not failed)
	execStatus := getExecutionStatus(t, hc, execID)
	if execStatus != domain.ExecutionRunning {
		t.Fatalf("expected execution to stay running, got %q", execStatus)
	}

	// Cancel the execution since no runner will pick up the job
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel execution: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCancelled, 5*time.Second)
}

//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/policy"
)

func TestLocalToolHappyPath(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-local-tool"
	consumerID := "consumer-1"

	// 1. Connect agent SSE
	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	// 2. Create execution
	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-local"}`))

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
	if claim.ExecutionID != execID {
		t.Fatalf("claim execution_id mismatch: got %q, want %q", claim.ExecutionID, execID)
	}

	// 4. Agent submits invoke_tool intent (local)
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "local.echo",
			"arguments": map[string]string{
				"message": "hello",
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

	// 5. Execution stays running (tool calls no longer block)

	// 6. Agent submits step result (success)
	status, body = hc.postJSON(t, "/v0/agents/step-result", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"step_id":      stepID,
		"success":      true,
		"data":         map[string]string{"echoed": "hello"},
	})
	if status != http.StatusOK {
		t.Fatalf("submit step result: status %d, body: %s", status, body)
	}

	// 7. Agent receives tool.result via SSE
	toolEvt := sse.readEvent(t, 5*time.Second)
	if toolEvt.Type != "tool.result" {
		t.Fatalf("expected tool.result, got %q", toolEvt.Type)
	}

	// 8. Agent submits complete intent
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
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

	// 9. Verify status = completed
	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

	// 10. Verify event trail (no blocked/resumed for tool calls)
	events := getEvents(t, hc, execID)
	requireEventTrail(t, events, []domain.EventType{
		domain.EventExecutionCreated,
		domain.EventExecutionStarted,
		domain.EventIntentAccepted,
		domain.EventStepCreated,
		domain.EventStepCompleted,
		domain.EventIntentAccepted,
		domain.EventExecutionCompleted,
	})
}

// TestLocalToolStepFailure verifies that an agent can report a local tool failure
// and still complete the execution. Exercises the error branch of SubmitStepResult.
func TestLocalToolStepFailure(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-local-fail"
	consumerID := "consumer-fail-1"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-local-fail"}`))

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

	// Submit invoke_tool intent
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "local.flaky",
			"arguments": map[string]string{
				"action": "fail",
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

	// Agent reports step failure
	status, body = hc.postJSON(t, "/v0/agents/step-result", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"step_id":      stepID,
		"success":      false,
		"error":        "tool crashed: out of memory",
	})
	if status != http.StatusOK {
		t.Fatalf("submit step result: status %d, body: %s", status, body)
	}

	// Agent receives tool.result with failure
	toolEvt := sse.readEvent(t, 5*time.Second)
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
	if toolResult.Error != "tool crashed: out of memory" {
		t.Fatalf("expected error message 'tool crashed: out of memory', got %q", toolResult.Error)
	}

	// Agent still completes the execution (it handled the error gracefully)
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "handled error"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)

	// Verify event trail includes step.failed
	events := getEvents(t, hc, execID)
	requireEventTrail(t, events, []domain.EventType{
		domain.EventExecutionCreated,
		domain.EventExecutionStarted,
		domain.EventIntentAccepted,
		domain.EventStepCreated,
		domain.EventStepFailed,
		domain.EventIntentAccepted,
		domain.EventExecutionCompleted,
	})
}

// TestLocalToolPolicyDenied verifies that when policy denies a tool invocation,
// the intent is rejected and an intent.denied event is recorded.
func TestLocalToolPolicyDenied(t *testing.T) {
	// Create a deny policy for "dangerous.*" tools
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-dangerous",
				Priority: 0,
				When: domain.PolicyCondition{
					ToolID: "dangerous.*",
				},
				Then: domain.PolicyAction{
					Decision: domain.PolicyDeny,
					Reason:   "dangerous tools are forbidden",
				},
			},
			{
				ID:       "allow-rest",
				Priority: 1,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "allow by default",
				},
			},
		},
		Default: domain.PolicyAction{
			Decision: domain.PolicyAllow,
			Reason:   "default allow",
		},
	}
	engine, err := policy.NewRuleEngine(cfg)
	if err != nil {
		t.Fatalf("create policy engine: %v", err)
	}

	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-policy-deny"
	consumerID := "consumer-policy-1"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-policy"}`))

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

	// Try to invoke a dangerous tool — should be denied
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "dangerous.delete_all",
			"arguments": map[string]string{
				"target": "/",
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
	if intentResult.Accepted {
		t.Fatalf("expected intent to be denied, but it was accepted")
	}
	if !strings.Contains(intentResult.Error, "policy denied") {
		t.Fatalf("expected 'policy denied' in error, got %q", intentResult.Error)
	}

	// Verify intent.denied event was recorded
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventIntentDenied) {
		t.Fatalf("expected intent.denied event in trail: %v", eventTypes(events))
	}

	// Agent can still invoke an allowed tool and complete
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "denied gracefully"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

// TestCancelExecutionMidTool verifies that cancelling an execution while a local
// tool step is in progress correctly cancels both the step and execution.
func TestCancelExecutionMidTool(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-cancel-mid"
	consumerID := "consumer-cancel-1"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-cancel"}`))

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

	// Start a tool invocation
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "local.slow_tool",
			"arguments": map[string]string{
				"duration": "10s",
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

	// Cancel the execution while the tool is in progress
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel execution: status %d, body: %s", status, body)
	}

	// Verify execution is cancelled
	waitForStatus(t, hc, execID, domain.ExecutionCancelled, 5*time.Second)

	// Verify event trail includes step.cancelled and execution.cancelled
	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventStepCancelled) {
		t.Fatalf("expected step.cancelled in event trail: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionCancelled) {
		t.Fatalf("expected execution.cancelled in event trail: %v", eventTypes(events))
	}

	// Verify that submitting a step result after cancellation fails
	status, _ = hc.postJSON(t, "/v0/agents/step-result", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"step_id":      intentResult.StepID,
		"success":      true,
		"data":         map[string]string{"result": "late"},
	})
	// Should be rejected — step is already resolved
	if status == http.StatusOK {
		t.Fatalf("expected step result submission to fail after cancellation, got 200")
	}
}

// TestAgentFailsExecution verifies the fail intent path where the agent
// explicitly marks an execution as failed.
func TestAgentFailsExecution(t *testing.T) {
	ts := startServer(t, testPool)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-fail-exec"
	consumerID := "consumer-fail-exec-1"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-fail"}`))

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

	// Agent decides to fail the execution
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":  "fail",
			"error": "unrecoverable agent error",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit fail intent: status %d, body: %s", status, body)
	}

	var intentResult domain.IntentResult
	if err := json.Unmarshal(body, &intentResult); err != nil {
		t.Fatalf("decode intent result: %v", err)
	}
	if !intentResult.Accepted {
		t.Fatalf("fail intent not accepted: %s", intentResult.Error)
	}

	waitForStatus(t, hc, execID, domain.ExecutionFailed, 5*time.Second)

	events := getEvents(t, hc, execID)
	requireEventTrail(t, events, []domain.EventType{
		domain.EventExecutionCreated,
		domain.EventExecutionStarted,
		domain.EventIntentAccepted,
		domain.EventExecutionFailed,
	})

	// Verify that attempting to complete after failure is rejected
	status, _ = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "too late"},
		},
	})
	if status == http.StatusOK {
		t.Fatalf("expected intent on terminal execution to fail, got 200")
	}
}

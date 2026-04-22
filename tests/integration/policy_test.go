//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/policy"
)

// TestPolicyDenial verifies that a glob-based deny rule blocks a tool invocation,
// emits intent.denied without creating a step, and that the agent can still complete.
func TestPolicyDenial(t *testing.T) {
	denyEngine := buildDenyDangerousPolicy(t)
	ts := startServerWithPolicy(t, testPool, denyEngine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-policy"
	consumerID := "consumer-policy"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"test-policy"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "dangerous.delete_all",
			"arguments": map[string]string{
				"target": "everything",
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

	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventIntentDenied) {
		t.Fatalf("expected intent.denied in event trail: %v", eventTypes(events))
	}
	if containsEventType(events, domain.EventStepCreated) {
		t.Fatalf("expected no step.created in event trail: %v", eventTypes(events))
	}

	// Agent can still complete (lifecycle actions are allowed)
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "denied as expected"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete intent: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

// TestPolicyAllowedToolPassesDenyGlob verifies that a tool not matching the deny
// glob is allowed through and creates a step.
func TestPolicyAllowedToolPassesDenyGlob(t *testing.T) {
	denyEngine := buildDenyDangerousPolicy(t)
	ts := startServerWithPolicy(t, testPool, denyEngine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-policy-allow"
	consumerID := "consumer-policy-allow"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"allowed-tool"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// "safe.read_file" does not match "dangerous.*" — should be allowed
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "safe.read_file",
			"arguments": map[string]string{
				"path": "/tmp/test.txt",
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
		t.Fatalf("expected intent to be accepted, got error: %s", intentResult.Error)
	}
	if intentResult.StepID == "" {
		t.Fatalf("expected step_id in accepted result")
	}

	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventStepCreated) {
		t.Fatalf("expected step.created in event trail: %v", eventTypes(events))
	}
	if containsEventType(events, domain.EventIntentDenied) {
		t.Fatalf("should not contain intent.denied: %v", eventTypes(events))
	}
}

// TestPolicyDenyWithArgumentInspection verifies that argument predicates are
// evaluated: a tool is denied when an argument matches a forbidden pattern,
// but allowed when the argument does not match.
func TestPolicyDenyWithArgumentInspection(t *testing.T) {
	engine := buildArgumentInspectionPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-arg-inspect"
	consumerID := "consumer-arg-inspect"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"arg-inspect"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// Subtest: forbidden path pattern should be denied
	t.Run("denied_by_argument_pattern", func(t *testing.T) {
		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "fs.write_file",
				"arguments": map[string]string{
					"path":    "/etc/passwd",
					"content": "evil",
				},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("submit intent: status %d, body: %s", status, body)
		}

		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.Accepted {
			t.Fatalf("expected denial for /etc/passwd path")
		}
	})

	// Subtest: safe path should be allowed
	t.Run("allowed_by_argument_pattern", func(t *testing.T) {
		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "fs.write_file",
				"arguments": map[string]string{
					"path":    "/home/user/notes.txt",
					"content": "hello",
				},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("submit intent: status %d, body: %s", status, body)
		}

		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !result.Accepted {
			t.Fatalf("expected allowed for safe path, got error: %s", result.Error)
		}
	})
}

// TestPolicyRequireApprovalGranted exercises the full require_approval flow:
// intent -> blocked -> signal approve -> resumed -> complete.
func TestPolicyRequireApprovalGranted(t *testing.T) {
	engine := buildRequireApprovalPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-approval"
	consumerID := "consumer-approval"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"approval-test"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// Submit intent for a tool that requires approval
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "deploy.production",
			"arguments": map[string]string{
				"version": "v1.2.3",
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
		t.Fatalf("expected accepted with pending_approval, got error: %s", intentResult.Error)
	}
	if !intentResult.PendingApproval {
		t.Fatalf("expected pending_approval=true")
	}
	stepID := intentResult.StepID

	// Execution should be blocked
	waitForStatus(t, hc, execID, domain.ExecutionBlocked, 5*time.Second)

	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventStepApprovalRequired) {
		t.Fatalf("expected step.approval_required in events: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionBlocked) {
		t.Fatalf("expected execution.blocked in events: %v", eventTypes(events))
	}

	// Send approval signal
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "step.approve",
		"payload": map[string]any{
			"step_id":  stepID,
			"approved": true,
		},
	})
	if status != http.StatusOK {
		t.Fatalf("send approval signal: status %d, body: %s", status, body)
	}

	// Execution should resume to running
	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)

	events = getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventExecutionResumed) {
		t.Fatalf("expected execution.resumed after approval: %v", eventTypes(events))
	}

	// Agent reports the step as complete so the execution has no active steps.
	status, body = hc.postJSON(t, "/v0/agents/step-result", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"step_id":      stepID,
		"success":      true,
		"data":         map[string]string{"deployed": "v1.2.3"},
	})
	if status != http.StatusOK {
		t.Fatalf("submit step result: status %d, body: %s", status, body)
	}

	// Agent completes the execution
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"deployed": "v1.2.3"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

// TestPolicyRequireApprovalDenied exercises the approval flow where the human
// rejects the pending step: intent -> blocked -> signal deny -> resumed -> agent can continue.
func TestPolicyRequireApprovalDenied(t *testing.T) {
	engine := buildRequireApprovalPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-approval-deny"
	consumerID := "consumer-approval-deny"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"approval-deny"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "deploy.production",
			"arguments": map[string]string{
				"version": "v2.0.0",
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
	if !intentResult.PendingApproval {
		t.Fatalf("expected pending_approval=true")
	}
	stepID := intentResult.StepID

	waitForStatus(t, hc, execID, domain.ExecutionBlocked, 5*time.Second)

	// Deny the approval
	status, body = hc.postJSON(t, "/v0/executions/"+execID+"/signal", map[string]any{
		"signal_type": "step.approve",
		"payload": map[string]any{
			"step_id":  stepID,
			"approved": false,
			"reason":   "not ready for production",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("send denial signal: status %d, body: %s", status, body)
	}

	// Execution should resume (step failed, but execution continues)
	waitForStatus(t, hc, execID, domain.ExecutionRunning, 5*time.Second)

	events := getEvents(t, hc, execID)
	if !containsEventType(events, domain.EventStepFailed) {
		t.Fatalf("expected step.failed after approval denial: %v", eventTypes(events))
	}
	if !containsEventType(events, domain.EventExecutionResumed) {
		t.Fatalf("expected execution.resumed after approval denial: %v", eventTypes(events))
	}

	// Agent can still complete
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "approval denied, completed gracefully"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

// TestPolicyPriorityOrdering verifies that a lower-priority (numerically smaller)
// deny rule takes precedence over a higher-priority allow rule when both match.
func TestPolicyPriorityOrdering(t *testing.T) {
	engine := buildPriorityTestPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-priority"
	consumerID := "consumer-priority"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"priority-test"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// "admin.delete" matches both the priority-0 deny-admin rule and the
	// priority-50 allow-all rule. The deny should win because it has lower priority number.
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "admin.delete",
			"arguments": map[string]string{
				"resource": "user-123",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit intent: status %d, body: %s", status, body)
	}

	var result domain.IntentResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Accepted {
		t.Fatalf("expected denial due to higher priority deny rule")
	}

	// Verify the deny event references the correct rule
	events := getEvents(t, hc, execID)
	for _, e := range events {
		if e.Type == domain.EventIntentDenied {
			var payload domain.IntentDeniedPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("decode denied payload: %v", err)
			}
			if payload.RuleID != "deny-admin" {
				t.Fatalf("expected rule_id=deny-admin, got %q", payload.RuleID)
			}
			return
		}
	}
	t.Fatalf("intent.denied event not found in: %v", eventTypes(events))
}

// TestPolicyWithLabels verifies that label-based conditions match execution labels,
// denying a tool when labels match and allowing it when they do not.
func TestPolicyWithLabels(t *testing.T) {
	engine := buildLabelPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-labels"
	consumerID := "consumer-labels"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	// Create execution with env=production label
	status, body := hc.postJSON(t, "/v0/executions", map[string]any{
		"agent_id": agentID,
		"input":    json.RawMessage(`{"task":"label-test"}`),
		"labels":   map[string]string{"env": "production"},
	})
	if status != http.StatusCreated {
		t.Fatalf("create execution: status %d, body: %s", status, body)
	}

	var createResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	execID := createResp.ID
	cleanupExecution(t, testPool, execID)

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// The label policy denies destructive tools in env=production
	t.Run("denied_in_production", func(t *testing.T) {
		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "db.drop_table",
				"arguments": map[string]string{
					"table": "users",
				},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("submit intent: status %d, body: %s", status, body)
		}

		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.Accepted {
			t.Fatalf("expected denial for destructive tool in production")
		}
	})

	// Non-destructive tool in production should be allowed
	t.Run("allowed_non_destructive_in_production", func(t *testing.T) {
		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "db.select",
				"arguments": map[string]string{
					"query": "SELECT 1",
				},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("submit intent: status %d, body: %s", status, body)
		}

		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !result.Accepted {
			t.Fatalf("expected allowed for non-destructive tool, got: %s", result.Error)
		}
	})
}

// TestPolicyAgentScopedVsGlobal verifies that agent-specific rules override global
// rules, and that an unknown agent falls back to the global policy.
func TestPolicyAgentScopedVsGlobal(t *testing.T) {
	engine := buildAgentScopedPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	// Subtest: agent with specific rules — "special-agent" allows "secret.read"
	t.Run("agent_specific_allows", func(t *testing.T) {
		agentID := "special-agent"
		consumerID := "consumer-scoped-allow"

		sse := connectAgentSSE(t, ts.URL, agentID, consumerID)
		execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"scoped"}`))

		evt := sse.readEvent(t, 5*time.Second)
		if evt.Type != "execution.assigned" {
			t.Fatalf("expected execution.assigned, got %q", evt.Type)
		}

		var claim struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
			t.Fatalf("decode claim: %v", err)
		}

		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "secret.read",
			},
		})
		if status != http.StatusOK {
			t.Fatalf("submit intent: status %d, body: %s", status, body)
		}

		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !result.Accepted {
			t.Fatalf("expected special-agent to be allowed secret.read, got: %s", result.Error)
		}
	})

	// Subtest: unknown agent falls back to global — "secret.read" is denied globally
	t.Run("unknown_agent_uses_global_deny", func(t *testing.T) {
		agentID := "unknown-agent"
		consumerID := "consumer-scoped-deny"

		sse := connectAgentSSE(t, ts.URL, agentID, consumerID)
		execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"global-deny"}`))

		evt := sse.readEvent(t, 5*time.Second)
		if evt.Type != "execution.assigned" {
			t.Fatalf("expected execution.assigned, got %q", evt.Type)
		}

		var claim struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
			t.Fatalf("decode claim: %v", err)
		}

		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "secret.read",
			},
		})
		if status != http.StatusOK {
			t.Fatalf("submit intent: status %d, body: %s", status, body)
		}

		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.Accepted {
			t.Fatalf("expected unknown-agent to be denied secret.read by global policy")
		}
	})
}

// TestPolicyDefaultDenyWhenNoRulesMatch verifies that when no rules match and the
// default decision is deny, the intent is rejected.
func TestPolicyDefaultDenyWhenNoRulesMatch(t *testing.T) {
	engine := buildDefaultDenyPolicy(t)
	ts := startServerWithPolicy(t, testPool, engine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-default-deny"
	consumerID := "consumer-default-deny"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"default-deny"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// Only "safe.read" is explicitly allowed; everything else falls to default deny
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "unknown.tool",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit intent: status %d, body: %s", status, body)
	}

	var result domain.IntentResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Accepted {
		t.Fatalf("expected default deny for unmatched tool")
	}

	// The explicitly allowed tool should pass
	status, body = hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":    "invoke_tool",
			"tool_id": "safe.read",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit intent: status %d, body: %s", status, body)
	}

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected safe.read to be allowed, got: %s", result.Error)
	}
}

// TestPolicyMultipleDenialsDoNotCorruptState verifies that an agent can submit
// multiple denied intents in a row and the execution remains healthy, with each
// denial recorded as a separate event.
func TestPolicyMultipleDenialsDoNotCorruptState(t *testing.T) {
	denyEngine := buildDenyDangerousPolicy(t)
	ts := startServerWithPolicy(t, testPool, denyEngine)
	hc := newHTTPClient(ts.URL)

	agentID := "agent-multi-deny"
	consumerID := "consumer-multi-deny"

	sse := connectAgentSSE(t, ts.URL, agentID, consumerID)

	execID := createExecution(t, hc, testPool, agentID, json.RawMessage(`{"task":"multi-deny"}`))

	evt := sse.readEvent(t, 5*time.Second)
	if evt.Type != "execution.assigned" {
		t.Fatalf("expected execution.assigned, got %q", evt.Type)
	}

	var claim struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(evt.Data), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	// Submit 3 denied intents in sequence
	for i := 0; i < 3; i++ {
		status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
			"execution_id": execID,
			"session_id":   claim.SessionID,
			"intent": map[string]any{
				"type":    "invoke_tool",
				"tool_id": "dangerous.nuke",
			},
		})
		if status != http.StatusOK {
			t.Fatalf("intent %d: status %d, body: %s", i, status, body)
		}
		var result domain.IntentResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if result.Accepted {
			t.Fatalf("intent %d: expected denial", i)
		}
	}

	// Count intent.denied events
	events := getEvents(t, hc, execID)
	deniedCount := 0
	for _, e := range events {
		if e.Type == domain.EventIntentDenied {
			deniedCount++
		}
	}
	if deniedCount != 3 {
		t.Fatalf("expected 3 intent.denied events, got %d in: %v", deniedCount, eventTypes(events))
	}

	// Execution is still healthy — can complete
	status, body := hc.postJSON(t, "/v0/agents/intent", map[string]any{
		"execution_id": execID,
		"session_id":   claim.SessionID,
		"intent": map[string]any{
			"type":   "complete",
			"output": map[string]string{"result": "survived denials"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("submit complete: status %d, body: %s", status, body)
	}

	waitForStatus(t, hc, execID, domain.ExecutionCompleted, 5*time.Second)
}

// --- Policy builders ---

func buildDenyDangerousPolicy(t *testing.T) policy.Engine {
	t.Helper()
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
				Priority: 100,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "allow everything else",
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
		t.Fatalf("build deny policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

func buildArgumentInspectionPolicy(t *testing.T) policy.Engine {
	t.Helper()
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-etc-writes",
				Priority: 0,
				When: domain.PolicyCondition{
					ToolID: "fs.write_file",
					Arguments: []domain.ArgumentPredicate{
						{
							Field:   "path",
							Pattern: "^/etc/.*",
						},
					},
				},
				Then: domain.PolicyAction{
					Decision: domain.PolicyDeny,
					Reason:   "writing to /etc is forbidden",
				},
			},
			{
				ID:       "allow-all",
				Priority: 100,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "allow everything else",
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
		t.Fatalf("build argument inspection policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

func buildRequireApprovalPolicy(t *testing.T) policy.Engine {
	t.Helper()
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "approve-deploy",
				Priority: 0,
				When: domain.PolicyCondition{
					ToolID: "deploy.*",
				},
				Then: domain.PolicyAction{
					Decision: domain.PolicyRequireApproval,
					Reason:   "deployment requires human approval",
				},
			},
			{
				ID:       "allow-rest",
				Priority: 100,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "allow everything else",
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
		t.Fatalf("build require approval policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

func buildPriorityTestPolicy(t *testing.T) policy.Engine {
	t.Helper()
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-admin",
				Priority: 0,
				When: domain.PolicyCondition{
					ToolID: "admin.*",
				},
				Then: domain.PolicyAction{
					Decision: domain.PolicyDeny,
					Reason:   "admin tools are forbidden",
				},
			},
			{
				ID:       "allow-all-wildcard",
				Priority: 50,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "allow all (lower priority)",
				},
			},
		},
		Default: domain.PolicyAction{
			Decision: domain.PolicyDeny,
			Reason:   "default deny",
		},
	}
	engine, err := policy.NewRuleEngine(cfg)
	if err != nil {
		t.Fatalf("build priority test policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

func buildLabelPolicy(t *testing.T) policy.Engine {
	t.Helper()
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-destructive-production",
				Priority: 0,
				When: domain.PolicyCondition{
					ToolID: "db.drop_*",
					Labels: map[string]string{"env": "production"},
				},
				Then: domain.PolicyAction{
					Decision: domain.PolicyDeny,
					Reason:   "destructive DB operations forbidden in production",
				},
			},
			{
				ID:       "allow-rest",
				Priority: 100,
				When:     domain.PolicyCondition{},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "allow everything else",
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
		t.Fatalf("build label policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

func buildAgentScopedPolicy(t *testing.T) policy.Engine {
	t.Helper()
	// Build an AgentEngine that simulates _global.yaml + special-agent.yaml
	result := &policy.LoadDirResult{
		Agents: map[string]*policy.PolicyConfig{
			"special-agent": {
				Rules: []domain.PolicyRule{
					{
						ID:       "allow-secret-read",
						Priority: 0,
						When: domain.PolicyCondition{
							ToolID: "secret.read",
						},
						Then: domain.PolicyAction{
							Decision: domain.PolicyAllow,
							Reason:   "special agent may read secrets",
						},
					},
				},
				Default: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "default allow for special agent",
				},
			},
		},
		Global: &policy.PolicyConfig{
			Rules: []domain.PolicyRule{
				{
					ID:       "global-deny-secrets",
					Priority: 50,
					When: domain.PolicyCondition{
						ToolID: "secret.*",
					},
					Then: domain.PolicyAction{
						Decision: domain.PolicyDeny,
						Reason:   "secrets are forbidden by default",
					},
				},
				{
					ID:       "global-allow-rest",
					Priority: 100,
					When:     domain.PolicyCondition{},
					Then: domain.PolicyAction{
						Decision: domain.PolicyAllow,
						Reason:   "allow everything else",
					},
				},
			},
			Default: domain.PolicyAction{
				Decision: domain.PolicyDeny,
				Reason:   "global default deny",
			},
		},
	}

	engine, err := policy.NewAgentEngine(result)
	if err != nil {
		t.Fatalf("build agent-scoped policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

func buildDefaultDenyPolicy(t *testing.T) policy.Engine {
	t.Helper()
	cfg := policy.PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-safe-read",
				Priority: 0,
				When: domain.PolicyCondition{
					ToolID: "safe.read",
				},
				Then: domain.PolicyAction{
					Decision: domain.PolicyAllow,
					Reason:   "safe.read is explicitly allowed",
				},
			},
		},
		Default: domain.PolicyAction{
			Decision: domain.PolicyDeny,
			Reason:   "deny by default",
		},
	}
	engine, err := policy.NewRuleEngine(cfg)
	if err != nil {
		t.Fatalf("build default deny policy: %v", err)
	}
	return policy.NewSecureDefaultEngine(engine)
}

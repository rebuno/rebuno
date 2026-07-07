package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/api"
	"github.com/rebuno/kernel/internal/dispatcher"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/identity"
	"github.com/rebuno/kernel/internal/kernel"
	"github.com/rebuno/kernel/internal/policy"
	"github.com/rebuno/kernel/internal/store/memstore"
)

const testAgentID = "agent-1"
const testAgentSecret = "secret"

func setupRouter(t *testing.T) (http.Handler, *kernel.Kernel, context.Context) {
	t.Helper()
	ms := memstore.NewStore()
	k := kernel.New(kernel.DefaultConfig(), kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms, Policy: policy.NewBundleResolver(ms, policy.PermissiveEngine{}),
	})
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: testAgentID, WebhookURL: "http://localhost", Secret: testAgentSecret}); err != nil {
		t.Fatal(err)
	}
	adapt := &api.KernelAPI{Inner: k}
	mux := api.NewRouter(adapt, adapt, adapt, "", nil)
	return mux, k, ctx
}

// signAgentRequest adds Rebuno-Agent-Id and Rebuno-Signature headers computed
// over the exact request body bytes.
func signAgentRequest(req *http.Request, body []byte) {
	req.Header.Set("Rebuno-Agent-Id", testAgentID)
	req.Header.Set("Rebuno-Signature", "sha256="+dispatcher.SignPayload(testAgentSecret, body))
}

func TestCreateExecutionViaHTTP(t *testing.T) {
	mux, _, _ := setupRouter(t)
	body, _ := json.Marshal(map[string]any{"agent_id": "agent-1", "input": map[string]string{"msg": "hi"}})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var exec domain.Execution
	if err := json.Unmarshal(rr.Body.Bytes(), &exec); err != nil {
		t.Fatal(err)
	}
	if exec.AgentID != "agent-1" {
		t.Fatal("agent id mismatch")
	}
}

func TestAgentSubmitAndCompleteViaHTTP(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := computeStepID(t, exec.ID, domain.StepKindTool, "read", args, 0)

	submit := map[string]any{"kind": "tool_call", "target": "read", "args": json.RawMessage(args)}
	body, _ := json.Marshal(submit)
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	req.Header.Set("Rebuno-Step-Id", stepID)
	signAgentRequest(req, body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit failed: %d %s", rr.Code, rr.Body.String())
	}
	var dec domain.StepDecision
	json.Unmarshal(rr.Body.Bytes(), &dec)
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}

	comp := map[string]any{"result": map[string]bool{"ok": true}}
	body, _ = json.Marshal(comp)
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps/"+stepID+"/complete", bytes.NewReader(body))
	signAgentRequest(req, body)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("complete failed: %d %s", rr.Code, rr.Body.String())
	}

	// Replay via GET step
	req = httptest.NewRequest(http.MethodGet, "/v0/executions/"+exec.ID.String()+"/steps/"+stepID, nil)
	signAgentRequest(req, nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get step failed: %d %s", rr.Code, rr.Body.String())
	}
	var step domain.Step
	json.Unmarshal(rr.Body.Bytes(), &step)
	if step.Status != domain.StepSucceeded {
		t.Fatalf("expected succeeded, got %s", step.Status)
	}
}

func TestListStepsTerminalFilter(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// Step 1: submit + complete -> terminal (succeeded).
	doneArgs := json.RawMessage(`{"path":"/a"}`)
	doneID := computeStepID(t, exec.ID, domain.StepKindTool, "read", doneArgs, 0)
	submitStepHTTP(t, mux, exec.ID, "read", doneArgs, doneID)
	completeStepHTTP(t, mux, exec.ID, doneID)

	// Step 2: submit only -> non-terminal (executing).
	openArgs := json.RawMessage(`{"path":"/b"}`)
	openID := computeStepID(t, exec.ID, domain.StepKindTool, "read", openArgs, 0)
	submitStepHTTP(t, mux, exec.ID, "read", openArgs, openID)

	// Unfiltered: both steps.
	if got := listStepsHTTP(t, mux, exec.ID, ""); len(got) != 2 {
		t.Fatalf("expected 2 steps unfiltered, got %d", len(got))
	}

	// status=terminal: only the completed step.
	terminal := listStepsHTTP(t, mux, exec.ID, "terminal")
	if len(terminal) != 1 {
		t.Fatalf("expected 1 terminal step, got %d", len(terminal))
	}
	if terminal[0].StepID != doneID || terminal[0].Status != domain.StepSucceeded {
		t.Fatalf("unexpected terminal step: %+v", terminal[0])
	}
}

func TestStepsReachableViaBearerAuth(t *testing.T) {
	ms := memstore.NewStore()
	k := kernel.New(kernel.DefaultConfig(), kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: policy.NewBundleResolver(ms, policy.PermissiveEngine{}),
	})
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: testAgentID, WebhookURL: "http://localhost", Secret: testAgentSecret}); err != nil {
		t.Fatal(err)
	}
	adapt := &api.KernelAPI{Inner: k}
	mux := api.NewRouter(adapt, adapt, adapt, "tok", nil)

	exec, _ := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`), "")
	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := computeStepID(t, exec.ID, domain.StepKindTool, "read", args, 0)
	submitStepHTTP(t, mux, exec.ID, "read", args, stepID) // submitted as the agent, over HMAC

	// listSteps via bearer token (no HMAC headers) must succeed.
	req := httptest.NewRequest(http.MethodGet, "/v0/executions/"+exec.ID.String()+"/steps", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for bearer-authed listSteps, got %d: %s", rr.Code, rr.Body.String())
	}

	// getStep via bearer token must also succeed.
	req = httptest.NewRequest(http.MethodGet, "/v0/executions/"+exec.ID.String()+"/steps/"+stepID, nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for bearer-authed getStep, got %d: %s", rr.Code, rr.Body.String())
	}
}

func submitStepHTTP(t *testing.T, mux http.Handler, execID uuid.UUID, target string, args json.RawMessage, stepID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"kind": "tool_call", "target": target, "args": args})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID.String()+"/steps", bytes.NewReader(body))
	req.Header.Set("Rebuno-Step-Id", stepID)
	signAgentRequest(req, body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit failed: %d %s", rr.Code, rr.Body.String())
	}
}

func completeStepHTTP(t *testing.T, mux http.Handler, execID uuid.UUID, stepID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"result": map[string]bool{"ok": true}})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID.String()+"/steps/"+stepID+"/complete", bytes.NewReader(body))
	signAgentRequest(req, body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("complete failed: %d %s", rr.Code, rr.Body.String())
	}
}

func listStepsHTTP(t *testing.T, mux http.Handler, execID uuid.UUID, status string) []domain.Step {
	t.Helper()
	path := "/v0/executions/" + execID.String() + "/steps"
	if status != "" {
		path += "?status=" + status
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	signAgentRequest(req, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list steps failed: %d %s", rr.Code, rr.Body.String())
	}
	var steps []domain.Step
	if err := json.Unmarshal(rr.Body.Bytes(), &steps); err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestAgentHMACRejectsBadSignature(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	body, _ := json.Marshal(map[string]any{"kind": "tool_call", "target": "read", "args": map[string]string{"path": "/tmp"}})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	req.Header.Set("Rebuno-Agent-Id", testAgentID)
	req.Header.Set("Rebuno-Signature", "sha256=badbadbad")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad signature, got %d", rr.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	ms := memstore.NewStore()
	k := kernel.New(kernel.DefaultConfig(), kernel.Deps{Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms})
	adapt := &api.KernelAPI{Inner: k}
	mux := api.NewRouter(adapt, adapt, adapt, "tok", nil)
	req := httptest.NewRequest(http.MethodGet, "/v0/approvals", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	req.Header.Set("Authorization", "Bearer tok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestAdminLoadPolicyBundle(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	exec, _ := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`), "")

	bundle := `
rules:
  - id: allow-prod
    priority: 10
    when:
      target: read
      arguments:
        env:
          regex: "^prod-.*"
    then:
      decision: allow
default_action: deny
`

	load := api.LoadPolicyRequest{Bundle: bundle}
	body, _ := json.Marshal(load)
	req := httptest.NewRequest(http.MethodPost, "/v0/policies/"+url.PathEscape(testAgentID), bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("load policy failed: %d %s", rr.Code, rr.Body.String())
	}

	// Non-matching argument should be denied by the loaded bundle.
	args := json.RawMessage(`{"env":"staging-123"}`)
	stepID := computeStepID(t, exec.ID, domain.StepKindTool, "read", args, 0)
	submit := map[string]any{"kind": "tool_call", "target": "read", "args": json.RawMessage(args)}
	body, _ = json.Marshal(submit)
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	req.Header.Set("Rebuno-Step-Id", stepID)
	signAgentRequest(req, body)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit denied step failed: %d %s", rr.Code, rr.Body.String())
	}
	var dec domain.StepDecision
	json.Unmarshal(rr.Body.Bytes(), &dec)
	if dec.Decision != "denied" {
		t.Fatalf("expected denied, got %s", dec.Decision)
	}

	// Matching argument should be allowed.
	args = json.RawMessage(`{"env":"prod-123"}`)
	stepID = computeStepID(t, exec.ID, domain.StepKindTool, "read", args, 0)
	submit = map[string]any{"kind": "tool_call", "target": "read", "args": json.RawMessage(args)}
	body, _ = json.Marshal(submit)
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	req.Header.Set("Rebuno-Step-Id", stepID)
	signAgentRequest(req, body)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit allowed step failed: %d %s", rr.Code, rr.Body.String())
	}
	json.Unmarshal(rr.Body.Bytes(), &dec)
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}
}

func computeStepID(t *testing.T, execID uuid.UUID, kind domain.StepKind, target string, args []byte, occ int) string {
	t.Helper()
	argsHash, err := identity.ComputeArgsHash(args)
	if err != nil {
		t.Fatal(err)
	}
	return identity.ComputeStepID(execID, kind, target, argsHash, occ)
}

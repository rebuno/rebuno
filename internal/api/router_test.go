package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/dispatcher"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
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
	mux := api.NewRouter(adapt, adapt, adapt, "", nil, nil)
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
	setLeaseHeaders(t, k, req, exec.ID)
	signAgentRequest(req, body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit failed: %d %s", rr.Code, rr.Body.String())
	}
	var dec domain.StepDecision
	_ = json.Unmarshal(rr.Body.Bytes(), &dec)
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}

	comp := map[string]any{"result": map[string]bool{"ok": true}}
	body, _ = json.Marshal(comp)
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps/"+stepID+"/complete", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, exec.ID)
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
	_ = json.Unmarshal(rr.Body.Bytes(), &step)
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
	submitStepHTTP(t, mux, k, exec.ID, "read", doneArgs)
	completeStepHTTP(t, mux, k, exec.ID, doneID)

	// Step 2: submit only -> non-terminal (executing).
	openArgs := json.RawMessage(`{"path":"/b"}`)
	submitStepHTTP(t, mux, k, exec.ID, "read", openArgs)

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
	mux := api.NewRouter(adapt, adapt, adapt, "tok", nil, nil)

	exec, _ := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`), "")
	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := computeStepID(t, exec.ID, domain.StepKindTool, "read", args, 0)
	submitStepHTTP(t, mux, k, exec.ID, "read", args) // submitted as the agent, over HMAC

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

// setLeaseHeaders carries the dispatch lease every agent mutation must present.
// It claims the execution's dispatch first, so the lease names an attempt that
// was delivered, as a real agent's would.
func setLeaseHeaders(t *testing.T, k *kernel.Kernel, req *http.Request, execID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	q := k.Deps().Queue
	ds, err := q.ListDispatchesByExecution(ctx, execID)
	if err != nil || len(ds) == 0 {
		t.Fatalf("no dispatch for execution: %v", err)
	}
	if ds[len(ds)-1].Status != domain.DispatchInFlight {
		if _, err := q.Claim(ctx, "test", 1000, time.Now().UTC()); err != nil {
			t.Fatalf("claim dispatches: %v", err)
		}
		if ds, err = q.ListDispatchesByExecution(ctx, execID); err != nil {
			t.Fatalf("list dispatches: %v", err)
		}
	}
	d := ds[len(ds)-1]
	req.Header.Set("Rebuno-Dispatch-Id", d.ID.String())
	req.Header.Set("Rebuno-Dispatch-Attempt", strconv.Itoa(d.Attempt))
}

func submitStepHTTP(t *testing.T, mux http.Handler, k *kernel.Kernel, execID uuid.UUID, target string, args json.RawMessage) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"kind": "tool_call", "target": target, "args": args})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID.String()+"/steps", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, execID)
	signAgentRequest(req, body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit failed: %d %s", rr.Code, rr.Body.String())
	}
}

func completeStepHTTP(t *testing.T, mux http.Handler, k *kernel.Kernel, execID uuid.UUID, stepID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"result": map[string]bool{"ok": true}})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID.String()+"/steps/"+stepID+"/complete", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, execID)
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
	mux := api.NewRouter(adapt, adapt, adapt, "tok", nil, nil)
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
	submit := map[string]any{"kind": "tool_call", "target": "read", "args": json.RawMessage(args)}
	body, _ = json.Marshal(submit)
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, exec.ID)
	signAgentRequest(req, body)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit denied step failed: %d %s", rr.Code, rr.Body.String())
	}
	var dec domain.StepDecision
	_ = json.Unmarshal(rr.Body.Bytes(), &dec)
	if dec.Decision != "denied" {
		t.Fatalf("expected denied, got %s", dec.Decision)
	}

	// Matching argument should be allowed.
	args = json.RawMessage(`{"env":"prod-123"}`)
	submit = map[string]any{"kind": "tool_call", "target": "read", "args": json.RawMessage(args)}
	body, _ = json.Marshal(submit)
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, exec.ID)
	signAgentRequest(req, body)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit allowed step failed: %d %s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &dec)
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

// A superseded attempt gets a 409 naming the lease, not a 400 and not a silent
// write alongside the attempt that replaced it.
func TestSupersededLeaseIsRejectedWith409(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	body, _ := json.Marshal(map[string]any{"kind": "tool_call", "target": "read", "args": json.RawMessage(`{"path":"/tmp"}`)})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, exec.ID)
	stalledAttempt := req.Header.Get("Rebuno-Dispatch-Attempt")

	// The lease stalls, is reclaimed, and the dispatch is delivered again.
	q := k.Deps().Queue
	later := time.Now().UTC().Add(time.Hour)
	if _, err := q.ReclaimStalled(ctx, later, time.Minute, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, "replica-2", 10, later); err != nil {
		t.Fatal(err)
	}

	signAgentRequest(req, body)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for the superseded attempt %s, got %d: %s", stalledAttempt, rr.Code, rr.Body.String())
	}
	var apiErr domain.APIError
	if err := json.Unmarshal(rr.Body.Bytes(), &apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Code != "lease_superseded" {
		t.Fatalf("expected lease_superseded, got %q", apiErr.Code)
	}

	// The attempt that replaced it is served normally.
	req = httptest.NewRequest(http.MethodPost, "/v0/executions/"+exec.ID.String()+"/steps", bytes.NewReader(body))
	setLeaseHeaders(t, k, req, exec.ID)
	signAgentRequest(req, body)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("live attempt must be served, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAgentResponsesOmitSecret(t *testing.T) {
	mux, _, _ := setupRouter(t)

	body, _ := json.Marshal(map[string]string{"id": "agent-2", "webhook_url": "http://localhost", "secret": testAgentSecret})
	for _, tc := range []struct {
		path string
		req  *http.Request
		want int
	}{
		{"POST /v0/agents", httptest.NewRequest(http.MethodPost, "/v0/agents", bytes.NewReader(body)), http.StatusCreated},
		{"GET /v0/agents", httptest.NewRequest(http.MethodGet, "/v0/agents", nil), http.StatusOK},
		{"GET /v0/agents/{id}", httptest.NewRequest(http.MethodGet, "/v0/agents/"+testAgentID, nil), http.StatusOK},
	} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, tc.req)
		if rr.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d: %s", tc.path, tc.want, rr.Code, rr.Body.String())
		}
		if bytes.Contains(rr.Body.Bytes(), []byte(testAgentSecret)) {
			t.Errorf("%s leaked the agent secret: %s", tc.path, rr.Body.String())
		}
	}
}

func TestRegisterAgentReturnsStoredAgent(t *testing.T) {
	mux, _, _ := setupRouter(t)
	body, _ := json.Marshal(map[string]string{"id": "agent-2", "webhook_url": "http://localhost"})
	req := httptest.NewRequest(http.MethodPost, "/v0/agents", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var agent domain.Agent
	if err := json.Unmarshal(rr.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.ID != "agent-2" {
		t.Errorf("expected id agent-2, got %q", agent.ID)
	}
	if agent.RegisteredAt.IsZero() {
		t.Error("registered_at is zero")
	}
}

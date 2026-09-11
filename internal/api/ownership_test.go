package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
)

func TestAgentCannotAccessAnotherAgentsExecution(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "other", Secret: "other-secret", WebhookURL: "http://localhost"}); err != nil {
		t.Fatal(err)
	}
	exec, err := k.CreateExecution(ctx, "other", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	step := domain.Step{StepID: "other-step", ExecutionID: exec.ID, Kind: domain.StepKindTool, Status: domain.StepExecuting}
	if err := k.Deps().Steps.Upsert(ctx, step); err != nil {
		t.Fatal(err)
	}
	setLeaseHeaders(t, k, httptest.NewRequest("POST", "/", nil), exec.ID)
	baseline, err := k.GetEvents(ctx, exec.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	base := "/v0/executions/" + exec.ID.String()
	cases := []struct{ name, method, path, body string }{
		{"input", "GET", base, ""},
		{"steps", "GET", base + "/steps", ""},
		{"step", "GET", base + "/steps/other-step", ""},
		{"submit", "POST", base + "/steps", `{"kind":"tool_call","target":"read","args":{}}`},
		{"complete step", "POST", base + "/steps/other-step/complete", `{"result":{}}`},
		{"fail step", "POST", base + "/steps/other-step/fail", `{"error":{}}`},
		{"heartbeat", "POST", base + "/heartbeat", ""},
		{"complete execution", "POST", base + "/complete", `{"output":{}}`},
		{"fail execution", "POST", base + "/fail", `{"error":"failed"}`},
		{"publish", "POST", base + "/steps/other-step/stream", `{"seq":1,"data":"foreign"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			setLeaseHeaders(t, k, req, exec.ID)
			before, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
			if err != nil {
				t.Fatal(err)
			}
			signAgentRequest(req, []byte(tc.body))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			after, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) || !before[0].LockedAt.Equal(*after[0].LockedAt) {
				t.Fatal("unauthorized request changed lease")
			}
		})
	}
	events, err := k.GetEvents(ctx, exec.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(baseline) {
		t.Fatalf("unauthorized requests appended events: %d", len(events))
	}
	got, err := k.GetStep(ctx, step.StepID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StepExecuting {
		t.Fatalf("step changed: %s", got.Status)
	}
}

func TestEventsRequireExistingExecution(t *testing.T) {
	mux, _, _ := setupRouter(t)
	req := httptest.NewRequest("GET", "/v0/executions/00000000-0000-0000-0000-000000000001/events?limit=10", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
}

func TestStepRoutesRequireMatchingExecution(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	first, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	submitStepHTTP(t, mux, k, first.ID, "read", json.RawMessage(`{}`))
	stepID := computeStepID(t, first.ID, domain.StepKindTool, "read", []byte(`{}`), 0)
	for _, tc := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"POST", "/complete", `{"result":{}}`}, {"POST", "/fail", `{"error":{}}`}, {"POST", "/stream", `{"seq":1,"data":"x"}`},
	} {
		t.Run(tc.method+tc.suffix, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/v0/executions/"+second.ID.String()+"/steps/"+stepID+tc.suffix, bytes.NewBufferString(tc.body))
			// A valid lease for the step's actual execution cannot authorize a mismatched URL.
			setLeaseHeaders(t, k, req, first.ID)
			signAgentRequest(req, []byte(tc.body))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestStreamSubscriptionRequiresExistingExecutionAndBearer(t *testing.T) {
	adapt, k := setupKernel(t)
	exec, err := k.CreateExecution(context.Background(), testAgentID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	mux := api.NewRouter(adapt, adapt, adapt, "admin-token", nil, nil)
	req := httptest.NewRequest("GET", "/v0/executions/"+exec.ID.String()+"/stream", nil)
	signAgentRequest(req, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rr.Code)
	}
	req = httptest.NewRequest("GET", "/v0/executions/00000000-0000-0000-0000-000000000001/stream", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestAdministrativeRoutesRequireBearer(t *testing.T) {
	adapt, _ := setupKernel(t)
	mux := api.NewRouter(adapt, adapt, adapt, "admin-token", nil, nil)
	for _, route := range []struct{ method, path string }{
		{"POST", "/v0/executions"},
		{"GET", "/v0/executions"},
		{"GET", "/v0/executions/id/events"},
		{"POST", "/v0/executions/id/cancel"},
		{"POST", "/v0/agents"},
		{"GET", "/v0/agents"},
		{"GET", "/v0/agents/agent-1"},
		{"DELETE", "/v0/agents/agent-1"},
		{"POST", "/v0/policies/agent-1"},
		{"POST", "/v0/policies/agent-1/test"},
		{"GET", "/v0/approvals"},
		{"GET", "/v0/approvals/id"},
		{"POST", "/v0/approvals/id/grant"},
		{"POST", "/v0/approvals/id/deny"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			for _, agentSignature := range []bool{false, true} {
				req := httptest.NewRequest(route.method, route.path, nil)
				if agentSignature {
					signAgentRequest(req, nil)
				}
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)
				if rr.Code != http.StatusUnauthorized {
					t.Fatalf("agent signature %v: status %d: %s", agentSignature, rr.Code, rr.Body.String())
				}
			}
		})
	}
	req := httptest.NewRequest("GET", "/v0/agents", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bearer status %d: %s", rr.Code, rr.Body.String())
	}
}

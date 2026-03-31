package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/store"
)

type stubEventStore struct {
	events     map[string][]domain.Event
	executions map[string]*domain.ExecutionSummary
}

func newStubEventStore() *stubEventStore {
	return &stubEventStore{
		events:     make(map[string][]domain.Event),
		executions: make(map[string]*domain.ExecutionSummary),
	}
}

func (s *stubEventStore) Append(_ context.Context, event domain.Event) error {
	seq := int64(len(s.events[event.ExecutionID]) + 1)
	event.Sequence = seq
	s.events[event.ExecutionID] = append(s.events[event.ExecutionID], event)
	return nil
}

func (s *stubEventStore) AppendBatch(_ context.Context, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	execID := events[0].ExecutionID
	base := int64(len(s.events[execID]))
	for i := range events {
		events[i].Sequence = base + int64(i) + 1
		s.events[execID] = append(s.events[execID], events[i])
	}
	return nil
}

func (s *stubEventStore) GetByExecution(_ context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error) {
	var result []domain.Event
	for _, e := range s.events[executionID] {
		if e.Sequence > afterSequence {
			result = append(result, e)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *stubEventStore) GetLatestSequence(_ context.Context, executionID string) (int64, error) {
	return int64(len(s.events[executionID])), nil
}

func (s *stubEventStore) ListActiveExecutionIDs(_ context.Context) ([]string, error) {
	return nil, nil
}

func (s *stubEventStore) ListExecutions(_ context.Context, filter domain.ExecutionFilter, _ string, _ int) ([]domain.ExecutionSummary, string, error) {
	if filter.Status != "" {
		valid := map[domain.ExecutionStatus]bool{
			domain.ExecutionPending:   true,
			domain.ExecutionRunning:   true,
			domain.ExecutionBlocked:   true,
			domain.ExecutionCompleted: true,
			domain.ExecutionFailed:    true,
			domain.ExecutionCancelled: true,
		}
		if !valid[filter.Status] {
			return nil, "", domain.ErrValidation
		}
	}
	return nil, "", nil
}

func (s *stubEventStore) GetExecution(_ context.Context, executionID string) (*domain.ExecutionSummary, error) {
	e, ok := s.executions[executionID]
	if !ok {
		return nil, nil
	}
	return e, nil
}

func (s *stubEventStore) CreateExecution(_ context.Context, id, agentID string) error {
	s.executions[id] = &domain.ExecutionSummary{
		ID:      id,
		AgentID: agentID,
		Status:  domain.ExecutionPending,
	}
	return nil
}

func (s *stubEventStore) UpdateExecutionStatus(_ context.Context, executionID string, status domain.ExecutionStatus) error {
	if e, ok := s.executions[executionID]; ok {
		e.Status = status
	}
	return nil
}

func (s *stubEventStore) DeleteExecution(_ context.Context, executionID string) error {
	delete(s.executions, executionID)
	delete(s.events, executionID)
	return nil
}

func (s *stubEventStore) ListTerminalExecutions(_ context.Context, _ int64, _ int) ([]string, error) {
	return nil, nil
}

type stubCheckpointStore struct{}

func (s *stubCheckpointStore) Get(_ context.Context, _ string) (*domain.Checkpoint, bool, error) {
	return nil, false, nil
}
func (s *stubCheckpointStore) Save(_ context.Context, _ domain.Checkpoint) error { return nil }
func (s *stubCheckpointStore) Delete(_ context.Context, _ string) error          { return nil }

type stubAgentHub struct{}

func (s *stubAgentHub) Send(_ string, _ store.AgentMessage) bool          { return false }
func (s *stubAgentHub) SendTo(_, _ string, _ store.AgentMessage) bool     { return false }
func (s *stubAgentHub) SendToSession(_ string, _ store.AgentMessage) bool { return false }
func (s *stubAgentHub) PickConnection(_ string) (store.ConnInfo, bool) {
	return store.ConnInfo{}, false
}
func (s *stubAgentHub) HasConnections(_ string) bool { return false }

type stubRunnerHub struct{}

func (s *stubRunnerHub) Dispatch(_ string, _ store.RunnerMessage) (store.RunnerConnInfo, bool) {
	return store.RunnerConnInfo{}, false
}
func (s *stubRunnerHub) SendTo(_, _ string, _ store.RunnerMessage) bool { return false }
func (s *stubRunnerHub) MarkBusy(_, _ string)                           {}
func (s *stubRunnerHub) MarkIdle(_, _ string)                           {}
func (s *stubRunnerHub) MarkRunnerIdle(_ string)                        {}
func (s *stubRunnerHub) HasCapability(_ string) bool                    { return false }
func (s *stubRunnerHub) UpdateCapabilities(_ string, _ []string)        {}

type stubSignalStore struct{}

func (s *stubSignalStore) Publish(_ context.Context, _ string, _ domain.Signal) error { return nil }
func (s *stubSignalStore) GetPending(_ context.Context, _ string) ([]domain.Signal, error) {
	return nil, nil
}
func (s *stubSignalStore) Clear(_ context.Context, _ string) error { return nil }

type stubSessionStore struct {
	sessions map[string]*domain.Session
}

func newStubSessionStore() *stubSessionStore {
	return &stubSessionStore{sessions: make(map[string]*domain.Session)}
}

func (s *stubSessionStore) Create(_ context.Context, session domain.Session) error {
	s.sessions[session.ID] = &session
	return nil
}
func (s *stubSessionStore) Get(_ context.Context, id string) (*domain.Session, bool, error) {
	sess, ok := s.sessions[id]
	return sess, ok, nil
}
func (s *stubSessionStore) GetByExecution(_ context.Context, execID string) (*domain.Session, bool, error) {
	for _, sess := range s.sessions {
		if sess.ExecutionID == execID {
			return sess, true, nil
		}
	}
	return nil, false, nil
}
func (s *stubSessionStore) Extend(_ context.Context, id string, d time.Duration) error {
	if sess, ok := s.sessions[id]; ok {
		sess.ExpiresAt = time.Now().Add(d)
	}
	return nil
}
func (s *stubSessionStore) Delete(_ context.Context, id string) error {
	delete(s.sessions, id)
	return nil
}
func (s *stubSessionStore) DeleteAll(_ context.Context) (int, error) {
	count := len(s.sessions)
	s.sessions = make(map[string]*domain.Session)
	return count, nil
}

func (s *stubSessionStore) DeleteExpired(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

type stubRunnerStore struct{}

func (s *stubRunnerStore) Register(_ context.Context, _ domain.Runner) error { return nil }
func (s *stubRunnerStore) Get(_ context.Context, _ string) (*domain.Runner, bool, error) {
	return nil, false, nil
}
func (s *stubRunnerStore) List(_ context.Context) ([]domain.Runner, error)   { return nil, nil }
func (s *stubRunnerStore) UpdateHeartbeat(_ context.Context, _ string) error { return nil }
func (s *stubRunnerStore) Delete(_ context.Context, _ string) error          { return nil }

type stubLocker struct{}

func (s *stubLocker) Acquire(_ context.Context, _ string) (func(), error) {
	return func() {}, nil
}

type stubPolicy struct{}

func (s *stubPolicy) Evaluate(_ context.Context, _ domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.PolicyAllow, Reason: "test"}, nil
}

func newTestKernel() *kernel.Kernel {
	return kernel.NewKernel(kernel.Deps{
		Events:      newStubEventStore(),
		Checkpoints: &stubCheckpointStore{},
		AgentHub:    &stubAgentHub{},
		RunnerHub:   &stubRunnerHub{},
		Signals:     &stubSignalStore{},
		Sessions:    newStubSessionStore(),
		Runners:     &stubRunnerStore{},
		Locker:      &stubLocker{},
		Policy:      &stubPolicy{},
	})
}

func TestCreateExecution_MissingAgentID(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	body, _ := json.Marshal(map[string]any{
		"agent_id": "",
		"input":    map[string]string{"task": "test"},
	})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateExecution_InvalidJSON(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	req := httptest.NewRequest(http.MethodPost, "/v0/executions", bytes.NewReader([]byte(`{not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListExecutions_InvalidStatus(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	req := httptest.NewRequest(http.MethodGet, "/v0/executions?status=invalid", nil)
	w := httptest.NewRecorder()

	h.list(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEvents_DefaultPagination(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	ctx := context.Background()
	execID, err := k.CreateExecution(ctx, kernel.CreateExecutionRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/v0/executions/{id}/events", h.getEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/executions/"+execID+"/events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Events         []domain.Event `json:"events"`
		LatestSequence int64          `json:"latest_sequence"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Events) == 0 {
		t.Fatal("expected at least 1 event with default pagination")
	}
}

func TestCreateExecution_NilBody(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	req := httptest.NewRequest(http.MethodPost, "/v0/executions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = nil
	w := httptest.NewRecorder()

	h.create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for nil body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateExecution_AgentIDTooLong(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	longID := strings.Repeat("a", 257)
	body, _ := json.Marshal(map[string]any{
		"agent_id": longID,
		"input":    map[string]string{"task": "test"},
	})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized agent_id, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateExecution_Success(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	body, _ := json.Marshal(map[string]any{
		"agent_id": "agent-1",
		"input":    map[string]string{"task": "test"},
	})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListExecutions_ValidStatus(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	for _, status := range []string{"pending", "running", "blocked", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v0/executions?status="+status, nil)
			w := httptest.NewRecorder()
			h.list(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for status=%s, got %d: %s", status, w.Code, w.Body.String())
			}
		})
	}
}

func TestListExecutions_EmptyResult(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	req := httptest.NewRequest(http.MethodGet, "/v0/executions?status=completed", nil)
	w := httptest.NewRecorder()
	h.list(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Executions []any `json:"executions"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Executions == nil {
		t.Fatal("expected non-nil executions array (empty but present)")
	}
}

func TestGetEvents_CustomPagination(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	ctx := context.Background()
	execID, err := k.CreateExecution(ctx, kernel.CreateExecutionRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/v0/executions/{id}/events", h.getEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/executions/"+execID+"/events?after_sequence=0&limit=5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEvents_InvalidAfterSequence(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	ctx := context.Background()
	execID, err := k.CreateExecution(ctx, kernel.CreateExecutionRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/v0/executions/{id}/events", h.getEvents)

	// Non-numeric after_sequence should be treated as 0 (default).
	req := httptest.NewRequest(http.MethodGet, "/v0/executions/"+execID+"/events?after_sequence=abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendSignal_MissingSignalType(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	ctx := context.Background()
	execID, err := k.CreateExecution(ctx, kernel.CreateExecutionRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/v0/executions/{id}/signal", h.sendSignal)

	body, _ := json.Marshal(map[string]any{
		"signal_type": "",
		"payload":     nil,
	})
	req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID+"/signal", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendSignal_InvalidJSON(t *testing.T) {
	k := newTestKernel()
	h := &executionHandlers{kernel: k}

	r := chi.NewRouter()
	r.Post("/v0/executions/{id}/signal", h.sendSignal)

	req := httptest.NewRequest(http.MethodPost, "/v0/executions/exec-1/signal", bytes.NewReader([]byte(`{bad`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitIntent_MissingFields(t *testing.T) {
	k := newTestKernel()
	h := &agentHandlers{kernel: k}

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing execution_id",
			body: map[string]any{
				"execution_id": "",
				"session_id":   "sess-1",
				"intent":       map[string]any{"type": "tool_call"},
			},
		},
		{
			name: "missing session_id",
			body: map[string]any{
				"execution_id": "exec-1",
				"session_id":   "",
				"intent":       map[string]any{"type": "tool_call"},
			},
		},
		{
			name: "missing intent type",
			body: map[string]any{
				"execution_id": "exec-1",
				"session_id":   "sess-1",
				"intent":       map[string]any{"type": ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/v0/agents/intent", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.submitIntent(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSubmitIntent_InvalidJSON(t *testing.T) {
	k := newTestKernel()
	h := &agentHandlers{kernel: k}

	req := httptest.NewRequest(http.MethodPost, "/v0/agents/intent", bytes.NewReader([]byte(`{bad`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.submitIntent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitIntent_ToolIDTooLong(t *testing.T) {
	k := newTestKernel()
	h := &agentHandlers{kernel: k}

	body, _ := json.Marshal(map[string]any{
		"execution_id": "exec-1",
		"session_id":   "sess-1",
		"intent": map[string]any{
			"type":    "tool_call",
			"tool_id": strings.Repeat("x", 257),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v0/agents/intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.submitIntent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized tool_id, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStepResult_MissingFields(t *testing.T) {
	k := newTestKernel()
	h := &agentHandlers{kernel: k}

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing execution_id",
			body: map[string]any{
				"execution_id": "",
				"session_id":   "sess-1",
				"step_id":      "step-1",
				"success":      true,
			},
		},
		{
			name: "missing session_id",
			body: map[string]any{
				"execution_id": "exec-1",
				"session_id":   "",
				"step_id":      "step-1",
				"success":      true,
			},
		},
		{
			name: "missing step_id",
			body: map[string]any{
				"execution_id": "exec-1",
				"session_id":   "sess-1",
				"step_id":      "",
				"success":      true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/v0/agents/step-result", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.stepResult(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStepResult_InvalidJSON(t *testing.T) {
	k := newTestKernel()
	h := &agentHandlers{kernel: k}

	req := httptest.NewRequest(http.MethodPost, "/v0/agents/step-result", bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.stepResult(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitResult_MissingFields(t *testing.T) {
	k := newTestKernel()
	h := &runnerHandlers{kernel: k}

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing job_id",
			body: map[string]any{
				"job_id":       "",
				"execution_id": "exec-1",
				"step_id":      "step-1",
				"success":      true,
			},
		},
		{
			name: "missing execution_id",
			body: map[string]any{
				"job_id":       "job-1",
				"execution_id": "",
				"step_id":      "step-1",
				"success":      true,
			},
		},
		{
			name: "missing step_id",
			body: map[string]any{
				"job_id":       "job-1",
				"execution_id": "exec-1",
				"step_id":      "",
				"success":      true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.body)

			r := chi.NewRouter()
			r.Post("/v0/runners/{id}/results", h.submitResult)

			req := httptest.NewRequest(http.MethodPost, "/v0/runners/runner-1/results", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSubmitResult_InvalidJSON(t *testing.T) {
	k := newTestKernel()
	h := &runnerHandlers{kernel: k}

	r := chi.NewRouter()
	r.Post("/v0/runners/{id}/results", h.submitResult)

	req := httptest.NewRequest(http.MethodPost, "/v0/runners/runner-1/results", bytes.NewReader([]byte(`{bad`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStepStarted_InvalidJSON(t *testing.T) {
	k := newTestKernel()
	h := &runnerHandlers{kernel: k}

	r := chi.NewRouter()
	r.Post("/v0/runners/steps/{stepId}/started", h.stepStarted)

	req := httptest.NewRequest(http.MethodPost, "/v0/runners/steps/step-1/started", bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStepStarted_MissingFields(t *testing.T) {
	k := newTestKernel()
	h := &runnerHandlers{kernel: k}

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "empty execution_id",
			body: map[string]any{
				"execution_id": "",
				"runner_id":    "runner-1",
			},
		},
		{
			name: "empty runner_id",
			body: map[string]any{
				"execution_id": "exec-1",
				"runner_id":    "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.body)

			r := chi.NewRouter()
			r.Post("/v0/runners/steps/{stepId}/started", h.stepStarted)

			req := httptest.NewRequest(http.MethodPost, "/v0/runners/steps/step-1/started", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				t.Fatalf("expected non-200 for missing fields, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

package kernel

import (
	"context"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

type mockEventStore struct {
	mu         sync.Mutex
	events     map[string][]domain.Event
	executions map[string]*domain.ExecutionSummary
	appendErr  error
}

func newMockEventStore() *mockEventStore {
	return &mockEventStore{
		events:     make(map[string][]domain.Event),
		executions: make(map[string]*domain.ExecutionSummary),
	}
}

func (m *mockEventStore) Append(_ context.Context, event domain.Event) error {
	if m.appendErr != nil {
		return m.appendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := int64(len(m.events[event.ExecutionID]) + 1)
	event.Sequence = seq
	m.events[event.ExecutionID] = append(m.events[event.ExecutionID], event)
	return nil
}

func (m *mockEventStore) AppendBatch(_ context.Context, events []domain.Event) error {
	if m.appendErr != nil {
		return m.appendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(events) == 0 {
		return nil
	}
	execID := events[0].ExecutionID
	base := int64(len(m.events[execID]))
	for i := range events {
		events[i].Sequence = base + int64(i) + 1
		m.events[execID] = append(m.events[execID], events[i])
	}
	return nil
}

func (m *mockEventStore) GetByExecution(_ context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.Event
	for _, e := range m.events[executionID] {
		if e.Sequence > afterSequence {
			result = append(result, e)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockEventStore) GetLatestSequence(_ context.Context, executionID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.events[executionID])), nil
}

func (m *mockEventStore) ListActiveExecutionIDs(_ context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockEventStore) ListExecutions(_ context.Context, _ domain.ExecutionFilter, _ string, _ int) ([]domain.ExecutionSummary, string, error) {
	return nil, "", nil
}

func (m *mockEventStore) GetExecution(_ context.Context, executionID string) (*domain.ExecutionSummary, error) {
	if s, ok := m.executions[executionID]; ok {
		return s, nil
	}
	return nil, nil
}

func (m *mockEventStore) CreateExecution(_ context.Context, id, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executions[id] = &domain.ExecutionSummary{
		ID:      id,
		AgentID: agentID,
		Status:  domain.ExecutionPending,
	}
	return nil
}

func (m *mockEventStore) UpdateExecutionStatus(_ context.Context, executionID string, status domain.ExecutionStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.executions[executionID]; ok {
		s.Status = status
	}
	return nil
}

func (m *mockEventStore) DeleteExecution(_ context.Context, executionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.executions, executionID)
	delete(m.events, executionID)
	return nil
}

func (m *mockEventStore) ListTerminalExecutions(_ context.Context, _ int64, _ int) ([]string, error) {
	return nil, nil
}

type mockCheckpointStore struct {
	checkpoints map[string]*domain.Checkpoint
}

func newMockCheckpointStore() *mockCheckpointStore {
	return &mockCheckpointStore{checkpoints: make(map[string]*domain.Checkpoint)}
}

func (m *mockCheckpointStore) Get(_ context.Context, executionID string) (*domain.Checkpoint, bool, error) {
	cp, ok := m.checkpoints[executionID]
	return cp, ok, nil
}

func (m *mockCheckpointStore) Save(_ context.Context, checkpoint domain.Checkpoint) error {
	m.checkpoints[checkpoint.ExecutionID] = &checkpoint
	return nil
}

func (m *mockCheckpointStore) Delete(_ context.Context, executionID string) error {
	delete(m.checkpoints, executionID)
	return nil
}

type mockAgentHub struct {
	mu       sync.Mutex
	sent     []store.AgentMessage
	sessions map[string]store.AgentMessage
	hasConn  bool
	connInfo store.ConnInfo
}

func newMockAgentHub() *mockAgentHub {
	return &mockAgentHub{
		sessions: make(map[string]store.AgentMessage),
		hasConn:  false,
	}
}

func newConnectedMockAgentHub() *mockAgentHub {
	return &mockAgentHub{
		sessions: make(map[string]store.AgentMessage),
		hasConn:  true,
		connInfo: store.ConnInfo{ConsumerID: "test-consumer", SessionID: ""},
	}
}

func (m *mockAgentHub) Send(_ string, msg store.AgentMessage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return m.hasConn
}

func (m *mockAgentHub) SendTo(_ string, _ string, msg store.AgentMessage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return m.hasConn
}

func (m *mockAgentHub) SendToSession(sessionID string, msg store.AgentMessage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sessionID] = msg
	return true
}

func (m *mockAgentHub) PickConnection(_ string) (store.ConnInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasConn {
		return store.ConnInfo{}, false
	}
	return m.connInfo, true
}

func (m *mockAgentHub) HasConnections(_ string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasConn
}

func (m *mockAgentHub) SetSession(_, _, _ string) {}

type mockRunnerHub struct {
	mu         sync.Mutex
	dispatched []store.RunnerMessage
	idle       map[string]bool // runnerID -> idle
	hasCap     bool
	onDispatch func() // optional callback invoked after each Dispatch
}

func newMockRunnerHub() *mockRunnerHub {
	return &mockRunnerHub{
		idle:   make(map[string]bool),
		hasCap: true,
	}
}

func (m *mockRunnerHub) Dispatch(toolID string, msg store.RunnerMessage) (store.RunnerConnInfo, bool) {
	m.mu.Lock()
	m.dispatched = append(m.dispatched, msg)
	cb := m.onDispatch
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
	return store.RunnerConnInfo{RunnerID: "mock-runner", ConsumerID: "mock-consumer"}, true
}

func (m *mockRunnerHub) SendTo(_, _ string, _ store.RunnerMessage) bool {
	return true
}

func (m *mockRunnerHub) MarkBusy(runnerID, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle[runnerID] = false
}

func (m *mockRunnerHub) MarkIdle(runnerID, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle[runnerID] = true
}

func (m *mockRunnerHub) MarkRunnerIdle(runnerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle[runnerID] = true
}

func (m *mockRunnerHub) HasCapability(_ string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasCap
}

func (m *mockRunnerHub) UpdateCapabilities(_ string, _ []string) {}

type mockSignalStore struct {
	mu      sync.Mutex
	signals map[string][]domain.Signal
}

func newMockSignalStore() *mockSignalStore {
	return &mockSignalStore{signals: make(map[string][]domain.Signal)}
}

func (m *mockSignalStore) Publish(_ context.Context, executionID string, signal domain.Signal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signals[executionID] = append(m.signals[executionID], signal)
	return nil
}

func (m *mockSignalStore) GetPending(_ context.Context, executionID string) ([]domain.Signal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.signals[executionID], nil
}

func (m *mockSignalStore) Clear(_ context.Context, executionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.signals, executionID)
	return nil
}

type mockSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*domain.Session
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{sessions: make(map[string]*domain.Session)}
}

func (m *mockSessionStore) Create(_ context.Context, session domain.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.ID] = &session
	return nil
}

func (m *mockSessionStore) Get(_ context.Context, sessionID string) (*domain.Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	return s, ok, nil
}

func (m *mockSessionStore) GetByExecution(_ context.Context, executionID string) (*domain.Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.ExecutionID == executionID {
			return s, true, nil
		}
	}
	return nil, false, nil
}

func (m *mockSessionStore) Extend(_ context.Context, sessionID string, duration time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		s.ExpiresAt = time.Now().Add(duration)
	}
	return nil
}

func (m *mockSessionStore) Delete(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
	return nil
}

func (m *mockSessionStore) DeleteExpired(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

type mockRunnerStore struct {
	mu      sync.Mutex
	runners map[string]*domain.Runner
}

func newMockRunnerStore() *mockRunnerStore {
	return &mockRunnerStore{runners: make(map[string]*domain.Runner)}
}

func (m *mockRunnerStore) Register(_ context.Context, runner domain.Runner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runners[runner.ID] = &runner
	return nil
}

func (m *mockRunnerStore) Get(_ context.Context, runnerID string) (*domain.Runner, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[runnerID]
	return r, ok, nil
}

func (m *mockRunnerStore) List(_ context.Context) ([]domain.Runner, error) {
	return nil, nil
}

func (m *mockRunnerStore) UpdateHeartbeat(_ context.Context, runnerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runners[runnerID]; ok {
		r.LastHeartbeat = time.Now()
	}
	return nil
}

func (m *mockRunnerStore) Delete(_ context.Context, runnerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.runners, runnerID)
	return nil
}

type mockLocker struct{}

func (m *mockLocker) Acquire(_ context.Context, _ string) (func(), error) {
	return func() {}, nil
}

type mockPolicyEngine struct {
	decision domain.PolicyDecision
	reason   string
	ruleID   string
}

func newAllowAllPolicy() *mockPolicyEngine {
	return &mockPolicyEngine{
		decision: domain.PolicyAllow,
		reason:   "allowed by test",
		ruleID:   "test-allow",
	}
}

func newDenyAllPolicy() *mockPolicyEngine {
	return &mockPolicyEngine{
		decision: domain.PolicyDeny,
		reason:   "denied by test",
		ruleID:   "test-deny",
	}
}

func (m *mockPolicyEngine) Evaluate(_ context.Context, _ domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{
		Decision: m.decision,
		Reason:   m.reason,
		RuleID:   m.ruleID,
	}, nil
}

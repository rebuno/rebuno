package lifecycle

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

type mockEventStore struct {
	mu            sync.Mutex
	events        map[string][]domain.Event
	executions    map[string]*domain.ExecutionSummary
	activeIDs     []string
	terminalIDs   []string
	appendErr     error
	deletedIDs    []string
	statusUpdates map[string]domain.ExecutionStatus
}

func newMockEventStore() *mockEventStore {
	return &mockEventStore{
		events:        make(map[string][]domain.Event),
		executions:    make(map[string]*domain.ExecutionSummary),
		statusUpdates: make(map[string]domain.ExecutionStatus),
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeIDs, nil
}

func (m *mockEventStore) ListExecutions(_ context.Context, _ domain.ExecutionFilter, _ string, _ int) ([]domain.ExecutionSummary, string, error) {
	return nil, "", nil
}

func (m *mockEventStore) GetExecution(_ context.Context, executionID string) (*domain.ExecutionSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.executions[executionID]
	if !ok {
		return nil, nil
	}
	return s, nil
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
	m.statusUpdates[executionID] = status
	if s, ok := m.executions[executionID]; ok {
		s.Status = status
	}
	return nil
}

func (m *mockEventStore) DeleteExecution(_ context.Context, executionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedIDs = append(m.deletedIDs, executionID)
	delete(m.executions, executionID)
	delete(m.events, executionID)
	return nil
}

func (m *mockEventStore) ListTerminalExecutions(_ context.Context, _ int64, _ int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terminalIDs, nil
}

type mockCheckpointStore struct {
	mu         sync.Mutex
	deletedIDs []string
}

func newMockCheckpointStore() *mockCheckpointStore {
	return &mockCheckpointStore{}
}

func (m *mockCheckpointStore) Get(_ context.Context, _ string) (*domain.Checkpoint, bool, error) {
	return nil, false, nil
}

func (m *mockCheckpointStore) Save(_ context.Context, _ domain.Checkpoint) error {
	return nil
}

func (m *mockCheckpointStore) Delete(_ context.Context, executionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedIDs = append(m.deletedIDs, executionID)
	return nil
}

type mockSignalStore struct {
	mu         sync.Mutex
	signals    map[string][]domain.Signal
	clearedIDs []string
}

func newMockSignalStore() *mockSignalStore {
	return &mockSignalStore{
		signals: make(map[string][]domain.Signal),
	}
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
	m.clearedIDs = append(m.clearedIDs, executionID)
	delete(m.signals, executionID)
	return nil
}

type mockSessionStore struct {
	mu             sync.Mutex
	sessions       map[string]*domain.Session
	deletedExpired int
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
	m.mu.Lock()
	defer m.mu.Unlock()
	count := m.deletedExpired
	m.deletedExpired = 0
	return count, nil
}

type mockAgentHub struct {
	mu      sync.Mutex
	sent    []mockHubMessage
	hasConn bool
}

type mockHubMessage struct {
	AgentID string
	Type    string
}

func newMockAgentHub() *mockAgentHub {
	return &mockAgentHub{}
}

func (m *mockAgentHub) Send(agentID string, msg store.AgentMessage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, mockHubMessage{AgentID: agentID, Type: msg.Type})
	return m.hasConn
}

func (m *mockAgentHub) SendTo(_ string, _ string, _ store.AgentMessage) bool {
	return m.hasConn
}

func (m *mockAgentHub) SendToSession(_ string, _ store.AgentMessage) bool {
	return true
}

func (m *mockAgentHub) PickConnection(_ string) (store.ConnInfo, bool) {
	return store.ConnInfo{}, false
}

func (m *mockAgentHub) HasConnections(_ string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasConn
}

type mockLocker struct{}

func (m *mockLocker) Acquire(_ context.Context, _ string) (func(), error) {
	return func() {}, nil
}

type emittedEvent struct {
	ExecutionID string
	StepID      string
	EventType   domain.EventType
}

type mockEmitter struct {
	mu     sync.Mutex
	events []emittedEvent
}

func newMockEmitter() *mockEmitter {
	return &mockEmitter{}
}

func (m *mockEmitter) EmitEvent(
	_ context.Context,
	executionID string,
	stepID string,
	eventType domain.EventType,
	_ any,
	_ uuid.UUID,
	_ uuid.UUID,
) (domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, emittedEvent{
		ExecutionID: executionID,
		StepID:      stepID,
		EventType:   eventType,
	})
	return domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: executionID,
		StepID:      stepID,
		Type:        eventType,
		Payload:     json.RawMessage(`{}`),
		Timestamp:   time.Now(),
	}, nil
}

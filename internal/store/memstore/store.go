package memstore

import (
	"sync"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

func NewStore() *Store {
	return &Store{
		agents:     make(map[string]domain.Agent),
		executions: make(map[uuid.UUID]domain.Execution),
		events:     make(map[uuid.UUID][]domain.Event),
		steps:      make(map[string]domain.Step),
		approvals:  make(map[uuid.UUID]domain.Approval),
		dispatches: make(map[uuid.UUID]domain.Dispatch),
		lockers:    make(map[string]chan struct{}),
	}
}

type Store struct {
	mu         sync.RWMutex
	agents     map[string]domain.Agent
	executions map[uuid.UUID]domain.Execution
	events     map[uuid.UUID][]domain.Event
	steps      map[string]domain.Step
	approvals  map[uuid.UUID]domain.Approval
	dispatches map[uuid.UUID]domain.Dispatch
	lockers    map[string]chan struct{}
	lockMtx    sync.Mutex
}

var _ store.EventStore = (*Store)(nil)
var _ store.StepStore = (*Store)(nil)
var _ store.ExecutionStore = (*Store)(nil)
var _ store.AgentStore = (*Store)(nil)
var _ store.ApprovalStore = (*Store)(nil)
var _ store.JobQueue = (*Store)(nil)
var _ store.Locker = (*Store)(nil)
var _ store.UnitOfWork = (*Store)(nil)

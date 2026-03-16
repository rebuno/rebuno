package hub

import (
	"log/slog"
	"sync"

	"github.com/rebuno/rebuno/internal/store"
)

type runnerRef struct {
	RunnerID   string
	ConsumerID string
}

type RunnerHub struct {
	mu       sync.RWMutex
	runners  map[string]map[string]*RunnerConn // runnerID -> consumerID -> conn
	capIndex map[string][]runnerRef            // toolID -> []runnerRef
	rrIndex  map[string]int                    // toolID -> round-robin index
	logger   *slog.Logger
}

func NewRunnerHub(logger *slog.Logger) *RunnerHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &RunnerHub{
		runners:  make(map[string]map[string]*RunnerConn),
		capIndex: make(map[string][]runnerRef),
		rrIndex:  make(map[string]int),
		logger:   logger,
	}
}

func (h *RunnerHub) Register(runnerID, consumerID string, capabilities []string) *RunnerConn {
	h.mu.Lock()
	defer h.mu.Unlock()

	conn := NewRunnerConn(runnerID, consumerID, capabilities)

	if h.runners[runnerID] == nil {
		h.runners[runnerID] = make(map[string]*RunnerConn)
	}

	if old, ok := h.runners[runnerID][consumerID]; ok {
		close(old.EventCh)
		h.removeFromCapIndex(runnerID, consumerID)
	}

	h.runners[runnerID][consumerID] = conn

	ref := runnerRef{RunnerID: runnerID, ConsumerID: consumerID}
	for _, cap := range capabilities {
		h.capIndex[cap] = append(h.capIndex[cap], ref)
	}

	h.logger.Debug("runner connection registered",
		slog.String("runner_id", runnerID),
		slog.String("consumer_id", consumerID),
		slog.Int("capabilities", len(capabilities)),
	)

	return conn
}

func (h *RunnerHub) Unregister(runnerID, consumerID string, generation uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	consumers, ok := h.runners[runnerID]
	if !ok {
		return
	}

	conn, ok := consumers[consumerID]
	if !ok {
		return
	}

	if conn.generation != generation {
		return
	}

	close(conn.EventCh)
	delete(consumers, consumerID)
	h.removeFromCapIndex(runnerID, consumerID)

	if len(consumers) == 0 {
		delete(h.runners, runnerID)
	}

	h.logger.Debug("runner connection unregistered",
		slog.String("runner_id", runnerID),
		slog.String("consumer_id", consumerID),
	)
}

func (h *RunnerHub) Dispatch(toolID string, msg store.RunnerMessage) (store.RunnerConnInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	refs := h.capIndex[toolID]
	if len(refs) == 0 {
		return store.RunnerConnInfo{}, false
	}

	startIdx := h.rrIndex[toolID] % len(refs)
	for i := 0; i < len(refs); i++ {
		idx := (startIdx + i) % len(refs)
		ref := refs[idx]

		consumers, ok := h.runners[ref.RunnerID]
		if !ok {
			continue
		}
		conn, ok := consumers[ref.ConsumerID]
		if !ok {
			continue
		}
		if conn.Busy {
			continue
		}

		if !conn.Send(msg) {
			h.logger.Warn("runner event channel full",
				slog.String("runner_id", ref.RunnerID),
				slog.String("consumer_id", ref.ConsumerID),
			)
			continue
		}

		h.rrIndex[toolID] = idx + 1
		return store.RunnerConnInfo{
			RunnerID:   ref.RunnerID,
			ConsumerID: ref.ConsumerID,
		}, true
	}

	return store.RunnerConnInfo{}, false
}

func (h *RunnerHub) SendTo(runnerID, consumerID string, msg store.RunnerMessage) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	consumers, ok := h.runners[runnerID]
	if !ok {
		return false
	}
	conn, ok := consumers[consumerID]
	if !ok {
		return false
	}
	return conn.Send(msg)
}

func (h *RunnerHub) MarkBusy(runnerID, consumerID string) {
	h.setConnBusy(runnerID, consumerID, true)
}

func (h *RunnerHub) MarkIdle(runnerID, consumerID string) {
	h.setConnBusy(runnerID, consumerID, false)
}

func (h *RunnerHub) setConnBusy(runnerID, consumerID string, busy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if consumers, ok := h.runners[runnerID]; ok {
		if conn, ok := consumers[consumerID]; ok {
			conn.Busy = busy
		}
	}
}

func (h *RunnerHub) MarkRunnerIdle(runnerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if consumers, ok := h.runners[runnerID]; ok {
		for _, conn := range consumers {
			conn.Busy = false
		}
	}
}

func (h *RunnerHub) UpdateCapabilities(runnerID string, capabilities []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	consumers, ok := h.runners[runnerID]
	if !ok {
		return
	}

	for consumerID := range consumers {
		h.removeFromCapIndex(runnerID, consumerID)
	}

	for consumerID, conn := range consumers {
		conn.Capabilities = capabilities
		ref := runnerRef{RunnerID: runnerID, ConsumerID: consumerID}
		for _, cap := range capabilities {
			h.capIndex[cap] = append(h.capIndex[cap], ref)
		}
	}

	h.logger.Debug("runner capabilities updated",
		slog.String("runner_id", runnerID),
		slog.Int("capabilities", len(capabilities)),
	)
}

func (h *RunnerHub) HasCapability(toolID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.capIndex[toolID]) > 0
}

func (h *RunnerHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, consumers := range h.runners {
		for _, conn := range consumers {
			close(conn.EventCh)
		}
	}
	h.runners = make(map[string]map[string]*RunnerConn)
	h.capIndex = make(map[string][]runnerRef)
	h.rrIndex = make(map[string]int)
}

func (h *RunnerHub) removeFromCapIndex(runnerID, consumerID string) {
	for cap, refs := range h.capIndex {
		var filtered []runnerRef
		for _, ref := range refs {
			if ref.RunnerID != runnerID || ref.ConsumerID != consumerID {
				filtered = append(filtered, ref)
			}
		}
		if len(filtered) == 0 {
			delete(h.capIndex, cap)
			delete(h.rrIndex, cap)
		} else {
			h.capIndex[cap] = filtered
		}
	}
}

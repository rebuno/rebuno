package hub

import (
	"log/slog"
	"sort"
	"sync"

	"github.com/rebuno/rebuno/internal/store"
)

type Hub struct {
	mu       sync.RWMutex
	agents   map[string]map[string]*Conn // agentID -> consumerID -> Conn
	sessions map[string]*Conn            // sessionID -> Conn
	rrIndex  map[string]int              // agentID -> round-robin index
	logger   *slog.Logger
}

func New(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		agents:   make(map[string]map[string]*Conn),
		sessions: make(map[string]*Conn),
		rrIndex:  make(map[string]int),
		logger:   logger,
	}
}

func (h *Hub) Register(agentID, consumerID string) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()

	conn := NewConn(agentID, consumerID)

	if h.agents[agentID] == nil {
		h.agents[agentID] = make(map[string]*Conn)
	}

	if old, ok := h.agents[agentID][consumerID]; ok {
		close(old.EventCh)
		if old.SessionID != "" {
			delete(h.sessions, old.SessionID)
		}
	}

	h.agents[agentID][consumerID] = conn

	h.logger.Debug("connection registered",
		slog.String("agent_id", agentID),
		slog.String("consumer_id", consumerID),
	)

	return conn
}

func (h *Hub) Unregister(agentID, consumerID string, generation uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	consumers, ok := h.agents[agentID]
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

	if conn.SessionID != "" {
		delete(h.sessions, conn.SessionID)
	}
	close(conn.EventCh)
	delete(consumers, consumerID)

	if len(consumers) == 0 {
		delete(h.agents, agentID)
		delete(h.rrIndex, agentID)
	}

	h.logger.Debug("connection unregistered",
		slog.String("agent_id", agentID),
		slog.String("consumer_id", consumerID),
	)
}

func (h *Hub) GetSessionID(agentID, consumerID string, generation uint64) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	consumers, ok := h.agents[agentID]
	if !ok {
		return ""
	}
	conn, ok := consumers[consumerID]
	if !ok {
		return ""
	}
	if conn.generation != generation {
		return ""
	}
	return conn.SessionID
}

func (h *Hub) SetSession(agentID, consumerID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	consumers, ok := h.agents[agentID]
	if !ok {
		return
	}
	conn, ok := consumers[consumerID]
	if !ok {
		return
	}

	if conn.SessionID != "" {
		delete(h.sessions, conn.SessionID)
	}

	conn.SessionID = sessionID
	h.sessions[sessionID] = conn
}

func (h *Hub) Send(agentID string, msg store.AgentMessage) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	conn := h.roundRobin(agentID)
	if conn == nil {
		return false
	}

	if !conn.Send(msg) {
		h.logger.Warn("event channel full, closing connection",
			slog.String("agent_id", agentID),
			slog.String("consumer_id", conn.ConsumerID),
		)
		if conn.SessionID != "" {
			delete(h.sessions, conn.SessionID)
		}
		close(conn.EventCh)
		delete(h.agents[agentID], conn.ConsumerID)
		if len(h.agents[agentID]) == 0 {
			delete(h.agents, agentID)
			delete(h.rrIndex, agentID)
		}
		return false
	}
	return true
}

func (h *Hub) SendTo(consumerID, agentID string, msg store.AgentMessage) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	consumers, ok := h.agents[agentID]
	if !ok {
		return false
	}
	conn, ok := consumers[consumerID]
	if !ok {
		return false
	}
	return conn.Send(msg)
}

func (h *Hub) SendToSession(sessionID string, msg store.AgentMessage) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	conn, ok := h.sessions[sessionID]
	if !ok {
		return false
	}
	return conn.Send(msg)
}

func (h *Hub) PickConnection(agentID string) (store.ConnInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	conn := h.roundRobin(agentID)
	if conn == nil {
		return store.ConnInfo{}, false
	}

	return store.ConnInfo{
		ConsumerID: conn.ConsumerID,
		SessionID:  conn.SessionID,
	}, true
}

func (h *Hub) roundRobin(agentID string) *Conn {
	consumers := h.agents[agentID]
	if len(consumers) == 0 {
		return nil
	}

	ids := make([]string, 0, len(consumers))
	for id := range consumers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	idx := h.rrIndex[agentID] % len(ids)
	h.rrIndex[agentID] = idx + 1
	return consumers[ids[idx]]
}

func (h *Hub) HasConnections(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.agents[agentID]) > 0
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, consumers := range h.agents {
		for _, conn := range consumers {
			close(conn.EventCh)
		}
	}
	h.agents = make(map[string]map[string]*Conn)
	h.sessions = make(map[string]*Conn)
	h.rrIndex = make(map[string]int)
}

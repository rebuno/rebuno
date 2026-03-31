package hub

import (
	"sync/atomic"

	"github.com/rebuno/rebuno/internal/store"
)

const eventChannelSize = 64

var epoch atomic.Uint64

type Conn struct {
	AgentID    string
	ConsumerID string
	EventCh    chan store.AgentMessage
	generation uint64
	sessionIDs map[string]struct{}
}

func NewConn(agentID, consumerID string) *Conn {
	return &Conn{
		AgentID:    agentID,
		ConsumerID: consumerID,
		EventCh:    make(chan store.AgentMessage, eventChannelSize),
		generation: epoch.Add(1),
		sessionIDs: make(map[string]struct{}),
	}
}

func (c *Conn) Generation() uint64 {
	return c.generation
}

func (c *Conn) Send(msg store.AgentMessage) bool {
	select {
	case c.EventCh <- msg:
		return true
	default:
		return false
	}
}

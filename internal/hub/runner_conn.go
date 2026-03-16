package hub

import "github.com/rebuno/rebuno/internal/store"

const runnerEventChannelSize = 64

type RunnerConn struct {
	RunnerID     string
	ConsumerID   string
	Capabilities []string
	Busy         bool
	EventCh      chan store.RunnerMessage
	generation   uint64
}

func NewRunnerConn(runnerID, consumerID string, capabilities []string) *RunnerConn {
	return &RunnerConn{
		RunnerID:     runnerID,
		ConsumerID:   consumerID,
		Capabilities: capabilities,
		EventCh:      make(chan store.RunnerMessage, runnerEventChannelSize),
		generation:   epoch.Add(1),
	}
}

func (c *RunnerConn) Generation() uint64 {
	return c.generation
}

func (c *RunnerConn) Send(msg store.RunnerMessage) bool {
	select {
	case c.EventCh <- msg:
		return true
	default:
		return false
	}
}

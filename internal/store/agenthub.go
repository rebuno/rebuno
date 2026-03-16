package store

import "encoding/json"

type AgentMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type ConnInfo struct {
	ConsumerID string
	SessionID  string
}

type AgentHub interface {
	Send(agentID string, msg AgentMessage) bool
	SendTo(consumerID string, agentID string, msg AgentMessage) bool
	SendToSession(sessionID string, msg AgentMessage) bool
	PickConnection(agentID string) (ConnInfo, bool)
	HasConnections(agentID string) bool
}

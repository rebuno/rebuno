package store

import "encoding/json"

type RunnerMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type RunnerConnInfo struct {
	RunnerID   string
	ConsumerID string
}

type RunnerHub interface {
	Dispatch(toolID string, msg RunnerMessage) (RunnerConnInfo, bool)
	SendTo(runnerID, consumerID string, msg RunnerMessage) bool
	MarkBusy(runnerID, consumerID string)
	MarkIdle(runnerID, consumerID string)
	HasCapability(toolID string) bool
	UpdateCapabilities(runnerID string, capabilities []string)
}

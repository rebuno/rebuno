package domain

import (
	"encoding/json"
	"time"
)

const RunnerStatusOnline = "online"

type Runner struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Capabilities  []string        `json:"capabilities"`
	Status        string          `json:"status"` // "online", "offline"
	LastHeartbeat time.Time       `json:"last_heartbeat"`
	RegisteredAt  time.Time       `json:"registered_at"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

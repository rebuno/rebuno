package domain

import (
	"encoding/json"
	"time"
)

type Signal struct {
	ID          string          `json:"id"`
	ExecutionID string          `json:"execution_id"`
	SignalType  string          `json:"signal_type"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
}

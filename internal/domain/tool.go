package domain

import (
	"encoding/json"
	"time"
)

type ToolSchema struct {
	ID           string          `json:"id"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	RunnerID     string          `json:"runner_id"`
	RegisteredAt time.Time       `json:"registered_at"`
}

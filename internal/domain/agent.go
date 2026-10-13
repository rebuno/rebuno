package domain

import (
	"encoding/json"
	"time"
)

type Agent struct {
	ID                  string    `json:"id"`
	WebhookURL          string    `json:"webhook_url"`
	Secret              string    `json:"-"`
	PolicyBundle        string    `json:"policy_bundle,omitempty"`
	RegisteredAt        time.Time `json:"registered_at"`
	LeaseTimeoutSeconds float64   `json:"lease_timeout_seconds,omitempty"`
	AgentMetadata
}

type AgentMetadata struct {
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

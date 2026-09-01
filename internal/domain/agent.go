package domain

import "time"

type Agent struct {
	ID           string    `json:"id"`
	WebhookURL   string    `json:"webhook_url"`
	Secret       string    `json:"-"`
	PolicyBundle string    `json:"policy_bundle,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

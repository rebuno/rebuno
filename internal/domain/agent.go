package domain

import "time"

type Agent struct {
	ID           string
	WebhookURL   string
	Secret       string
	PolicyBundle string
	RegisteredAt time.Time
}

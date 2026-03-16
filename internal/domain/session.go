package domain

import "time"

type Session struct {
	ID          string    `json:"id"`
	ExecutionID string    `json:"execution_id"`
	AgentID     string    `json:"agent_id"`
	ConsumerID  string    `json:"consumer_id"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

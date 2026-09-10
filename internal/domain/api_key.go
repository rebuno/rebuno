package domain

import "time"

type Scope string

const (
	ScopeExecutionsRead  Scope = "executions:read"
	ScopeExecutionsWrite Scope = "executions:write"
	ScopeApprovalsRead   Scope = "approvals:read"
	ScopeApprovalsWrite  Scope = "approvals:write"
	ScopeAgentsRead      Scope = "agents:read"
	ScopeAgentsWrite     Scope = "agents:write"
	ScopePoliciesWrite   Scope = "policies:write"
	ScopeAPIKeysManage   Scope = "api_keys:manage"
)

func (s Scope) Valid() bool {
	switch s {
	case ScopeExecutionsRead, ScopeExecutionsWrite, ScopeApprovalsRead, ScopeApprovalsWrite, ScopeAgentsRead, ScopeAgentsWrite, ScopePoliciesWrite, ScopeAPIKeysManage:
		return true
	default:
		return false
	}
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []Scope    `json:"scopes"`
	SecretHash []byte     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

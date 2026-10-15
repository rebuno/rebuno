package domain

import (
	"slices"
	"time"
)

type Scope string

const (
	ScopeExecutionsRead        Scope = "executions:read"
	ScopeExecutionsWrite       Scope = "executions:write"
	ScopeExecutionsImpersonate Scope = "executions:impersonate"
	ScopeApprovalsRead         Scope = "approvals:read"
	ScopeApprovalsWrite        Scope = "approvals:write"
	ScopeApprovalsDelegate     Scope = "approvals:delegate"
	ScopeAgentsRead            Scope = "agents:read"
	ScopeAgentsWrite           Scope = "agents:write"
	ScopePoliciesWrite         Scope = "policies:write"
	ScopeAPIKeysManage         Scope = "api_keys:manage"
)

var AllScopes = []Scope{
	ScopeExecutionsRead, ScopeExecutionsWrite, ScopeExecutionsImpersonate,
	ScopeApprovalsRead, ScopeApprovalsWrite, ScopeApprovalsDelegate,
	ScopeAgentsRead, ScopeAgentsWrite,
	ScopePoliciesWrite, ScopeAPIKeysManage,
}

func (s Scope) Valid() bool {
	return slices.Contains(AllScopes, s)
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []Scope    `json:"scopes"`
	SecretHash []byte     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

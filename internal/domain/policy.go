package domain

import (
	"encoding/json"
	"time"
)

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func (e APIError) Error() string { return e.Code + ": " + e.Message }

const (
	DecisionAllow           = "allow"
	DecisionDeny            = "deny"
	DecisionRequireApproval = "require_approval"
)

type PolicyResult struct {
	Decision       string               `json:"decision" yaml:"decision"`
	Reason         string               `json:"reason,omitempty" yaml:"reason,omitempty"`
	RuleID         string               `json:"rule_id,omitempty" yaml:"-"`
	ApprovalConfig PolicyApprovalConfig `json:"approval_config,omitempty" yaml:"approval_config,omitempty"`
	RateLimit      RateLimitConfig      `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
}

type RateLimitConfig struct {
	MaxCalls int           `json:"max_calls,omitempty" yaml:"max_calls,omitempty"`
	Window   time.Duration `json:"window,omitempty" yaml:"window,omitempty"`
	PerWhat  string        `json:"per_what,omitempty" yaml:"per_what,omitempty"` // "execution" (default), "agent", "global"
	// OnLimiterError selects behavior when the limiter backend errors:
	// LimiterErrorAllow (default, fail-open) or LimiterErrorDeny (fail-closed).
	// Hard ceilings should generally be expressed as policy deny/require_approval.
	OnLimiterError string `json:"on_limiter_error,omitempty" yaml:"on_limiter_error,omitempty"`
}

const (
	LimiterErrorAllow = "allow"
	LimiterErrorDeny  = "deny"
)

type PolicyApprovalConfig struct {
	Approvers []string      `json:"approvers,omitempty" yaml:"approvers,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Message   string        `json:"message,omitempty" yaml:"message,omitempty"`
}

type PolicyInput struct {
	AgentID  string
	Target   string
	Args     json.RawMessage
	StepKind StepKind
}

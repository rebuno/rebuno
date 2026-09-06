package domain

import (
	"encoding/json"
	"time"
)

const (
	DecisionAllow           = "allow"
	DecisionDeny            = "deny"
	DecisionRequireApproval = "require_approval"
)

// RuleIndeterminateRetry marks a denial no rule made: an at_most_once retry
// refused after an indeterminate outcome.
const RuleIndeterminateRetry = "__indeterminate_retry"

type PolicyResult struct {
	Decision       string               `json:"decision" yaml:"decision"`
	Reason         string               `json:"reason,omitempty" yaml:"reason,omitempty"`
	RuleID         string               `json:"rule_id,omitempty" yaml:"-"`
	ApprovalConfig PolicyApprovalConfig `json:"approval_config,omitempty" yaml:"approval_config,omitempty"`
	RateLimit      RateLimitConfig      `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
	Budget         BudgetConfig         `json:"budget,omitempty" yaml:"budget,omitempty"`
}

type BudgetConfig struct {
	MaxTokens int    `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
	OnExceed  string `json:"on_exceed,omitempty" yaml:"on_exceed,omitempty"`
}

type RateLimitConfig struct {
	MaxCalls       int           `json:"max_calls,omitempty" yaml:"max_calls,omitempty"`
	Window         time.Duration `json:"window,omitempty" yaml:"window,omitempty"`
	PerWhat        string        `json:"per_what,omitempty" yaml:"per_what,omitempty"`
	MaxWait        time.Duration `json:"max_wait,omitempty" yaml:"max_wait,omitempty"`
	OnLimiterError string        `json:"on_limiter_error,omitempty" yaml:"on_limiter_error,omitempty"`
}

const (
	LimiterErrorAllow = "allow"
	LimiterErrorDeny  = "deny"
)

const (
	PerWhatExecution = "execution"
	PerWhatAgent     = "agent"
	PerWhatGlobal    = "global"
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

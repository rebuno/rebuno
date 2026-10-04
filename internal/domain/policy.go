package domain

import (
	"encoding/json"
	"time"
)

const (
	DecisionAllow           = "allow"
	DecisionDeny            = "deny"
	DecisionRequireApproval = "require_approval"
	DecisionJudge           = "judge"
)

// Policy tests skip decisions from the kernel's retry guard.
const RuleIndeterminateRetry = "__indeterminate_retry"

type PolicyResult struct {
	Decision       string               `json:"decision" yaml:"decision"`
	Reason         string               `json:"reason,omitempty" yaml:"reason,omitempty"`
	RuleID         string               `json:"rule_id,omitempty" yaml:"-"`
	PolicyHash     string               `json:"policy_hash,omitempty" yaml:"-"`
	ApprovalConfig PolicyApprovalConfig `json:"approval_config,omitempty" yaml:"approval_config,omitempty"`
	RateLimit      RateLimitConfig      `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
	Budget         BudgetConfig         `json:"budget,omitempty" yaml:"budget,omitempty"`
	Judge          JudgeConfig          `json:"judge,omitempty" yaml:"judge,omitempty"`
}

type JudgeConfig struct {
	Instructions string  `json:"instructions,omitempty" yaml:"instructions,omitempty"`
	Threshold    float64 `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	Fallback     string  `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

type BudgetConfig struct {
	MaxTokens int    `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
	Scope     string `json:"scope,omitempty" yaml:"scope,omitempty"`
	OnExceed  string `json:"on_exceed,omitempty" yaml:"on_exceed,omitempty"`
}

const (
	BudgetScopeExecution = "execution"
	BudgetScopeSession   = "session"
)

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
	PerWhatSession   = "session"
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

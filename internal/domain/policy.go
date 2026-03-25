package domain

import "encoding/json"

type PolicyInput struct {
	Action      string            `json:"action"` // "tool.invoke", "execution.complete", "execution.fail", "execution.wait"
	ToolID      string            `json:"tool_id,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	ExecutionID string            `json:"execution_id"`
	AgentID     string            `json:"agent_id,omitempty"`
	Arguments   json.RawMessage   `json:"arguments,omitempty"`
	StepCount   int               `json:"step_count"`
	DurationMs  int64             `json:"duration_ms"`
}

type PolicyDecision string

const (
	PolicyAllow           PolicyDecision = "allow"
	PolicyDeny            PolicyDecision = "deny"
	PolicyRequireApproval PolicyDecision = "require_approval"
)

type PolicyResult struct {
	Decision  PolicyDecision `json:"decision"`
	Reason    string         `json:"reason"`
	RuleID    string         `json:"rule_id"`
	TimeoutMs int64          `json:"timeout_ms,omitempty"`
}

type ArgumentPredicate struct {
	Field     string   `json:"field" yaml:"field"`
	Pattern   string   `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	OneOf     []string `json:"one_of,omitempty" yaml:"one_of,omitempty"`
	Min       *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max       *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	MaxLength *int     `json:"max_length,omitempty" yaml:"max_length,omitempty"`
	Required  bool     `json:"required,omitempty" yaml:"required,omitempty"`
}

type PolicyCondition struct {
	Action    string              `json:"action,omitempty" yaml:"action,omitempty"`
	ToolID    string              `json:"tool_id,omitempty" yaml:"tool_id,omitempty"`
	ToolIDs   []string            `json:"tool_ids,omitempty" yaml:"tool_ids,omitempty"`
	AgentID   string              `json:"agent_id,omitempty" yaml:"agent_id,omitempty"`
	AgentIDs  []string            `json:"agent_ids,omitempty" yaml:"agent_ids,omitempty"`
	Labels    map[string]string   `json:"labels,omitempty" yaml:"labels,omitempty"`
	Arguments     []ArgumentPredicate `json:"arguments,omitempty" yaml:"arguments,omitempty"`
	MinStepCount  *int                `json:"min_step_count,omitempty" yaml:"min_step_count,omitempty"`
	MaxDurationMs *int64              `json:"max_duration_ms,omitempty" yaml:"max_duration_ms,omitempty"`
}

type PolicyAction struct {
	Decision  PolicyDecision `json:"decision" yaml:"decision"`
	Reason    string         `json:"reason,omitempty" yaml:"reason,omitempty"`
	TimeoutMs int64          `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
}

type PolicyRule struct {
	ID       string          `json:"id" yaml:"id"`
	Priority int             `json:"priority" yaml:"priority"`
	When     PolicyCondition `json:"when" yaml:"when"`
	Then     PolicyAction    `json:"then" yaml:"then"`
}

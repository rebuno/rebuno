package policy

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"

	"github.com/rebuno/rebuno/internal/domain"
)

type Engine interface {
	Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error)
}

type RuleEngine struct {
	rules         []domain.PolicyRule
	defaultAction domain.PolicyAction
}

func NewRuleEngine(cfg PolicyConfig) (*RuleEngine, error) {
	seen := make(map[int]string)
	for _, r := range cfg.Rules {
		if existing, ok := seen[r.Priority]; ok {
			return nil, fmt.Errorf("%w: priority %d used by both %q and %q",
				domain.ErrDuplicatePriority, r.Priority, existing, r.ID)
		}
		seen[r.Priority] = r.ID
	}

	rules := make([]domain.PolicyRule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	slices.SortFunc(rules, func(a, b domain.PolicyRule) int {
		return cmp.Compare(a.Priority, b.Priority)
	})

	def := cfg.Default
	if def.Decision == "" {
		def = domain.PolicyAction{
			Decision: domain.PolicyDeny,
			Reason:   "No explicit allow rule matched",
		}
	}

	return &RuleEngine{rules: rules, defaultAction: def}, nil
}

func (e *RuleEngine) Evaluate(_ context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	for _, rule := range e.rules {
		if matchesRule(rule.When, input) {
			return domain.PolicyResult{
				Decision:  rule.Then.Decision,
				Reason:    rule.Then.Reason,
				RuleID:    rule.ID,
				TimeoutMs: rule.Then.TimeoutMs,
			}, nil
		}
	}
	return domain.PolicyResult{
		Decision: e.defaultAction.Decision,
		Reason:   e.defaultAction.Reason,
		RuleID:   "default",
	}, nil
}

func matchesRule(cond domain.PolicyCondition, input domain.PolicyInput) bool {
	if cond.Action != "" && cond.Action != input.Action {
		return false
	}
	if cond.ToolID != "" && !globMatch(cond.ToolID, input.ToolID) {
		return false
	}
	if len(cond.ToolIDs) > 0 && !globMatchAny(cond.ToolIDs, input.ToolID) {
		return false
	}
	if cond.AgentID != "" && cond.AgentID != input.AgentID {
		return false
	}
	if len(cond.AgentIDs) > 0 && !slices.Contains(cond.AgentIDs, input.AgentID) {
		return false
	}
	for k, v := range cond.Labels {
		if input.Labels[k] != v {
			return false
		}
	}
	if len(cond.Arguments) > 0 && !matchArguments(cond.Arguments, input.Arguments) {
		return false
	}
	return true
}

func globMatch(pattern, value string) bool {
	matched, err := path.Match(pattern, value)
	if err != nil {
		return pattern == value
	}
	return matched
}

func globMatchAny(patterns []string, value string) bool {
	for _, p := range patterns {
		if globMatch(p, value) {
			return true
		}
	}
	return false
}

type AgentEngine struct {
	engines      map[string]*RuleEngine
	globalEngine *RuleEngine // nil if no global config
}

func NewAgentEngine(result *LoadDirResult) (*AgentEngine, error) {
	var globalRules []domain.PolicyRule
	var globalDefault domain.PolicyAction
	if result.Global != nil {
		globalRules = result.Global.Rules
		globalDefault = result.Global.Default
	}

	engines := make(map[string]*RuleEngine, len(result.Agents))
	for agentID, cfg := range result.Agents {
		merged := PolicyConfig{
			Rules:   make([]domain.PolicyRule, 0, len(cfg.Rules)+len(globalRules)),
			Default: cfg.Default,
		}
		merged.Rules = append(merged.Rules, cfg.Rules...)
		merged.Rules = append(merged.Rules, globalRules...)

		engine, err := NewRuleEngine(merged)
		if err != nil {
			return nil, fmt.Errorf("building engine for agent %q: %w", agentID, err)
		}
		engines[agentID] = engine
	}

	var globalEngine *RuleEngine
	if result.Global != nil {
		var err error
		globalEngine, err = NewRuleEngine(PolicyConfig{
			Rules:   globalRules,
			Default: globalDefault,
		})
		if err != nil {
			return nil, fmt.Errorf("building global engine: %w", err)
		}
	}

	return &AgentEngine{engines: engines, globalEngine: globalEngine}, nil
}

func (e *AgentEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	engine, ok := e.engines[input.AgentID]
	if !ok {
		if e.globalEngine != nil {
			return e.globalEngine.Evaluate(ctx, input)
		}
		return domain.PolicyResult{
			Decision: domain.PolicyDeny,
			Reason:   fmt.Sprintf("no policy configured for agent %q", input.AgentID),
			RuleID:   "agent-default",
		}, nil
	}
	return engine.Evaluate(ctx, input)
}

func (e *AgentEngine) Agents() []string {
	agents := make([]string, 0, len(e.engines))
	for id := range e.engines {
		agents = append(agents, id)
	}
	slices.Sort(agents)
	return agents
}

type SecureDefaultEngine struct {
	inner Engine
}

func NewSecureDefaultEngine(inner Engine) *SecureDefaultEngine {
	return &SecureDefaultEngine{inner: inner}
}

func (e *SecureDefaultEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	switch input.Action {
	case "execution.complete", "execution.fail", "execution.wait":
		return domain.PolicyResult{
			Decision: domain.PolicyAllow,
			Reason:   "Execution lifecycle action allowed by default",
			RuleID:   "secure-default",
		}, nil
	default:
		if e.inner != nil {
			return e.inner.Evaluate(ctx, input)
		}
		return domain.PolicyResult{
			Decision: domain.PolicyDeny,
			Reason:   "No policy engine configured",
			RuleID:   "secure-default",
		}, nil
	}
}

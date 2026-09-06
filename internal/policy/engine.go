package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/rebuno/rebuno/internal/domain"
)

type Engine interface {
	Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error)
}

// Rules are evaluated in the order they appear in the bundle; the first match
// wins.
type Rule struct {
	ID   string              `yaml:"id"`
	When Condition           `yaml:"when"`
	Then domain.PolicyResult `yaml:"then"`
}

type Condition struct {
	Target    string                  `yaml:"target,omitempty"`
	Targets   []string                `yaml:"targets,omitempty"`
	AgentID   string                  `yaml:"agent_id,omitempty"`
	AgentIDs  []string                `yaml:"agent_ids,omitempty"`
	StepKind  string                  `yaml:"step_kind,omitempty"`
	Arguments map[string]ArgPredicate `yaml:"arguments,omitempty"`
}

type ArgPredicate struct {
	Equals   string         `yaml:"equals,omitempty"`
	Contains string         `yaml:"contains,omitempty"`
	OneOf    []string       `yaml:"one_of,omitempty"`
	Regex    string         `yaml:"regex,omitempty"`
	rx       *regexp.Regexp // compiled at engine construction
}

type Config struct {
	DefaultAction string `yaml:"default_action,omitempty"`
	Rules         []Rule `yaml:"rules"`
}

type RuleEngine struct {
	rules         []Rule
	defaultResult domain.PolicyResult
}

func NewRuleEngine(cfg Config) (*RuleEngine, error) {
	if err := oneOf("default_action", cfg.DefaultAction, domain.DecisionAllow, domain.DecisionDeny); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, r := range cfg.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("rule missing id")
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if r.Then.Decision == "" {
			return nil, fmt.Errorf("rule %q: missing decision", r.ID)
		}
		for _, err := range []error{
			oneOf("decision", r.Then.Decision, domain.DecisionAllow, domain.DecisionDeny, domain.DecisionRequireApproval),
			oneOf("per_what", r.Then.RateLimit.PerWhat, domain.PerWhatExecution, domain.PerWhatAgent, domain.PerWhatGlobal),
			oneOf("on_limiter_error", r.Then.RateLimit.OnLimiterError, domain.LimiterErrorAllow, domain.LimiterErrorDeny),
			oneOf("on_exceed", r.Then.Budget.OnExceed, domain.DecisionDeny, domain.DecisionRequireApproval),
		} {
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
		}
	}
	rules := make([]Rule, len(cfg.Rules))
	copy(rules, cfg.Rules)

	def := domain.PolicyResult{Decision: domain.DecisionDeny, Reason: "no explicit allow rule matched", RuleID: "default"}
	if cfg.DefaultAction == domain.DecisionAllow {
		def = domain.PolicyResult{Decision: domain.DecisionAllow, Reason: "default allow", RuleID: "default"}
	}

	for i := range rules {
		for key, pred := range rules[i].When.Arguments {
			if pred.Equals == "" && pred.Contains == "" && pred.Regex == "" && len(pred.OneOf) == 0 {
				return nil, fmt.Errorf("rule %q argument %q has no constraint (equals/contains/one_of/regex); an empty predicate matches any value", rules[i].ID, key)
			}
			if pred.Regex == "" {
				continue
			}
			rx, err := regexp.Compile(pred.Regex)
			if err != nil {
				return nil, fmt.Errorf("rule %q argument %q invalid regex: %w", rules[i].ID, key, err)
			}
			pred.rx = rx
			rules[i].When.Arguments[key] = pred
		}
	}

	return &RuleEngine{rules: rules, defaultResult: def}, nil
}

// An empty value keeps the documented default.
func oneOf(field, value string, valid ...string) error {
	if value == "" || slices.Contains(valid, value) {
		return nil
	}
	return fmt.Errorf("unknown %s %q (want %s)", field, value, strings.Join(valid, ", "))
}

func NewRuleEngineFromBundle(bundleYAML string) (*RuleEngine, error) {
	cfg, err := LoadBundle(bundleYAML)
	if err != nil {
		return nil, err
	}
	return NewRuleEngine(cfg)
}

func (e *RuleEngine) RuleIDs() []string {
	ids := make([]string, len(e.rules))
	for i, r := range e.rules {
		ids[i] = r.ID
	}
	return ids
}

func (e *RuleEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	for _, rule := range e.rules {
		if matches(rule.When, input) {
			res := rule.Then
			if res.RuleID == "" {
				res.RuleID = rule.ID
			}
			return res, nil
		}
	}
	if input.StepKind == domain.StepKindLocal {
		return domain.PolicyResult{Decision: domain.DecisionAllow, Reason: "local step", RuleID: "local"}, nil
	}
	return e.defaultResult, nil
}

func matches(cond Condition, input domain.PolicyInput) bool {
	if cond.Target != "" && !globMatch(cond.Target, input.Target) {
		return false
	}
	if len(cond.Targets) > 0 && !globMatchAny(cond.Targets, input.Target) {
		return false
	}
	if cond.AgentID != "" && cond.AgentID != input.AgentID {
		return false
	}
	if len(cond.AgentIDs) > 0 && !slices.Contains(cond.AgentIDs, input.AgentID) {
		return false
	}
	if cond.StepKind != "" && cond.StepKind != string(input.StepKind) {
		return false
	}
	if len(cond.Arguments) > 0 && !matchArguments(cond.Arguments, input.Args) {
		return false
	}
	return true
}

func globMatch(pattern, value string) bool {
	if pattern == value {
		return true
	}
	m, err := path.Match(pattern, value)
	if err != nil {
		return false
	}
	return m
}

func globMatchAny(patterns []string, value string) bool {
	for _, p := range patterns {
		if globMatch(p, value) {
			return true
		}
	}
	return false
}

func matchArguments(predicates map[string]ArgPredicate, args []byte) bool {
	var obj map[string]any
	if err := json.Unmarshal(args, &obj); err != nil {
		return false
	}
	for key, pred := range predicates {
		v, ok := obj[key]
		if !ok {
			return false
		}
		s := fmt.Sprintf("%v", v)
		if pred.Equals != "" && pred.Equals != s {
			return false
		}
		if pred.Contains != "" && !strings.Contains(s, pred.Contains) {
			return false
		}
		if len(pred.OneOf) > 0 && !slices.Contains(pred.OneOf, s) {
			return false
		}
		if pred.Regex != "" {
			if pred.rx == nil || !pred.rx.MatchString(s) {
				return false
			}
		}
	}
	return true
}

type PermissiveEngine struct{}

func (PermissiveEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.DecisionAllow, RuleID: "permissive"}, nil
}

type DenyAllEngine struct{}

func (DenyAllEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.DecisionDeny, Reason: "denied by default", RuleID: "deny-all"}, nil
}

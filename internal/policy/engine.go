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

type Rule struct {
	ID       string              `yaml:"id"`
	Priority int                 `yaml:"priority"`
	When     Condition           `yaml:"when"`
	Then     domain.PolicyResult `yaml:"then"`
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
	seen := make(map[int]string)
	for _, r := range cfg.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("rule missing id")
		}
		if existing, ok := seen[r.Priority]; ok {
			return nil, fmt.Errorf("duplicate priority %d used by %q and %q", r.Priority, existing, r.ID)
		}
		seen[r.Priority] = r.ID
	}
	rules := make([]Rule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	slices.SortFunc(rules, func(a, b Rule) int { return a.Priority - b.Priority })

	def := domain.PolicyResult{Decision: domain.DecisionDeny, Reason: "no explicit allow rule matched", RuleID: "default"}
	if cfg.DefaultAction == domain.DecisionAllow {
		def = domain.PolicyResult{Decision: domain.DecisionAllow, Reason: "default allow", RuleID: "default"}
	}

	for i := range rules {
		for key, pred := range rules[i].When.Arguments {
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

func NewRuleEngineFromBundle(bundleYAML string) (*RuleEngine, error) {
	cfg, err := LoadBundle(bundleYAML)
	if err != nil {
		return nil, err
	}
	return NewRuleEngine(cfg)
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

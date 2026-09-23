package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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
	targetRx  *regexp.Regexp
	targetsRx []*regexp.Regexp
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
	Judge         *Judge
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
			oneOf("decision", r.Then.Decision, domain.DecisionAllow, domain.DecisionDeny, domain.DecisionRequireApproval, domain.DecisionJudge),
			oneOf("per_what", r.Then.RateLimit.PerWhat, domain.PerWhatExecution, domain.PerWhatAgent, domain.PerWhatGlobal),
			oneOf("on_limiter_error", r.Then.RateLimit.OnLimiterError, domain.LimiterErrorAllow, domain.LimiterErrorDeny),
			oneOf("on_exceed", r.Then.Budget.OnExceed, domain.DecisionDeny, domain.DecisionRequireApproval),
			oneOf("fallback", r.Then.Judge.Fallback, domain.DecisionAllow, domain.DecisionDeny, domain.DecisionRequireApproval),
		} {
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
		}
		if r.Then.Judge != (domain.JudgeConfig{}) && r.Then.Decision != domain.DecisionJudge {
			return nil, fmt.Errorf("rule %q: judge is set but decision is %q", r.ID, r.Then.Decision)
		}
		if t := r.Then.Judge.Threshold; t < 0 || t > 1 {
			return nil, fmt.Errorf("rule %q: threshold %v outside 0 to 1", r.ID, t)
		}
	}
	rules := make([]Rule, len(cfg.Rules))
	copy(rules, cfg.Rules)

	def := domain.PolicyResult{Decision: domain.DecisionDeny, Reason: "no explicit allow rule matched", RuleID: "default"}
	if cfg.DefaultAction == domain.DecisionAllow {
		def = domain.PolicyResult{Decision: domain.DecisionAllow, Reason: "default allow", RuleID: "default"}
	}

	for i := range rules {
		when := &rules[i].When
		if when.Target != "" {
			rx, err := compileGlob(when.Target)
			if err != nil {
				return nil, fmt.Errorf("rule %q target %q: %w", rules[i].ID, when.Target, err)
			}
			when.targetRx = rx
		}
		for _, p := range when.Targets {
			rx, err := compileGlob(p)
			if err != nil {
				return nil, fmt.Errorf("rule %q target %q: %w", rules[i].ID, p, err)
			}
			when.targetsRx = append(when.targetsRx, rx)
		}
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
			if res.Decision == domain.DecisionJudge {
				res.Decision, res.Reason = e.Judge.decide(ctx, input, res.Judge)
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
	if cond.targetRx != nil && !cond.targetRx.MatchString(input.Target) {
		return false
	}
	if len(cond.targetsRx) > 0 && !slices.ContainsFunc(cond.targetsRx, func(rx *regexp.Regexp) bool {
		return rx.MatchString(input.Target)
	}) {
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

func compileGlob(pattern string) (*regexp.Regexp, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(`(?s)^`)
	next := func(i int) (string, int) {
		if pattern[i] == '\\' {
			i++
		}
		r, n := utf8.DecodeRuneInString(pattern[i:])
		if r < utf8.RuneSelf && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return `\` + string(r), i + n
		}
		return string(r), i + n
	}
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			b.WriteString(`.*`)
			i++
		case '?':
			b.WriteString(`.`)
			i++
		case '[':
			b.WriteByte('[')
			i++
			if pattern[i] == '^' {
				b.WriteByte('^')
				i++
			}
			for first := true; first || pattern[i] != ']'; first = false {
				var lit string
				lit, i = next(i)
				b.WriteString(lit)
				if pattern[i] == '-' {
					lit, i = next(i + 1)
					b.WriteString("-" + lit)
				}
			}
			b.WriteByte(']')
			i++
		default:
			var lit string
			lit, i = next(i)
			b.WriteString(lit)
		}
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

func matchArguments(predicates map[string]ArgPredicate, args []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return false
	}
	for key, pred := range predicates {
		v, ok := obj[key]
		if !ok {
			return false
		}
		s := argString(v)
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

func argString(v json.RawMessage) string {
	var s string
	if v[0] == '"' && json.Unmarshal(v, &s) == nil {
		return s
	}
	var b bytes.Buffer
	_ = json.Compact(&b, v)
	return b.String()
}

type PermissiveEngine struct{}

func (PermissiveEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.DecisionAllow, RuleID: "permissive"}, nil
}

type DenyAllEngine struct{}

func (DenyAllEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.DecisionDeny, Reason: "denied by default", RuleID: "deny-all"}, nil
}

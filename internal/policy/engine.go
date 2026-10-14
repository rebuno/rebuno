package policy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
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
	History   []HistoryClause         `yaml:"history,omitempty"`
	targetRx  *regexp.Regexp
	targetsRx []*regexp.Regexp
}

// The step being decided counts toward a clause it matches unless the clause
// has result predicates.
type HistoryClause struct {
	Condition `yaml:",inline"`
	Scope     string                  `yaml:"scope,omitempty"`
	Within    time.Duration           `yaml:"within,omitempty"`
	Result    map[string]ArgPredicate `yaml:"result,omitempty"`
	Sum       string                  `yaml:"sum,omitempty"`
	Distinct  string                  `yaml:"distinct,omitempty"`
	AtLeast   *float64                `yaml:"at_least,omitempty"`
	AtMost    *float64                `yaml:"at_most,omitempty"`
}

type ArgPredicate struct {
	Equals    string         `yaml:"equals,omitempty"`
	EqualsArg string         `yaml:"equals_arg,omitempty"`
	Contains  string         `yaml:"contains,omitempty"`
	OneOf     []string       `yaml:"one_of,omitempty"`
	Regex     string         `yaml:"regex,omitempty"`
	rx        *regexp.Regexp // compiled at engine construction
}

type Config struct {
	DefaultAction string `yaml:"default_action,omitempty"`
	Rules         []Rule `yaml:"rules"`
}

type RuleEngine struct {
	rules         []Rule
	defaultResult domain.PolicyResult
	hash          string
	Judge         *Judge
	// Widest window per history scope; zero reads the whole scope.
	lookback map[string]time.Duration
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
			oneOf("per_what", r.Then.RateLimit.PerWhat, domain.PerWhatExecution, domain.PerWhatSession, domain.PerWhatAgent, domain.PerWhatGlobal),
			oneOf("on_limiter_error", r.Then.RateLimit.OnLimiterError, domain.LimiterErrorAllow, domain.LimiterErrorDeny),
			oneOf("scope", r.Then.Budget.Scope, domain.BudgetScopeExecution, domain.BudgetScopeSession),
			oneOf("on_exceed", r.Then.Budget.OnExceed, domain.DecisionDeny, domain.DecisionRequireApproval),
			oneOf("fallback", r.Then.Judge.Fallback, domain.DecisionAllow, domain.DecisionDeny, domain.DecisionRequireApproval),
		} {
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
		}
		if !reflect.DeepEqual(r.Then.Judge, domain.JudgeConfig{}) && r.Then.Decision != domain.DecisionJudge {
			return nil, fmt.Errorf("rule %q: judge is set but decision is %q", r.ID, r.Then.Decision)
		}
		if t := r.Then.Judge.Threshold; t < 0 || t > 1 {
			return nil, fmt.Errorf("rule %q: threshold %v outside 0 to 1", r.ID, t)
		}
		for d := range r.Then.Judge.Criteria {
			if err := oneOf("criteria", d, domain.DecisionAllow, domain.DecisionDeny, domain.DecisionRequireApproval); err != nil {
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

	lookback := make(map[string]time.Duration)
	for i := range rules {
		if err := compileCondition(&rules[i].When, false); err != nil {
			return nil, fmt.Errorf("rule %q %w", rules[i].ID, err)
		}
		for j := range rules[i].When.History {
			h := &rules[i].When.History[j]
			if err := compileHistoryClause(h); err != nil {
				return nil, fmt.Errorf("rule %q history %d: %w", rules[i].ID, j+1, err)
			}
			if prev, seen := lookback[h.Scope]; !seen || (prev != 0 && (h.Within == 0 || h.Within > prev)) {
				lookback[h.Scope] = h.Within
			}
		}
	}

	return &RuleEngine{rules: rules, defaultResult: def, lookback: lookback}, nil
}

func compileCondition(when *Condition, inHistory bool) error {
	if when.Target != "" {
		rx, err := compileGlob(when.Target)
		if err != nil {
			return fmt.Errorf("target %q: %w", when.Target, err)
		}
		when.targetRx = rx
	}
	for _, p := range when.Targets {
		rx, err := compileGlob(p)
		if err != nil {
			return fmt.Errorf("target %q: %w", p, err)
		}
		when.targetsRx = append(when.targetsRx, rx)
	}
	return compilePredicates("argument", when.Arguments, inHistory)
}

func compilePredicates(field string, preds map[string]ArgPredicate, allowEqualsArg bool) error {
	for key, pred := range preds {
		if pred.Equals == "" && pred.EqualsArg == "" && pred.Contains == "" && pred.Regex == "" && len(pred.OneOf) == 0 {
			return fmt.Errorf("%s %q has no constraint (equals/equals_arg/contains/one_of/regex); an empty predicate matches any value", field, key)
		}
		if pred.EqualsArg != "" && !allowEqualsArg {
			return fmt.Errorf("%s %q: equals_arg is only valid in history clauses", field, key)
		}
		if pred.Regex == "" {
			continue
		}
		rx, err := regexp.Compile(pred.Regex)
		if err != nil {
			return fmt.Errorf("%s %q invalid regex: %w", field, key, err)
		}
		pred.rx = rx
		preds[key] = pred
	}
	return nil
}

func compileHistoryClause(h *HistoryClause) error {
	if len(h.History) > 0 {
		return fmt.Errorf("history clauses cannot nest history")
	}
	if h.AgentID != "" || len(h.AgentIDs) > 0 {
		return fmt.Errorf("agent_id is not supported in history clauses")
	}
	if h.Scope == "" {
		h.Scope = domain.HistoryScopeExecution
	}
	if err := oneOf("scope", h.Scope, domain.HistoryScopeExecution, domain.HistoryScopeSession); err != nil {
		return err
	}
	if h.Within < 0 {
		return fmt.Errorf("within %v is negative", h.Within)
	}
	if h.Sum != "" && h.Distinct != "" {
		return fmt.Errorf("sum and distinct are mutually exclusive")
	}
	if h.AtLeast != nil && h.AtMost != nil && *h.AtLeast > *h.AtMost {
		return fmt.Errorf("at_least %v exceeds at_most %v", *h.AtLeast, *h.AtMost)
	}
	if h.AtLeast == nil && h.AtMost == nil {
		one := 1.0
		h.AtLeast = &one
	}
	if err := compileCondition(&h.Condition, true); err != nil {
		return err
	}
	return compilePredicates("result", h.Result, true)
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
	engine, err := NewRuleEngine(cfg)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(bundleYAML))
	engine.hash = hex.EncodeToString(sum[:])
	return engine, nil
}

func (e *RuleEngine) RuleIDs() []string {
	ids := make([]string, len(e.rules))
	for i, r := range e.rules {
		ids[i] = r.ID
	}
	return ids
}

func (e *RuleEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	res := e.evaluate(ctx, input)
	res.PolicyHash = e.hash
	return res, nil
}

func (e *RuleEngine) evaluate(ctx context.Context, input domain.PolicyInput) domain.PolicyResult {
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	hist := &historyView{engine: e, input: input, steps: make(map[string][]domain.Step)}
	for _, rule := range e.rules {
		if !matches(rule.When, input, input.Args) {
			continue
		}
		ok, err := hist.matchAll(ctx, rule.When.History)
		if err != nil {
			return domain.PolicyResult{Decision: domain.DecisionDeny, Reason: domain.ReasonHistoryUnavailable, RuleID: rule.ID}
		}
		if ok {
			res := rule.Then
			if res.RuleID == "" {
				res.RuleID = rule.ID
			}
			if res.Decision == domain.DecisionJudge {
				res.Decision, res.Reason = e.Judge.decide(ctx, input, res.Judge)
			}
			return res
		}
	}
	if input.StepKind == domain.StepKindLocal {
		return domain.PolicyResult{Decision: domain.DecisionAllow, Reason: "local step", RuleID: "local"}
	}
	return e.defaultResult
}

func matches(cond Condition, input domain.PolicyInput, current json.RawMessage) bool {
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
	if len(cond.Arguments) > 0 && !matchArguments(cond.Arguments, input.Args, current) {
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

func matchArguments(predicates map[string]ArgPredicate, args, current []byte) bool {
	var obj, cur map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return false
	}
	for key, pred := range predicates {
		v, ok := lookupArgument(obj, key)
		if !ok {
			return false
		}
		s := argString(v)
		if pred.Equals != "" && pred.Equals != s {
			return false
		}
		if pred.EqualsArg != "" {
			if cur == nil && json.Unmarshal(current, &cur) != nil {
				return false
			}
			cv, ok := lookupArgument(cur, pred.EqualsArg)
			if !ok || argString(cv) != s {
				return false
			}
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

// lookupArgument resolves key as an argument name, then as a dotted path
// through nested objects. A name that contains a dot takes precedence over the
// path it spells.
func lookupArgument(obj map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	if v, ok := obj[key]; ok {
		return v, true
	}
	head, rest, found := strings.Cut(key, ".")
	if !found {
		return nil, false
	}
	var nested map[string]json.RawMessage
	if v, ok := obj[head]; !ok || json.Unmarshal(v, &nested) != nil {
		return nil, false
	}
	return lookupArgument(nested, rest)
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

type historyView struct {
	engine *RuleEngine
	input  domain.PolicyInput
	steps  map[string][]domain.Step
}

func (h *historyView) load(ctx context.Context, scope string) ([]domain.Step, error) {
	if steps, ok := h.steps[scope]; ok {
		return steps, nil
	}
	var steps []domain.Step
	if h.input.History != nil {
		var since time.Time
		if w := h.engine.lookback[scope]; w > 0 {
			since = h.input.Now.Add(-w)
		}
		var err error
		if steps, err = h.input.History(ctx, scope, since); err != nil {
			return nil, err
		}
	}
	h.steps[scope] = steps
	return steps, nil
}

func (h *historyView) matchAll(ctx context.Context, clauses []HistoryClause) (bool, error) {
	for _, c := range clauses {
		steps, err := h.load(ctx, c.Scope)
		if err != nil {
			return false, err
		}
		if !matchClause(c, steps, h.input) {
			return false, nil
		}
	}
	return true, nil
}

func matchClause(c HistoryClause, steps []domain.Step, input domain.PolicyInput) bool {
	var count, sum float64
	distinct := make(map[string]bool)
	add := func(args json.RawMessage) bool {
		count++
		switch {
		case c.Sum != "":
			n, ok := numericArgument(args, c.Sum)
			if !ok {
				return false
			}
			sum += n
		case c.Distinct != "":
			var obj map[string]json.RawMessage
			if json.Unmarshal(args, &obj) == nil {
				if v, ok := lookupArgument(obj, c.Distinct); ok {
					distinct[argString(v)] = true
				}
			}
		}
		return true
	}
	for _, s := range steps {
		if c.Within > 0 && (s.StartedAt == nil || s.StartedAt.Before(input.Now.Add(-c.Within))) {
			continue
		}
		prior := domain.PolicyInput{Target: s.Target, Args: s.Args, StepKind: s.Kind}
		if !matches(c.Condition, prior, input.Args) {
			continue
		}
		if len(c.Result) > 0 && (s.Status != domain.StepSucceeded || !matchArguments(c.Result, s.Result, input.Args)) {
			continue
		}
		if !add(s.Args) {
			return false
		}
	}
	if len(c.Result) == 0 && matches(c.Condition, input, input.Args) && !add(input.Args) {
		return false
	}
	value := count
	switch {
	case c.Sum != "":
		value = sum
	case c.Distinct != "":
		value = float64(len(distinct))
	}
	if c.AtLeast != nil && value < *c.AtLeast {
		return false
	}
	return c.AtMost == nil || value <= *c.AtMost
}

func numericArgument(args json.RawMessage, key string) (float64, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return 0, false
	}
	v, ok := lookupArgument(obj, key)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseFloat(argString(v), 64)
	return n, err == nil
}

type PermissiveEngine struct{}

func (PermissiveEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.DecisionAllow, RuleID: "permissive"}, nil
}

type DenyAllEngine struct{}

func (DenyAllEngine) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: domain.DecisionDeny, Reason: "denied by default", RuleID: "deny-all"}, nil
}

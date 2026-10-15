package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rebuno/rebuno/internal/domain"
)

type Case struct {
	Name          string          `yaml:"name,omitempty" json:"name,omitempty"`
	AgentID       string          `yaml:"agent_id,omitempty" json:"agent_id,omitempty"`
	Subject       string          `yaml:"subject,omitempty" json:"subject,omitempty"`
	SubjectGroups []string        `yaml:"subject_groups,omitempty" json:"subject_groups,omitempty"`
	Kind          domain.StepKind `yaml:"kind,omitempty" json:"kind,omitempty"`
	Target        string          `yaml:"target" json:"target"`
	Args          map[string]any  `yaml:"args,omitempty" json:"args,omitempty"`
	Expect        string          `yaml:"expect,omitempty" json:"expect,omitempty"`
	ExpectRule    string          `yaml:"expect_rule,omitempty" json:"expect_rule,omitempty"`
	History       []PriorStep     `yaml:"history,omitempty" json:"history,omitempty"`

	ExecutionID string `yaml:"-" json:"execution_id,omitempty"`
	StepID      string `yaml:"-" json:"step_id,omitempty"`
	WasRule     string `yaml:"-" json:"was_rule,omitempty"`

	allowed   []domain.Step
	decidedAt time.Time
}

type PriorStep struct {
	Kind   domain.StepKind `yaml:"kind,omitempty" json:"kind,omitempty"`
	Target string          `yaml:"target" json:"target"`
	Args   map[string]any  `yaml:"args,omitempty" json:"args,omitempty"`
	Result any             `yaml:"result,omitempty" json:"result,omitempty"`
	Ago    time.Duration   `yaml:"ago,omitempty" json:"ago,omitempty"`
}

type CaseFile struct {
	AgentID string `yaml:"agent_id,omitempty"`
	Cases   []Case `yaml:"cases"`
}

type Result struct {
	Case
	Decision string `json:"decision"`
	RuleID   string `json:"rule_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Pass     bool   `json:"pass"`
	Failure  string `json:"failure,omitempty"`
}

type Report struct {
	Results          []Result `json:"results"`
	Failed           int      `json:"failed"`
	UnexercisedRules []string `json:"unexercised_rules,omitempty"`
}

func (c Case) Label() string {
	if c.Name != "" {
		return c.Name
	}
	if len(c.Args) == 0 {
		return c.Target
	}
	args, err := json.Marshal(c.Args)
	if err != nil {
		return c.Target
	}
	return c.Target + " " + string(args)
}

// Unknown keys are rejected: a misspelled `expect` would turn an assertion
// into a case that can never fail.
func LoadCases(caseYAML string) ([]Case, error) {
	dec := yaml.NewDecoder(strings.NewReader(caseYAML))
	dec.KnownFields(true)
	var f CaseFile
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	if err := NormalizeCases(f.Cases, f.AgentID); err != nil {
		return nil, err
	}
	return f.Cases, nil
}

func NormalizeCases(cases []Case, defaultAgentID string) error {
	for i := range cases {
		c := &cases[i]
		if c.Target == "" {
			return fmt.Errorf("case %d: missing target", i+1)
		}
		if c.AgentID == "" {
			c.AgentID = defaultAgentID
		}
		if c.Kind == "" {
			c.Kind = domain.StepKindTool
		}
		switch c.Kind {
		case domain.StepKindTool, domain.StepKindLLM, domain.StepKindLocal:
		default:
			return fmt.Errorf("case %d: unknown kind %q", i+1, c.Kind)
		}
		switch c.Expect {
		case "", domain.DecisionAllow, domain.DecisionDeny, domain.DecisionRequireApproval:
		default:
			return fmt.Errorf("case %d: unknown expect %q", i+1, c.Expect)
		}
		for j := range c.History {
			p := &c.History[j]
			if p.Target == "" {
				return fmt.Errorf("case %d history %d: missing target", i+1, j+1)
			}
			if p.Kind == "" {
				p.Kind = domain.StepKindTool
			}
		}
	}
	return nil
}

func (c Case) history(now time.Time) (func(context.Context, string, time.Time) ([]domain.Step, error), error) {
	if c.allowed != nil {
		return func(_ context.Context, scope string, since time.Time) ([]domain.Step, error) {
			var out []domain.Step
			for _, s := range c.allowed {
				if s.StepID == c.StepID || s.StartedAt.Before(since) || s.StartedAt.After(now) {
					continue
				}
				if scope == domain.HistoryScopeExecution && s.ExecutionID.String() != c.ExecutionID {
					continue
				}
				out = append(out, s)
			}
			return out, nil
		}, nil
	}
	if len(c.History) == 0 {
		return nil, nil
	}
	steps := make([]domain.Step, 0, len(c.History))
	for _, p := range c.History {
		args, err := json.Marshal(p.Args)
		if err != nil {
			return nil, err
		}
		var result json.RawMessage
		if p.Result != nil {
			if result, err = json.Marshal(p.Result); err != nil {
				return nil, err
			}
		}
		started := now.Add(-p.Ago)
		steps = append(steps, domain.Step{
			Kind: p.Kind, Target: p.Target, Args: args, Result: result,
			Status: domain.StepSucceeded, StartedAt: &started,
		})
	}
	return func(_ context.Context, _ string, since time.Time) ([]domain.Step, error) {
		var out []domain.Step
		for _, s := range steps {
			if !s.StartedAt.Before(since) {
				out = append(out, s)
			}
		}
		return out, nil
	}, nil
}

func Run(ctx context.Context, engine *RuleEngine, cases []Case) (Report, error) {
	report := Report{Results: make([]Result, 0, len(cases))}
	hit := make(map[string]bool, len(cases))
	for _, c := range cases {
		args, err := json.Marshal(c.Args)
		if err != nil {
			return Report{}, fmt.Errorf("case %q: %w", c.Label(), err)
		}
		now := c.decidedAt
		if now.IsZero() {
			now = time.Now().UTC()
		}
		history, err := c.history(now)
		if err != nil {
			return Report{}, fmt.Errorf("case %q: %w", c.Label(), err)
		}
		res, err := engine.Evaluate(ctx, domain.PolicyInput{
			AgentID:       c.AgentID,
			Subject:       c.Subject,
			SubjectGroups: c.SubjectGroups,
			Target:        c.Target,
			Args:          args,
			StepKind:      c.Kind,
			Now:           now,
			History:       history,
		})
		if err != nil {
			return Report{}, fmt.Errorf("case %q: %w", c.Label(), err)
		}
		hit[res.RuleID] = true
		out := evaluate(c, res)
		if !out.Pass {
			report.Failed++
		}
		report.Results = append(report.Results, out)
	}
	for _, id := range engine.RuleIDs() {
		if !hit[id] {
			report.UnexercisedRules = append(report.UnexercisedRules, id)
		}
	}
	return report, nil
}

func evaluate(c Case, res domain.PolicyResult) Result {
	out := Result{Case: c, Decision: res.Decision, RuleID: res.RuleID, Reason: res.Reason, Pass: true}
	switch {
	case c.Expect != "" && c.Expect != res.Decision:
		out.Pass = false
		out.Failure = fmt.Sprintf("%s, want %s", res.Decision, c.Expect)
	case c.ExpectRule != "" && c.ExpectRule != res.RuleID:
		out.Pass = false
		out.Failure = fmt.Sprintf("rule %s, want %s", res.RuleID, c.ExpectRule)
	}
	return out
}

type recorded struct {
	decision string
	ruleID   string
	at       time.Time
}

func IsDecisionEvent(eventType string) bool {
	switch eventType {
	case domain.EventStepAllowed, domain.EventStepDenied, domain.EventStepAwaitingApproval:
		return true
	}
	return false
}

func ReplayCases(steps []domain.Step, events []domain.Event, allowed []domain.Step) []Case {
	seen := make(map[string]recorded, len(steps))
	for _, e := range events {
		id, rec, ok := recordedDecision(e)
		if !ok {
			continue
		}
		// Granting an approval writes a second decision event for the same
		// step; the first is the policy's.
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = rec
	}
	cases := make([]Case, 0, len(steps))
	for _, s := range steps {
		c := Case{
			ExecutionID: s.ExecutionID.String(),
			StepID:      s.StepID,
			Kind:        s.Kind,
			Target:      s.Target,
			Expect:      seen[s.StepID].decision,
			WasRule:     seen[s.StepID].ruleID,
			allowed:     allowed,
			decidedAt:   seen[s.StepID].at,
		}
		if len(s.Args) > 0 {
			_ = json.Unmarshal(s.Args, &c.Args)
		}
		cases = append(cases, c)
	}
	return cases
}

func recordedDecision(e domain.Event) (string, recorded, bool) {
	if !IsDecisionEvent(e.Type) {
		return "", recorded{}, false
	}
	var payload struct {
		StepID string `json:"step_id"`
		RuleID string `json:"rule_id"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil || payload.StepID == "" {
		return "", recorded{}, false
	}
	if payload.RuleID == domain.RuleIndeterminateRetry {
		return "", recorded{}, false
	}
	return payload.StepID, recorded{decision: decisionForEvent(e.Type), ruleID: payload.RuleID, at: e.OccurredAt}, true
}

func decisionForEvent(eventType string) string {
	switch eventType {
	case domain.EventStepAllowed:
		return domain.DecisionAllow
	case domain.EventStepAwaitingApproval:
		return domain.DecisionRequireApproval
	default:
		return domain.DecisionDeny
	}
}

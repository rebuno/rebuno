package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rebuno/rebuno/internal/domain"
)

type Case struct {
	Name       string          `yaml:"name,omitempty" json:"name,omitempty"`
	AgentID    string          `yaml:"agent_id,omitempty" json:"agent_id,omitempty"`
	Kind       domain.StepKind `yaml:"kind,omitempty" json:"kind,omitempty"`
	Target     string          `yaml:"target" json:"target"`
	Args       map[string]any  `yaml:"args,omitempty" json:"args,omitempty"`
	Expect     string          `yaml:"expect,omitempty" json:"expect,omitempty"`
	ExpectRule string          `yaml:"expect_rule,omitempty" json:"expect_rule,omitempty"`

	StepID  string `yaml:"-" json:"step_id,omitempty"`
	WasRule string `yaml:"-" json:"was_rule,omitempty"`
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
	}
	return nil
}

func Run(ctx context.Context, engine *RuleEngine, cases []Case) (Report, error) {
	report := Report{Results: make([]Result, 0, len(cases))}
	hit := make(map[string]bool, len(cases))
	for _, c := range cases {
		args, err := json.Marshal(c.Args)
		if err != nil {
			return Report{}, fmt.Errorf("case %q: %w", c.Label(), err)
		}
		res, err := engine.Evaluate(ctx, domain.PolicyInput{
			AgentID:  c.AgentID,
			Target:   c.Target,
			Args:     args,
			StepKind: c.Kind,
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
}

func IsDecisionEvent(eventType string) bool {
	switch eventType {
	case domain.EventStepAllowed, domain.EventStepDenied, domain.EventStepAwaitingApproval:
		return true
	}
	return false
}

func ReplayCases(steps []domain.Step, events []domain.Event) []Case {
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
			StepID:  s.StepID,
			Kind:    s.Kind,
			Target:  s.Target,
			Expect:  seen[s.StepID].decision,
			WasRule: seen[s.StepID].ruleID,
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
	return payload.StepID, recorded{decision: decisionForEvent(e.Type), ruleID: payload.RuleID}, true
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

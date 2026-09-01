package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

const shellBundle = `
default_action: deny
rules:
  - id: allow-llm
    when:
      step_kind: llm_call
    then:
      decision: allow
  - id: allow-safe-shell
    when:
      target: shell_exec
      arguments:
        command:
          regex: '^(ls|cat)( [^;&|]*)?$'
    then:
      decision: allow
  - id: approve-other-shell
    when:
      target: shell_exec
    then:
      decision: require_approval
`

func decisionEvent(eventType, stepID, ruleID string) domain.Event {
	payload, err := json.Marshal(map[string]string{"step_id": stepID, "rule_id": ruleID})
	if err != nil {
		panic(err)
	}
	return domain.Event{Type: eventType, Payload: payload}
}

func TestRunReportsFailuresAndUnexercisedRules(t *testing.T) {
	engine, err := NewRuleEngineFromBundle(shellBundle)
	if err != nil {
		t.Fatal(err)
	}
	cases := []Case{
		{Name: "safe", Target: "shell_exec", Args: map[string]any{"command": "ls /tmp"}, Expect: domain.DecisionAllow},
		{Name: "chained", Target: "shell_exec", Args: map[string]any{"command": "ls; rm -rf /"}, Expect: domain.DecisionDeny},
		{Name: "wrong rule", Target: "shell_exec", Args: map[string]any{"command": "cat f"}, ExpectRule: "approve-other-shell"},
	}
	if err := NormalizeCases(cases, "shell"); err != nil {
		t.Fatal(err)
	}

	report, err := Run(context.Background(), engine, cases)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 2 {
		t.Fatalf("failed = %d, want 2", report.Failed)
	}
	if got := report.Results[1].Failure; got != "require_approval, want deny" {
		t.Fatalf("chained failure = %q", got)
	}
	if got := report.Results[2].Failure; got != "rule allow-safe-shell, want approve-other-shell" {
		t.Fatalf("wrong-rule failure = %q", got)
	}
	if len(report.UnexercisedRules) != 1 || report.UnexercisedRules[0] != "allow-llm" {
		t.Fatalf("unexercised = %v, want [allow-llm]", report.UnexercisedRules)
	}
}

func TestReplayCasesKeepsFirstDecision(t *testing.T) {
	steps := []domain.Step{
		{StepID: "s1", Kind: domain.StepKindTool, Target: "shell_exec", Args: json.RawMessage(`{"command":"rm -rf /"}`)},
		{StepID: "s2", Kind: domain.StepKindTool, Target: "shell_exec", Args: json.RawMessage(`{"command":"ls"}`)},
	}
	events := []domain.Event{
		decisionEvent(domain.EventStepAwaitingApproval, "s1", "approve-other-shell"),
		decisionEvent(domain.EventStepAllowed, "s1", ""),
		decisionEvent(domain.EventStepAllowed, "s2", "allow-safe-shell"),
	}

	cases := ReplayCases(steps, events)
	if len(cases) != 2 {
		t.Fatalf("cases = %d, want 2", len(cases))
	}
	if cases[0].Expect != domain.DecisionRequireApproval {
		t.Fatalf("s1 expect = %q, want require_approval", cases[0].Expect)
	}
	if cases[0].StepID != "s1" {
		t.Fatalf("s1 step_id = %q", cases[0].StepID)
	}
	if cases[0].WasRule != "approve-other-shell" {
		t.Fatalf("s1 was_rule = %q", cases[0].WasRule)
	}
	if cases[0].Args["command"] != "rm -rf /" {
		t.Fatalf("s1 args = %v", cases[0].Args)
	}
	if cases[1].Expect != domain.DecisionAllow {
		t.Fatalf("s2 expect = %q, want allow", cases[1].Expect)
	}
}

func TestReplayCasesSkipsNonPolicyDenial(t *testing.T) {
	steps := []domain.Step{{StepID: "s1", Kind: domain.StepKindTool, Target: "shell_exec"}}
	events := []domain.Event{decisionEvent(domain.EventStepDenied, "s1", domain.RuleIndeterminateRetry)}

	cases := ReplayCases(steps, events)
	if cases[0].Expect != "" {
		t.Fatalf("expect = %q, want empty", cases[0].Expect)
	}
}

func TestLoadCasesRejectsUnknownField(t *testing.T) {
	_, err := LoadCases("cases:\n  - target: shell_exec\n    expects: allow\n")
	if err == nil {
		t.Fatal("expected an error for a misspelled expect key")
	}
}

func TestLoadCasesAppliesFileDefaults(t *testing.T) {
	cases, err := LoadCases("agent_id: shell\ncases:\n  - target: shell_exec\n  - target: gpt-4o\n    kind: llm_call\n    agent_id: other\n")
	if err != nil {
		t.Fatal(err)
	}
	if cases[0].AgentID != "shell" || cases[0].Kind != domain.StepKindTool {
		t.Fatalf("case 0 = %+v", cases[0])
	}
	if cases[1].AgentID != "other" || cases[1].Kind != domain.StepKindLLM {
		t.Fatalf("case 1 = %+v", cases[1])
	}
}

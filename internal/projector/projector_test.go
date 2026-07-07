package projector

import (
	"testing"

	"github.com/rebuno/kernel/internal/domain"
)

// Event payloads reference the step by id; the step row is the system of record
// for the args/result bodies. Embedding the bodies here would duplicate large
// LLM input/output into the append-only events table (args up to 4x, result 2x).

func TestStepPayloadOmitsArgsBody(t *testing.T) {
	p := StepPayload("step-1", domain.StepKindLLM, "gpt-4", "rule-7")

	if _, ok := p["args"]; ok {
		t.Fatalf("step payload must not embed args body, got: %v", p)
	}
	if p["step_id"] != "step-1" {
		t.Errorf("step_id = %v, want step-1", p["step_id"])
	}
	if p["step_type"] != string(domain.StepKindLLM) {
		t.Errorf("step_type = %v, want %s", p["step_type"], domain.StepKindLLM)
	}
	if p["target"] != "gpt-4" {
		t.Errorf("target = %v, want gpt-4", p["target"])
	}
	if p["rule_id"] != "rule-7" {
		t.Errorf("rule_id = %v, want rule-7", p["rule_id"])
	}
}

func TestStepResultPayloadOmitsResultBody(t *testing.T) {
	p := StepResultPayload("step-1", domain.StepKindLLM)

	if _, ok := p["result"]; ok {
		t.Fatalf("step result payload must not embed result body, got: %v", p)
	}
	if p["step_id"] != "step-1" {
		t.Errorf("step_id = %v, want step-1", p["step_id"])
	}
	if p["step_type"] != string(domain.StepKindLLM) {
		t.Errorf("step_type = %v, want %s", p["step_type"], domain.StepKindLLM)
	}
}

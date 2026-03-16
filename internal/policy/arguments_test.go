package policy

import (
	"encoding/json"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func float64Ptr(f float64) *float64 { return &f }
func intPtr(i int) *int             { return &i }

func TestMatchArguments_Pattern(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "command", Pattern: "^ls"},
	}
	raw := json.RawMessage(`{"command":"ls -la /tmp"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for command starting with ls")
	}

	raw = json.RawMessage(`{"command":"rm -rf /"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for command starting with rm")
	}
}

func TestMatchArguments_OneOf(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "mode", OneOf: []string{"read", "write"}},
	}
	raw := json.RawMessage(`{"mode":"read"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for mode=read")
	}

	raw = json.RawMessage(`{"mode":"delete"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for mode=delete")
	}
}

func TestMatchArguments_MinMax(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "count", Min: float64Ptr(1), Max: float64Ptr(100)},
	}

	raw := json.RawMessage(`{"count":50}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for count=50")
	}

	raw = json.RawMessage(`{"count":0}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for count=0 (below min)")
	}

	raw = json.RawMessage(`{"count":101}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for count=101 (above max)")
	}
}

func TestMatchArguments_MaxLength(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "name", MaxLength: intPtr(5)},
	}

	raw := json.RawMessage(`{"name":"abc"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for name=abc (len 3 <= 5)")
	}

	raw = json.RawMessage(`{"name":"abcdef"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for name=abcdef (len 6 > 5)")
	}
}

func TestMatchArguments_Required(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "token", Required: true},
	}

	raw := json.RawMessage(`{"token":"abc123"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match when required field is present")
	}

	raw = json.RawMessage(`{"other":"value"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match when required field is missing")
	}

	raw = json.RawMessage(`{"token":""}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match when required field is empty string")
	}
}

func TestMatchArguments_MissingFieldWithoutRequired(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "optional_flag", Pattern: "^yes$"},
	}

	raw := json.RawMessage(`{"other":"value"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match when optional field is absent")
	}
}

func TestMatchArguments_MultiplePredicates(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "command", Pattern: "^ls"},
		{Field: "timeout", Max: float64Ptr(30)},
	}

	raw := json.RawMessage(`{"command":"ls -la","timeout":10}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match when both predicates pass")
	}

	raw = json.RawMessage(`{"command":"ls -la","timeout":60}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match when timeout exceeds max")
	}

	raw = json.RawMessage(`{"command":"rm -rf","timeout":10}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match when command pattern fails")
	}
}

func TestMatchArguments_NilArguments(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "command", Pattern: "^ls"},
	}
	if matchArguments(preds, nil) {
		t.Error("expected no match for nil arguments")
	}
}

func TestMatchArguments_InvalidJSON(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "command", Pattern: "^ls"},
	}
	if matchArguments(preds, json.RawMessage(`not json`)) {
		t.Error("expected no match for invalid JSON")
	}
}

func TestMatchArguments_NonStringPattern(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "count", Pattern: "^5$"},
	}
	raw := json.RawMessage(`{"count":5}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match: pattern on non-string field")
	}
}

func TestMatchArguments_MinOnNonNumeric(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "name", Min: float64Ptr(1)},
	}
	raw := json.RawMessage(`{"name":"hello"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match: min on string field")
	}
}

func TestMatchArguments_MaxLengthOnNonString(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "count", MaxLength: intPtr(5)},
	}
	raw := json.RawMessage(`{"count":12345}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match: max_length on numeric field")
	}
}

func TestMatchArguments_MaxLengthBoundary(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "s", MaxLength: intPtr(3)},
	}
	raw := json.RawMessage(`{"s":"abc"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for len=3, max_length=3")
	}
}

func TestMatchArguments_EmptyPredicatesWithEmptyArgs(t *testing.T) {
	raw := json.RawMessage(`{"command":"ls"}`)
	if !matchArguments(nil, raw) {
		t.Error("expected match for nil predicates")
	}
}

func TestMatchArguments_InvalidRegexPattern(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "command", Pattern: "[invalid("},
	}
	raw := json.RawMessage(`{"command":"anything"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for invalid regex pattern")
	}
}

func TestMatchArguments_MinOnly(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "count", Min: float64Ptr(5)},
	}
	raw := json.RawMessage(`{"count":5}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for count=5 with min=5 (boundary)")
	}
	raw = json.RawMessage(`{"count":4}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for count=4 with min=5")
	}
	raw = json.RawMessage(`{"count":999}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for count=999 with min=5 (no max)")
	}
}

func TestMatchArguments_MaxOnly(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "count", Max: float64Ptr(10)},
	}
	raw := json.RawMessage(`{"count":-100}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for count=-100 with max=10 (no min)")
	}
	raw = json.RawMessage(`{"count":10}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for count=10 with max=10 (boundary)")
	}
	raw = json.RawMessage(`{"count":11}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for count=11 with max=10")
	}
}

func TestMatchArguments_OneOfOnNonString(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "val", OneOf: []string{"1", "2"}},
	}
	raw := json.RawMessage(`{"val":1}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match: OneOf on numeric field")
	}
}

func TestMatchArguments_MaxLengthZero(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "s", MaxLength: intPtr(0)},
	}
	raw := json.RawMessage(`{"s":""}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match for empty string with max_length=0")
	}
	raw = json.RawMessage(`{"s":"a"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match for len=1 with max_length=0")
	}
}

func TestMatchArguments_MaxLengthUnicode(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "s", MaxLength: intPtr(3)},
	}
	// 3 runes, each multi-byte in UTF-8
	raw := json.RawMessage(`{"s":"日本語"}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match: 3 unicode runes within max_length=3")
	}
	raw = json.RawMessage(`{"s":"日本語字"}`)
	if matchArguments(preds, raw) {
		t.Error("expected no match: 4 unicode runes exceeds max_length=3")
	}
}

func TestMatchArguments_RequiredWithNonStringValue(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "count", Required: true},
	}
	raw := json.RawMessage(`{"count":0}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match: required numeric field present (even if zero)")
	}
	raw = json.RawMessage(`{"count":null}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match: required field present with null value")
	}
}

func TestMatchArguments_NilPredicatesNilArgs(t *testing.T) {
	// No predicates means vacuously true, but nil raw returns false
	if matchArguments(nil, nil) {
		t.Error("expected no match: nil predicates with nil raw should return false")
	}
}

func TestMatchArguments_NestedJSONField(t *testing.T) {
	// matchArguments only does top-level field lookup
	preds := []domain.ArgumentPredicate{
		{Field: "nested", Required: true},
	}
	raw := json.RawMessage(`{"nested":{"inner":"value"}}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match: nested object counts as present")
	}
}

func TestMatchArguments_OneOfEmptyString(t *testing.T) {
	preds := []domain.ArgumentPredicate{
		{Field: "mode", OneOf: []string{"", "read"}},
	}
	raw := json.RawMessage(`{"mode":""}`)
	if !matchArguments(preds, raw) {
		t.Error("expected match: empty string is in OneOf list")
	}
}

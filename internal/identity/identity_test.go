package identity_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
)

func TestCanonicalizeJSONSortsKeysAndWhitespace(t *testing.T) {
	in := []byte(`{"b":2,"a":1}`)
	got, err := identity.CanonicalizeJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":1,"b":2}`
	if string(got) != want {
		t.Fatalf("canonical mismatch: got %s want %s", got, want)
	}
}

func TestArgsHashStable(t *testing.T) {
	a, _ := identity.ComputeArgsHash([]byte(`{"x":1,"y":2}`))
	b, _ := identity.ComputeArgsHash([]byte(`{"y":2,"x":1}`))
	if a != b {
		t.Fatalf("hashes should match for equivalent objects")
	}
}

func TestStepIDDeterministicAndDistinct(t *testing.T) {
	execID := uuid.MustParse("018ff0a0-0000-7000-8000-000000000001")
	a := identity.ComputeStepID(execID, domain.StepKindTool, "read", "hash1", 0)
	b := identity.ComputeStepID(execID, domain.StepKindTool, "read", "hash1", 0)
	c := identity.ComputeStepID(execID, domain.StepKindTool, "read", "hash1", 1)
	if a != b {
		t.Fatal("same inputs must produce same id")
	}
	if a == c {
		t.Fatal("occurrence must change id")
	}
}

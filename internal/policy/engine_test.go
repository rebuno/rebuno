package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rebuno/kernel/internal/domain"
)

func TestArgPredicateRegex(t *testing.T) {
	bundle := `
rules:
  - id: allow-prod
    priority: 1
    when:
      arguments:
        env:
          regex: "^prod-.*"
    then:
      decision: allow
default_action: deny
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		env  string
		want string
	}{
		{"matching prod", "prod-123", domain.DecisionAllow},
		{"non-matching staging", "staging-123", domain.DecisionDeny},
		{"no prefix", "prod", domain.DecisionDeny},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"env": tc.env})
			res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Args: args})
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, res.Decision)
			}
		})
	}
}

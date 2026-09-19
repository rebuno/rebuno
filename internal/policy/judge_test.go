package policy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

const judgeBundle = `rules:
  - id: shell
    when:
      target: bash
    then:
      decision: judge
      judge:
        threshold: 0.7
        fallback: deny
`

func TestJudgeRuleDecision(t *testing.T) {
	cases := []struct {
		name   string
		status int
		probs  map[string]float64
		want   string
	}{
		{"confident answer is taken", http.StatusOK, map[string]float64{"allow": 0.91, "deny": 0.04, "require_approval": 0.05}, domain.DecisionAllow},
		{"split answer takes fallback", http.StatusOK, map[string]float64{"allow": 0.5, "deny": 0.2, "require_approval": 0.3}, domain.DecisionDeny},
		{"api error takes fallback", 529, nil, domain.DecisionDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Errorf("authorization header %q", r.Header.Get("Authorization"))
				}
				var req struct {
					State struct {
						ToolCall struct {
							Arguments map[string]string `json:"arguments"`
						} `json:"tool_call"`
					} `json:"state"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.State.ToolCall.Arguments["command"] != "ls" {
					t.Errorf("state.tool_call.arguments = %v (%v)", req.State.ToolCall.Arguments, err)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model":   "jev-latest",
					"answers": map[string]any{"decision": map[string]any{"type": "choice", "probabilities": tc.probs}},
				})
			}))
			defer srv.Close()

			engine, err := NewRuleEngineFromBundle(judgeBundle)
			if err != nil {
				t.Fatal(err)
			}
			engine.Judge = &Judge{url: srv.URL, apiKey: "key", client: srv.Client()}
			res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Target: "bash", Args: json.RawMessage(`{"command":"ls"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != tc.want || res.RuleID != "shell" {
				t.Fatalf("got %s (%s, %s), want %s", res.Decision, res.RuleID, res.Reason, tc.want)
			}
		})
	}
}

func TestJudgeRuleWithoutAPIKeyDenies(t *testing.T) {
	bundle := strings.Replace(judgeBundle, "fallback: deny", "fallback: allow", 1)
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	engine.Judge = NewJudge("")
	res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Target: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionDeny {
		t.Fatalf("got %s, want deny", res.Decision)
	}
}

func TestJudgeConfigIsValidatedAtLoad(t *testing.T) {
	cases := []struct {
		name     string
		decision string
		judge    string
		wantErr  string
	}{
		{"judge without decision judge", "allow", "threshold: 0.7", "judge is set"},
		{"unknown fallback", "judge", "fallback: allow_all", "unknown fallback"},
		{"threshold above one", "judge", "threshold: 70", "threshold 70"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := `rules:
  - id: r
    when:
      target: bash
    then:
      decision: ` + tc.decision + `
      judge:
        ` + tc.judge + `
`
			_, err := NewRuleEngineFromBundle(bundle)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got error %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

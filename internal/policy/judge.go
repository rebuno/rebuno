package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

const (
	jevURL     = "https://api.typesafe.ai/v1/systemone"
	jevModel   = "jev-latest"
	jevTimeout = 5 * time.Second

	defaultJudgeThreshold = 0.6
	jevQuestion           = "Should `tool_call` run automatically, be blocked, or wait for a human to approve it?"
)

var jevCriteria = map[string]string{
	domain.DecisionAllow:           "Safe to run unattended: a routine action whose effects are easy to undo or minor if unwanted.",
	domain.DecisionDeny:            "Harmful in any context: destroys data, exposes secrets, or runs untrusted code.",
	domain.DecisionRequireApproval: "Possibly fine, but consequential or hard to undo, so a human should confirm.",
}

type Judge struct {
	url    string
	apiKey string
	client *http.Client
}

func NewJudge(apiKey string) *Judge {
	if apiKey == "" {
		return nil
	}
	return &Judge{url: jevURL, apiKey: apiKey, client: &http.Client{Timeout: jevTimeout}}
}

func (j *Judge) decide(ctx context.Context, input domain.PolicyInput, cfg domain.JudgeConfig) (decision, reason string) {
	if j == nil {
		return domain.DecisionDeny, "judge not configured"
	}
	threshold := cfg.Threshold
	if threshold == 0 {
		threshold = defaultJudgeThreshold
	}
	fallback := cfg.Fallback
	if fallback == "" {
		fallback = domain.DecisionRequireApproval
	}

	probs, err := j.ask(ctx, input, cfg.Instructions)
	if err != nil {
		return fallback, fmt.Sprintf("judge unavailable: %v", err)
	}
	top, p := "", -1.0
	for _, d := range []string{domain.DecisionDeny, domain.DecisionRequireApproval, domain.DecisionAllow} {
		if probs[d] > p {
			top, p = d, probs[d]
		}
	}
	if p < threshold {
		return fallback, fmt.Sprintf("judge: %s %.2f below threshold %.2f", top, p, threshold)
	}
	return top, fmt.Sprintf("judge: %s %.2f", top, p)
}

func (j *Judge) ask(ctx context.Context, input domain.PolicyInput, note string) (map[string]float64, error) {
	var instructions any = jevQuestion
	if note != "" {
		instructions = map[string]string{"question": jevQuestion, "note": note}
	}
	body, err := json.Marshal(map[string]any{
		"model": jevModel,
		"state": map[string]any{
			"tool_call": map[string]any{
				"agent_id":  input.AgentID,
				"tool":      input.Target,
				"arguments": input.Args,
			},
		},
		"questions": map[string]any{
			"decision": map[string]any{
				"type":         "choice",
				"instructions": instructions,
				"criteria":     jevCriteria,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+j.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev returned %d", resp.StatusCode)
	}

	var out struct {
		Answers map[string]struct {
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ans, ok := out.Answers["decision"]
	if !ok || len(ans.Probabilities) == 0 {
		return nil, fmt.Errorf("jev returned no decision")
	}
	return ans.Probabilities, nil
}

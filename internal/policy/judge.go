package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rebuno/rebuno/internal/domain"
)

const (
	defaultJudgeTimeout   = 5 * time.Second
	defaultJudgeThreshold = 0.6
	judgeQuestion         = "Should `tool_call` run automatically, be blocked, or wait for a human to approve it?"
)

var judgeCriteria = map[string]string{
	domain.DecisionAllow:           "Safe to run unattended: a routine action whose effects are easy to undo or minor if unwanted.",
	domain.DecisionDeny:            "Harmful in any context: destroys data, exposes secrets, or runs untrusted code.",
	domain.DecisionRequireApproval: "Possibly fine, but consequential or hard to undo, so a human should confirm.",
}

type judgeProvider struct {
	URL           string            `yaml:"url"`
	Headers       map[string]string `yaml:"headers"`
	Body          string            `yaml:"body"`
	Probabilities string            `yaml:"probabilities"`
	Timeout       time.Duration     `yaml:"timeout"`
}

type judgeRequest struct {
	ToolCall     map[string]any
	Question     string
	Criteria     map[string]string
	Instructions string
}

type Judge struct {
	url     string
	headers map[string]string
	body    *template.Template
	path    []string
	client  *http.Client
}

func LoadJudge(path string) (*Judge, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("judge config: %w", err)
	}
	var p judgeProvider
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("judge config %s: %w", path, err)
	}
	j, err := newJudge(p)
	if err != nil {
		return nil, fmt.Errorf("judge config %s: %w", path, err)
	}
	return j, nil
}

func newJudge(p judgeProvider) (*Judge, error) {
	if p.URL == "" || p.Body == "" || p.Probabilities == "" {
		return nil, fmt.Errorf("url, body, and probabilities are required")
	}
	body, err := template.New("body").Funcs(template.FuncMap{"json": toJSON}).Parse(p.Body)
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	headers := make(map[string]string, len(p.Headers))
	for k, v := range p.Headers {
		headers[k] = os.ExpandEnv(v)
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = defaultJudgeTimeout
	}
	return &Judge{
		url:     os.ExpandEnv(p.URL),
		headers: headers,
		body:    body,
		path:    strings.Split(p.Probabilities, "."),
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
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

func (j *Judge) ask(ctx context.Context, input domain.PolicyInput, instructions string) (map[string]float64, error) {
	var buf bytes.Buffer
	err := j.body.Execute(&buf, judgeRequest{
		ToolCall: map[string]any{
			"agent_id":  input.AgentID,
			"tool":      input.Target,
			"arguments": input.Args,
		},
		Question:     judgeQuestion,
		Criteria:     judgeCriteria,
		Instructions: instructions,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range j.headers {
		req.Header.Set(k, v)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("judge returned %d", resp.StatusCode)
	}

	var out any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	for _, key := range j.path {
		switch v := out.(type) {
		case map[string]any:
			out = v[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(v) {
				return nil, fmt.Errorf("judge response has no %s", strings.Join(j.path, "."))
			}
			out = v[i]
		default:
			return nil, fmt.Errorf("judge response has no %s", strings.Join(j.path, "."))
		}
	}
	obj, _ := out.(map[string]any)
	probs := make(map[string]float64, len(obj))
	for k, v := range obj {
		if f, ok := v.(float64); ok {
			probs[k] = f
		}
	}
	if len(probs) == 0 {
		return nil, fmt.Errorf("judge response has no probabilities at %s", strings.Join(j.path, "."))
	}
	return probs, nil
}

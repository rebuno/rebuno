package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
)

const (
	maxLabelWidth  = 56
	defaultTimeout = 30 * time.Second
)

type policyTestOpts struct {
	casesPath string
	agentID   string
	target    string
	args      string
	kind      string
	execution string
	kernelURL string
}

func policyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Work with policy bundles",
	}
	cmd.AddCommand(policyTestCmd())
	return cmd
}

func policyTestCmd() *cobra.Command {
	var opts policyTestOpts
	cmd := &cobra.Command{
		Use:   "test <bundle.yaml>",
		Short: "Evaluate a policy bundle against test cases",
		Long: "Evaluate a policy bundle against test cases and exit non-zero if any\n" +
			"expectation goes unmet. Cases come from --cases, or from the\n" +
			"<bundle>.policytest.yaml beside the bundle.\n\n" +
			"With --target the bundle is probed with a single input instead, printing\n" +
			"the decision and the rule behind it. With --execution the cases come from\n" +
			"a past execution's recorded steps, which a running kernel serves; set\n" +
			"REBUNO_API_KEY when that kernel enforces auth.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyTest(cmd.Context(), args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.casesPath, "cases", "", "Path to a case file (default: the .policytest.yaml beside the bundle)")
	f.StringVar(&opts.agentID, "agent-id", "", "Agent id for cases that do not name one")
	f.StringVar(&opts.target, "target", "", "Probe the bundle with this single target instead of running cases")
	f.StringVar(&opts.args, "args", "", "JSON arguments for --target")
	f.StringVar(&opts.kind, "kind", "", "Step kind for --target (tool_call, llm_call, local; default tool_call)")
	f.StringVar(&opts.execution, "execution", "", "Replay this execution's recorded steps, with --agent-id")
	f.StringVar(&opts.kernelURL, "url", kernelURL(), "Kernel base URL ($REBUNO_URL)")
	return cmd
}

func runPolicyTest(ctx context.Context, bundlePath string, opts policyTestOpts) error {
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	report, err := policyReport(ctx, string(bundle), bundlePath, opts)
	if err != nil {
		return err
	}
	if opts.target != "" {
		printPolicyProbe(report.Results[0])
		return nil
	}
	printPolicyReport(report)
	if report.Failed > 0 {
		os.Exit(1)
	}
	return nil
}

func policyReport(ctx context.Context, bundle, bundlePath string, opts policyTestOpts) (policy.Report, error) {
	if opts.execution != "" {
		return replayPolicy(ctx, bundle, opts)
	}
	engine, err := policy.NewRuleEngineFromBundle(bundle)
	if err != nil {
		return policy.Report{}, fmt.Errorf("%s: %w", bundlePath, err)
	}
	cases, err := policyTestCases(bundlePath, opts)
	if err != nil {
		return policy.Report{}, err
	}
	if err := policy.NormalizeCases(cases, opts.agentID); err != nil {
		return policy.Report{}, err
	}
	return policy.Run(ctx, engine, cases)
}

func replayPolicy(ctx context.Context, bundle string, opts policyTestOpts) (policy.Report, error) {
	if opts.agentID == "" {
		return policy.Report{}, fmt.Errorf("--execution needs --agent-id")
	}
	if opts.target != "" || opts.casesPath != "" {
		return policy.Report{}, fmt.Errorf("--execution cannot be combined with --target or --cases")
	}
	execID, err := uuid.Parse(opts.execution)
	if err != nil {
		return policy.Report{}, fmt.Errorf("--execution: %q is not a full execution id", opts.execution)
	}
	body, err := json.Marshal(kernel.PolicyTestRequest{Bundle: bundle, ExecutionID: &execID})
	if err != nil {
		return policy.Report{}, err
	}

	endpoint := strings.TrimSuffix(opts.kernelURL, "/") + "/v0/policies/" + url.PathEscape(opts.agentID) + "/test"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return policy.Report{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("REBUNO_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: defaultTimeout}).Do(req)
	if err != nil {
		return policy.Report{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var apiErr domain.APIError
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Message != "" {
			return policy.Report{}, fmt.Errorf("%s: %s", resp.Status, apiErr.Message)
		}
		return policy.Report{}, fmt.Errorf("%s", resp.Status)
	}
	var report policy.Report
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return policy.Report{}, err
	}
	return report, nil
}

func kernelURL() string {
	if v := os.Getenv("REBUNO_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func policyTestCases(bundlePath string, opts policyTestOpts) ([]policy.Case, error) {
	if opts.target != "" {
		probe := policy.Case{Target: opts.target, Kind: domain.StepKind(opts.kind)}
		if opts.args != "" {
			if err := json.Unmarshal([]byte(opts.args), &probe.Args); err != nil {
				return nil, fmt.Errorf("--args: %w", err)
			}
		}
		return []policy.Case{probe}, nil
	}

	path := opts.casesPath
	if path == "" {
		path = casesPathFor(bundlePath)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("no cases: expected %s, or pass --cases or --target", path)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cases, err := policy.LoadCases(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	return cases, nil
}

func casesPathFor(bundlePath string) string {
	return strings.TrimSuffix(bundlePath, filepath.Ext(bundlePath)) + ".policytest.yaml"
}

func printPolicyProbe(res policy.Result) {
	line := fmt.Sprintf("  %s  %s", res.Decision, res.RuleID)
	if res.Reason != "" {
		line += "  " + res.Reason
	}
	fmt.Println(line)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func policyDetail(res policy.Result) string {
	if res.Pass {
		return res.Decision
	}
	return res.Failure
}

func printPolicyReport(report policy.Report) {
	labels, details := 0, 0
	for _, res := range report.Results {
		labels = max(labels, len(truncate(res.Label(), maxLabelWidth)))
		details = max(details, len(policyDetail(res)))
	}

	for _, res := range report.Results {
		status := "PASS"
		if !res.Pass {
			status = "FAIL"
		}
		fmt.Printf("  %s  %-*s  %-*s  %s\n", status, labels, truncate(res.Label(), maxLabelWidth), details, policyDetail(res), res.RuleID)
	}
	if len(report.UnexercisedRules) > 0 {
		fmt.Printf("\n  rules never exercised: %s\n", strings.Join(report.UnexercisedRules, ", "))
	}
	if report.Failed > 0 {
		fmt.Printf("\n  %d of %d cases failed\n", report.Failed, len(report.Results))
		return
	}
	fmt.Printf("\n  %d cases passed\n", len(report.Results))
}

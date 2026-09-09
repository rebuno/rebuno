package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func TestKernelRequiresPrincipalAndExecutionOwner(t *testing.T) {
	k, ctx := setup(t)
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseOf(t, k, exec.ID)
	owner := auth.WithAgent(context.Background(), "agent-1")
	req := kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`), Lease: lease}
	dec, err := k.SubmitStep(owner, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CompleteStep(owner, dec.StepID, kernel.CompleteStepRequest{Lease: lease, Result: json.RawMessage(`{"private":true}`)}); err != nil {
		t.Fatal(err)
	}
	for _, principal := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"missing", context.Background(), domain.ErrUnauthorized},
		{"foreign", auth.WithAgent(context.Background(), "other"), domain.ErrForbidden},
	} {
		t.Run(principal.name, func(t *testing.T) {
			for name, call := range map[string]func() error{
				"execution":     func() error { _, err := k.GetExecution(principal.ctx, exec.ID); return err },
				"steps":         func() error { _, err := k.ListSteps(principal.ctx, exec.ID); return err },
				"step":          func() error { _, err := k.GetStep(principal.ctx, dec.StepID); return err },
				"submit replay": func() error { _, err := k.SubmitStep(principal.ctx, exec.ID, req); return err },
				"complete replay": func() error {
					_, err := k.CompleteStep(principal.ctx, dec.StepID, kernel.CompleteStepRequest{Lease: lease})
					return err
				},
				"fail replay": func() error {
					_, err := k.FailStep(principal.ctx, dec.StepID, kernel.FailStepRequest{Lease: lease})
					return err
				},
				"heartbeat":          func() error { return k.Heartbeat(principal.ctx, exec.ID, lease) },
				"complete execution": func() error { return k.CompleteExecution(principal.ctx, exec.ID, lease, nil) },
				"fail execution":     func() error { return k.FailExecution(principal.ctx, exec.ID, lease, "failed") },
			} {
				t.Run(name, func(t *testing.T) {
					if err := call(); !errors.Is(err, principal.want) {
						t.Fatalf("got %v, want %v", err, principal.want)
					}
				})
			}
		})
	}
	if _, err := k.GetStep(owner, dec.StepID); err != nil {
		t.Fatal(err)
	}
}

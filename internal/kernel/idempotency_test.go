package kernel_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func TestIdempotencyKeyCreatesOneExecution(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, replica, ctx := sessionKernels(t, backend)
			replicas := []*kernel.Kernel{k, replica(), replica()}
			ids := make([]uuid.UUID, 12)
			var wg sync.WaitGroup
			for i := range ids {
				wg.Go(func() {
					exec, err := replicas[i%len(replicas)].CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{IdempotencyKey: "step-1"})
					if err != nil {
						t.Error(err)
						return
					}
					ids[i] = exec.ID
				})
			}
			wg.Wait()
			for _, id := range ids {
				if id != ids[0] {
					t.Fatalf("ids differ: %v", ids)
				}
			}
			page, err := k.ListExecutions(ctx, domain.ExecutionFilter{AgentID: "agent-1"})
			if err != nil || len(page.Executions) != 1 || page.Executions[0].IdempotencyKey != "step-1" {
				t.Fatalf("executions: %+v, %v", page, err)
			}

			other, err := k.CreateExecution(ctx, "agent-2", json.RawMessage(`{}`), kernel.CreateExecutionOptions{IdempotencyKey: "step-1"})
			if err != nil || other.ID == ids[0] {
				t.Fatalf("other agent: %+v, %v", other, err)
			}
		})
	}
}

func TestIdempotencyKeyReplaysContinuation(t *testing.T) {
	k, _, ctx := sessionKernels(t, "memory")
	parent := createInSession(t, k, ctx, "agent-1", "first")
	if err := k.Deps().Executions.UpdateExecutionStatus(ctx, parent.ID, domain.ExecutionCompleted, []byte(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	opts := kernel.CreateExecutionOptions{Session: "second", ParentExecutionID: &parent.ID, IdempotencyKey: "continue"}
	first, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	again, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), opts)
	if err != nil || again.ID != first.ID {
		t.Fatalf("replay: %+v, %v", again, err)
	}
}

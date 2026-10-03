package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func onEachBackend(t *testing.T, test func(t *testing.T, k *kernel.Kernel, ctx context.Context)) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			test(t, k, ctx)
		})
	}
}

func registerWorkspace(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, req kernel.RegisterResourceRequest) kernel.ResourceView {
	t.Helper()
	req.Key, req.DriverID, req.Lease = "workspace", "test.v1", leaseOf(t, k, id)
	view, err := k.RegisterResource(ctx, id, req)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func publish(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, generation int64, ref string) {
	t.Helper()
	if err := k.PublishCheckpoints(ctx, id, kernel.PublishCheckpointsRequest{
		Captures: []kernel.ResourceCapture{{Key: "workspace", Generation: generation, CheckpointRef: ref}},
		Lease:    leaseOf(t, k, id),
	}); err != nil {
		t.Fatal(err)
	}
}

func submitTool(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, target string, resources []string) domain.StepDecision {
	t.Helper()
	dec, err := k.SubmitStep(ctx, id, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: target, Args: json.RawMessage(`{}`), Resources: resources, Lease: leaseOf(t, k, id),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

func completeTool(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, dec domain.StepDecision, ref string) {
	t.Helper()
	req := kernel.CompleteStepRequest{Result: json.RawMessage(`"ok"`), Lease: leaseOf(t, k, id)}
	for _, r := range dec.Resources {
		if r.Due {
			req.Captures = append(req.Captures, kernel.ResourceCapture{Key: r.Key, Generation: r.Generation, CheckpointRef: ref})
		}
	}
	if _, err := k.CompleteExecutionStep(ctx, id, dec.StepID, req); err != nil {
		t.Fatal(err)
	}
}

func forkPointCovered(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, seq int64) bool {
	t.Helper()
	points, err := k.ForkPoints(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if seq < 1 || seq > points.LatestSeq {
		t.Fatalf("boundary %d outside execution history ending at %d", seq, points.LatestSeq)
	}
	for key, registered := range points.Resources {
		if registered > seq {
			continue
		}
		if !slices.ContainsFunc(points.Checkpoints, func(c domain.ResourceCheckpoint) bool {
			return c.Key == key && c.Covers(seq)
		}) {
			return false
		}
	}
	return true
}

func TestSessionContinuesResourceBindingsAndForksRestoreSeparately(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		first := createInSession(t, k, ctx, "agent-1", "main")
		onCompletion := false
		policy := kernel.RegisterResourceRequest{EverySteps: 3, OnCompletion: &onCompletion}
		registerWorkspace(t, k, ctx, first.ID, policy)
		binding := json.RawMessage(`{"sandbox_id":"original"}`)
		if err := k.BindResource(ctx, first.ID, "workspace", kernel.BindResourceRequest{
			Binding: binding, Lease: leaseOf(t, k, first.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := k.RegisterResource(ctx, first.ID, kernel.RegisterResourceRequest{
			Key: "database", DriverID: "database.v1", Lease: leaseOf(t, k, first.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if err := k.BindResource(ctx, first.ID, "database", kernel.BindResourceRequest{
			Binding: json.RawMessage(`{"database":"original"}`), Lease: leaseOf(t, k, first.ID),
		}); err != nil {
			t.Fatal(err)
		}
		publish(t, k, ctx, first.ID, 0, "first-baseline")
		completeTool(t, k, ctx, first.ID, submitTool(t, k, ctx, first.ID, "write", []string{"workspace"}), "")
		second := createInSession(t, k, ctx, "agent-1", "main")
		if err := k.CompleteExecution(ctx, first.ID, leaseOf(t, k, first.ID), json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}

		view := registerWorkspace(t, k, ctx, second.ID, kernel.RegisterResourceRequest{})
		if view.CheckpointRef != "" || view.Covered {
			t.Fatalf("continued workspace: %+v", view)
		}
		requireJSON(t, view.Binding, string(binding))
		if view.Count != 0 || view.Generation != 0 || view.EverySteps != 3 || view.OnCompletion {
			t.Fatalf("continued policy and progress: %+v", view)
		}
		database, err := k.RegisterResource(ctx, second.ID, kernel.RegisterResourceRequest{
			Key: "database", DriverID: "database.v1", Lease: leaseOf(t, k, second.ID),
		})
		if err != nil {
			t.Fatal(err)
		}
		requireJSON(t, database.Binding, `{"database":"original"}`)
		if err := k.PublishCheckpoints(ctx, second.ID, kernel.PublishCheckpointsRequest{
			Captures: []kernel.ResourceCapture{
				{Key: "workspace", CheckpointRef: "second-workspace"},
				{Key: "database", CheckpointRef: "second-database"},
			},
			Lease: leaseOf(t, k, second.ID),
		}); err != nil {
			t.Fatal(err)
		}
		fork, err := k.ForkExecution(ctx, second.ID, kernel.ForkRequest{AtSeq: latestSeq(t, k, ctx, second.ID), Session: "branch"})
		if err != nil {
			t.Fatal(err)
		}
		view = registerWorkspace(t, k, ctx, fork.ID, policy)
		if view.Binding != nil || view.CheckpointRef != "second-workspace" || !view.Covered {
			t.Fatalf("forked workspace: %+v", view)
		}
		forkBinding := json.RawMessage(`{"sandbox_id":"fork"}`)
		if err := k.BindResource(ctx, fork.ID, "workspace", kernel.BindResourceRequest{
			Binding: forkBinding, Lease: leaseOf(t, k, fork.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if err := k.CompleteExecution(ctx, fork.ID, leaseOf(t, k, fork.ID), json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}
		followup := createInSession(t, k, ctx, "agent-1", "branch")
		view = registerWorkspace(t, k, ctx, followup.ID, policy)
		if view.CheckpointRef != "" || view.Covered {
			t.Fatalf("fork session continuation: %+v", view)
		}
		requireJSON(t, view.Binding, string(forkBinding))
		view = registerWorkspace(t, k, ctx, second.ID, policy)
		requireJSON(t, view.Binding, string(binding))
	})
}

func TestSessionResourceBindingsStayWithinTheirAgentAndSession(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		source := createInSession(t, k, ctx, "agent-1", "main")
		registerWorkspace(t, k, ctx, source.ID, kernel.RegisterResourceRequest{})
		if err := k.BindResource(ctx, source.ID, "workspace", kernel.BindResourceRequest{
			Binding: json.RawMessage(`{"sandbox_id":"source"}`), Lease: leaseOf(t, k, source.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if err := k.CompleteExecution(ctx, source.ID, leaseOf(t, k, source.ID), json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}
		otherAgent := createInSession(t, k, ctx, "agent-2", "main")
		otherSession := createInSession(t, k, ctx, "agent-1", "other")
		branched, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{
			ParentExecutionID: &source.ID, Session: "branched",
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, exec := range []domain.Execution{otherAgent, otherSession, branched} {
			if view := registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{}); view.Binding != nil {
				t.Fatalf("independent execution inherited a binding: %+v", view)
			}
		}
	})
}

func TestForkRestoresEachResourceFromItsNewestCheckpointByTheForkPoint(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			source := createInSession(t, k, ctx, "agent-1", "main")
			registerWorkspace(t, k, ctx, source.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 2})
			if err := k.BindResource(ctx, source.ID, "workspace", kernel.BindResourceRequest{
				Binding: json.RawMessage(`{"sandbox_id":"sbx-1"}`), Lease: leaseOf(t, k, source.ID),
			}); err != nil {
				t.Fatal(err)
			}
			publish(t, k, ctx, source.ID, 0, "s0")
			baseline := latestSeq(t, k, ctx, source.ID)

			first := submitTool(t, k, ctx, source.ID, "write", []string{"workspace"})
			if len(first.Resources) != 1 || first.Resources[0].Generation != 1 || first.Resources[0].Due {
				t.Fatalf("first affecting step: %+v", first.Resources)
			}
			completeTool(t, k, ctx, source.ID, first, "")
			uncaptured := latestSeq(t, k, ctx, source.ID)

			second := submitTool(t, k, ctx, source.ID, "write", []string{"workspace"})
			if len(second.Resources) != 1 || !second.Resources[0].Due {
				t.Fatalf("second affecting step is due a capture: %+v", second.Resources)
			}
			completeTool(t, k, ctx, source.ID, second, "s2")

			for _, resources := range [][]string{nil, {}} {
				read := submitTool(t, k, ctx, source.ID, "read", resources)
				if len(read.Resources) != 0 {
					t.Fatalf("a step declaring no resources: %+v", read.Resources)
				}
				completeTool(t, k, ctx, source.ID, read, "")
			}
			afterRead := latestSeq(t, k, ctx, source.ID)

			uncovered, err := k.ForkExecution(ctx, source.ID, kernel.ForkRequest{AtSeq: uncaptured})
			if err != nil {
				t.Fatal(err)
			}
			want := domain.ResourceSelection{
				CheckpointRef: "s0", CheckpointSeq: baseline - 1,
			}
			if r := uncovered.Restoration; r == nil || r["workspace"] != want {
				t.Fatalf("restoration between checkpoints: %+v", r)
			}
			view := registerWorkspace(t, k, ctx, uncovered.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 2})
			if view.CheckpointRef != "s0" || view.Binding != nil || view.Count != 1 || view.Generation != 2 || view.Covered {
				t.Fatalf("resource forked between checkpoints: %+v", view)
			}
			boundary := latestSeq(t, k, ctx, uncovered.ID)
			publish(t, k, ctx, uncovered.ID, 2, "restored")
			if at := forkPointCovered(t, k, ctx, uncovered.ID, boundary-1); at {
				t.Fatalf("a baseline covers the boundary before it: %+v", at)
			}
			if at := forkPointCovered(t, k, ctx, uncovered.ID, latestSeq(t, k, ctx, uncovered.ID)); !at {
				t.Fatalf("boundary after the fork's baseline: %+v", at)
			}

			fork, err := k.ForkExecution(ctx, source.ID, kernel.ForkRequest{AtSeq: afterRead})
			if err != nil {
				t.Fatal(err)
			}
			if r := fork.Restoration; r == nil || !r["workspace"].Covered || r["workspace"].CheckpointRef != "s2" {
				t.Fatalf("restoration at a covered boundary: %+v", r)
			}
			if stored, err := k.GetExecution(ctx, fork.ID); err != nil || stored.Restoration != nil {
				t.Fatalf("stored fork: %+v, %v", stored, err)
			}
			view = registerWorkspace(t, k, ctx, fork.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 2})
			if view.CheckpointRef != "s2" || view.Binding != nil || view.Count != 2 || view.Generation != 2 || !view.Covered {
				t.Fatalf("forked resource: %+v", view)
			}
			if dec := submitTool(t, k, ctx, fork.ID, "write", []string{"workspace"}); dec.Decision != "replay" || len(dec.Resources) != 0 {
				t.Fatalf("copied step: %+v", dec)
			}
			if at := forkPointCovered(t, k, ctx, fork.ID, latestSeq(t, k, ctx, fork.ID)); !at {
				t.Fatalf("fork boundary in the fork: %+v", at)
			}
		})
	}
}

func TestCaptureThatNoLongerDescribesItsResourceIsRefused(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true})

		stale := submitTool(t, k, ctx, exec.ID, "write", []string{"workspace"})
		if _, err := k.CompleteExecutionStep(ctx, exec.ID, stale.StepID, kernel.CompleteStepRequest{
			Result:   json.RawMessage(`"ok"`),
			Captures: []kernel.ResourceCapture{{Key: "workspace", Generation: 0, CheckpointRef: "old"}},
			Lease:    leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}

		a := submitTool(t, k, ctx, exec.ID, "a", []string{"workspace"})
		b := submitTool(t, k, ctx, exec.ID, "b", []string{"workspace"})
		if _, err := k.CompleteExecutionStep(ctx, exec.ID, a.StepID, kernel.CompleteStepRequest{
			Result:   json.RawMessage(`"ok"`),
			Captures: []kernel.ResourceCapture{{Key: "workspace", Generation: b.Resources[0].Generation, CheckpointRef: "racing"}},
			Lease:    leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}

		events, err := k.GetEvents(ctx, exec.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var refusals []string
		for _, e := range events {
			if e.Type == domain.EventResourceCheckpointFailed {
				var body struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(e.Payload, &body)
				refusals = append(refusals, body.Error)
			}
		}
		if !slices.Equal(refusals, []string{"stale_generation", "writers_active"}) {
			t.Fatalf("refusals: %v", refusals)
		}
		if step, _ := k.GetExecutionStep(ctx, exec.ID, a.StepID); step.Status != domain.StepSucceeded {
			t.Fatalf("outcome with a refused capture: %s", step.Status)
		}
		if at := forkPointCovered(t, k, ctx, exec.ID, latestSeq(t, k, ctx, exec.ID)); at {
			t.Fatalf("no capture was accepted: %+v", at)
		}
	})
}

func TestLLMStepEndsCoverageOnlyWithoutCoverageReuse(t *testing.T) {
	for _, reuse := range []bool{true, false} {
		k, ctx := setup(t)
		exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: reuse})
		publish(t, k, ctx, exec.ID, 0, "s0")

		dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
			Kind: domain.StepKindLLM, Target: "model", Args: json.RawMessage(`{}`), Lease: leaseOf(t, k, exec.ID),
		})
		if err != nil {
			t.Fatal(err)
		}
		at := forkPointCovered(t, k, ctx, exec.ID, latestSeq(t, k, ctx, exec.ID))
		if at != reuse || (len(dec.Resources) == 0) != reuse {
			t.Fatalf("coverage_reuse=%v: touched %+v, fork point %+v", reuse, dec.Resources, at)
		}
	}
}

func TestStepDeclaringAnUnregisteredResourceIsRejected(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	_, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "write", Args: json.RawMessage(`{}`), Resources: []string{"workspace"}, Lease: leaseOf(t, k, exec.ID),
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown resource: %v", err)
	}
}

func TestForkWithoutACheckpointUsesTheInitialEnvironment(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true})
		fork, err := k.ForkExecution(ctx, exec.ID, kernel.ForkRequest{AtSeq: latestSeq(t, k, ctx, exec.ID)})
		if err != nil {
			t.Fatal(err)
		}
		want := domain.ResourceSelection{}
		if r := fork.Restoration; r == nil || r["workspace"] != want {
			t.Fatalf("restoration without a checkpoint: %+v", r)
		}
		view := registerWorkspace(t, k, ctx, fork.ID, kernel.RegisterResourceRequest{CoverageReuse: true})
		if view.CheckpointRef != "" || view.Binding != nil || view.Generation != 1 || view.Count != 0 || view.Covered {
			t.Fatalf("resource initialized without a checkpoint: %+v", view)
		}
	})
}

func TestForkAtACapturedOutcomeLogsItsCheckpoint(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true})
		publish(t, k, ctx, exec.ID, 0, "s0")
		completeTool(t, k, ctx, exec.ID, submitTool(t, k, ctx, exec.ID, "write", []string{"workspace"}), "s1")
		outcome := latestSeq(t, k, ctx, exec.ID) - 1

		fork, err := k.ForkExecution(ctx, exec.ID, kernel.ForkRequest{AtSeq: outcome})
		if err != nil {
			t.Fatal(err)
		}
		events, err := k.GetEvents(ctx, fork.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var refs []string
		var generations map[string]int64
		for _, e := range events {
			var body struct {
				Ref         string           `json:"checkpoint_ref"`
				Generations map[string]int64 `json:"generations"`
			}
			_ = json.Unmarshal(e.Payload, &body)
			switch e.Type {
			case domain.EventResourceCheckpointed:
				refs = append(refs, body.Ref)
			case domain.EventStepExecuting:
				generations = body.Generations
			}
		}
		if !slices.Equal(refs, []string{"s0", "s1"}) || generations["workspace"] != 1 {
			t.Fatalf("fork log: checkpoints %v, generations %v", refs, generations)
		}
	})
}

func TestRepeatedRegistrationKeepsItsPolicyAndRefusesAnotherDriverOrCapability(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{EverySteps: 5})

	if again := registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{EverySteps: 1}); again.EverySteps != 5 {
		t.Fatalf("policy after a repeated registration: %+v", again)
	}
	for name, req := range map[string]kernel.RegisterResourceRequest{
		"driver":         {Key: "workspace", DriverID: "other.v1"},
		"coverage reuse": {Key: "workspace", DriverID: "test.v1", CoverageReuse: true},
	} {
		req.Lease = leaseOf(t, k, exec.ID)
		if _, err := k.RegisterResource(ctx, exec.ID, req); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("registration with another %s: %v", name, err)
		}
	}
}

func TestRerunOfAnOrphanedStepRefusesCapturesFromItsFirstAttempt(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true})

		first := submitTool(t, k, ctx, exec.ID, "write", []string{"workspace"})
		if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
			t.Fatal(err)
		}
		rerun := submitTool(t, k, ctx, exec.ID, "write", []string{"workspace"})
		if rerun.Decision != "proceed" || rerun.StepID != first.StepID || rerun.Resources[0].Generation != first.Resources[0].Generation+1 {
			t.Fatalf("rerun: %+v after %+v", rerun, first)
		}

		if _, err := k.CompleteExecutionStep(ctx, exec.ID, rerun.StepID, kernel.CompleteStepRequest{
			Result:   json.RawMessage(`"ok"`),
			Captures: []kernel.ResourceCapture{{Key: "workspace", Generation: first.Resources[0].Generation, CheckpointRef: "first-attempt"}},
			Lease:    leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if at := forkPointCovered(t, k, ctx, exec.ID, latestSeq(t, k, ctx, exec.ID)); at {
			t.Fatalf("first attempt's capture covers the rerun: %+v", at)
		}
		view := registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true})
		if view.Count != 1 {
			t.Fatalf("a rerun step counts once: %d", view.Count)
		}
	})
}

func TestForkKeepsTheNewestCheckpointAtTheSameCopiedBoundary(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true})
		publish(t, k, ctx, exec.ID, 0, "first")
		publish(t, k, ctx, exec.ID, 0, "latest")

		fork, err := k.ForkExecution(ctx, exec.ID, kernel.ForkRequest{AtSeq: latestSeq(t, k, ctx, exec.ID)})
		if err != nil {
			t.Fatal(err)
		}
		view := registerWorkspace(t, k, ctx, fork.ID, kernel.RegisterResourceRequest{CoverageReuse: true})
		if view.CheckpointRef != "latest" || !view.Covered {
			t.Fatalf("forked resource: %+v", view)
		}
		points, err := k.ForkPoints(ctx, fork.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(points.Checkpoints) != 1 || points.Checkpoints[0].Ref != "latest" {
			t.Fatalf("checkpoints at the copied boundary: %+v", points.Checkpoints)
		}
	})
}

func TestForkOfAnUncoveredForkKeepsItsBaselineAfterTheCopiedSteps(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		source, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		registerWorkspace(t, k, ctx, source.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 5})
		publish(t, k, ctx, source.ID, 0, "s0")
		completeTool(t, k, ctx, source.ID, submitTool(t, k, ctx, source.ID, "write", []string{"workspace"}), "")

		child, err := k.ForkExecution(ctx, source.ID, kernel.ForkRequest{AtSeq: latestSeq(t, k, ctx, source.ID)})
		if err != nil {
			t.Fatal(err)
		}
		view := registerWorkspace(t, k, ctx, child.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 5})
		publish(t, k, ctx, child.ID, view.Generation, "restored")

		grandchild, err := k.ForkExecution(ctx, child.ID, kernel.ForkRequest{AtSeq: latestSeq(t, k, ctx, child.ID)})
		if err != nil {
			t.Fatal(err)
		}
		view = registerWorkspace(t, k, ctx, grandchild.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 5})
		if !grandchild.Restoration["workspace"].Covered || view.CheckpointRef != "restored" || !view.Covered {
			t.Fatalf("fork at the child's baseline: %+v, resource %+v", grandchild.Restoration, view)
		}

		events, err := k.GetEvents(ctx, grandchild.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Type == domain.EventStepSucceeded {
				if at := forkPointCovered(t, k, ctx, grandchild.ID, e.EventSeq); at {
					t.Fatalf("copied outcome the child's restored state lacks: %+v", at)
				}
			}
		}
	})
}

func TestForkPointsWithoutResources(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		completeTool(t, k, ctx, exec.ID, submitTool(t, k, ctx, exec.ID, "read", nil), "")
		latest := latestSeq(t, k, ctx, exec.ID)
		points, err := k.ForkPoints(ctx, exec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if points.LatestSeq != latest || len(points.Resources) != 0 || len(points.Checkpoints) != 0 {
			t.Fatalf("fork points without resources: %+v", points)
		}
		fork, err := k.ForkExecution(ctx, exec.ID, kernel.ForkRequest{AtSeq: latest})
		if err != nil {
			t.Fatal(err)
		}
		if len(fork.Restoration) != 0 {
			t.Fatalf("resource selections without resources: %+v", fork.Restoration)
		}
	})
}

func TestForkSelectsIndependentResourceStatesAndExcludesLaterRegistrations(t *testing.T) {
	onEachBackend(t, func(t *testing.T, k *kernel.Kernel, ctx context.Context) {
		exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		registerWorkspace(t, k, ctx, exec.ID, kernel.RegisterResourceRequest{CoverageReuse: true, EverySteps: 5})
		publish(t, k, ctx, exec.ID, 0, "workspace-before-write")
		completeTool(t, k, ctx, exec.ID, submitTool(t, k, ctx, exec.ID, "write", []string{"workspace"}), "")
		if _, err := k.RegisterResource(ctx, exec.ID, kernel.RegisterResourceRequest{
			Key: "database", DriverID: "test.v1", CoverageReuse: true, Lease: leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if err := k.PublishCheckpoints(ctx, exec.ID, kernel.PublishCheckpointsRequest{
			Captures: []kernel.ResourceCapture{{Key: "database", CheckpointRef: "database-baseline"}},
			Lease:    leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}
		atSeq := latestSeq(t, k, ctx, exec.ID)
		if _, err := k.RegisterResource(ctx, exec.ID, kernel.RegisterResourceRequest{
			Key: "cache", DriverID: "test.v1", Lease: leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}
		fork, err := k.ForkExecution(ctx, exec.ID, kernel.ForkRequest{AtSeq: atSeq})
		if err != nil {
			t.Fatal(err)
		}
		workspace, database := fork.Restoration["workspace"], fork.Restoration["database"]
		if len(fork.Restoration) != 2 || workspace.CheckpointRef != "workspace-before-write" || workspace.Covered || database.CheckpointRef != "database-baseline" || !database.Covered || workspace.CheckpointSeq >= database.CheckpointSeq {
			t.Fatalf("independent resource selections: %+v", fork.Restoration)
		}
		if fork.ForkSeq != atSeq || forkPointCovered(t, k, ctx, exec.ID, atSeq) {
			t.Fatal("the uncovered fork must keep the requested boundary")
		}
	})
}

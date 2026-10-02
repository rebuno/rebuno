package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/store"
)

type RegisterResourceRequest struct {
	Key           string          `json:"key"`
	DriverID      string          `json:"driver_id"`
	Configuration json.RawMessage `json:"configuration,omitempty"`
	CoverageReuse bool            `json:"coverage_reuse,omitempty"`
	EverySteps    int             `json:"every_steps,omitempty"`
	OnCompletion  *bool           `json:"on_completion,omitempty"`
	Lease         domain.Lease    `json:"-"`
}

type BindResourceRequest struct {
	Binding json.RawMessage `json:"binding"`
	Lease   domain.Lease    `json:"-"`
}

type ResourceCapture struct {
	Key           string `json:"key"`
	Generation    int64  `json:"generation"`
	CheckpointRef string `json:"checkpoint_ref"`
}

type ResourceCaptureFailure struct {
	Key        string `json:"key"`
	Generation int64  `json:"generation"`
	Error      string `json:"error"`
}

type PublishCheckpointsRequest struct {
	Captures        []ResourceCapture        `json:"captures,omitempty"`
	CaptureFailures []ResourceCaptureFailure `json:"capture_failures,omitempty"`
	Lease           domain.Lease             `json:"-"`
}

type ResourceView struct {
	domain.Resource
	Covered bool `json:"covered"`
}

var resourceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func (k *Kernel) liveResourceTx(ctx context.Context, execID uuid.UUID, lease domain.Lease, fn func(tx store.TxStore) error) error {
	if !lease.Valid() {
		return fmt.Errorf("%w: missing dispatch lease", domain.ErrValidation)
	}
	return k.d.UnitOfWork.RunLocked(ctx, lockKey(execID), func(ctx context.Context) error {
		exec, err := authorizedExecution(ctx, k.d.Executions, execID)
		if err != nil {
			return err
		}
		if exec.Status.IsTerminal() {
			return domain.ErrExecutionTerminal
		}
		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			if err := renewAuthorizedLease(ctx, tx, execID, lease, time.Now().UTC()); err != nil {
				return err
			}
			return fn(tx)
		})
	})
}

func findResource(resources []domain.Resource, key string) (domain.Resource, bool) {
	i := slices.IndexFunc(resources, func(r domain.Resource) bool { return r.Key == key })
	if i < 0 {
		return domain.Resource{}, false
	}
	return resources[i], true
}

func resourceView(ctx context.Context, tx store.TxStore, r domain.Resource) (ResourceView, error) {
	checkpoints, err := tx.ListCheckpoints(ctx, r.ExecutionID)
	if err != nil {
		return ResourceView{}, err
	}
	covered := slices.ContainsFunc(checkpoints, func(c domain.ResourceCheckpoint) bool {
		return c.Key == r.Key && c.Generation == r.Generation && c.InvalidatedSeq == 0
	})
	return ResourceView{Resource: r, Covered: covered}, nil
}

func registerResourceTx(ctx context.Context, tx store.TxStore, r *domain.Resource) error {
	events := []store.EventRecord{{Type: domain.EventResourceRegistered, Payload: map[string]any{
		"key":            r.Key,
		"driver_id":      r.DriverID,
		"configuration":  r.Config,
		"coverage_reuse": r.CoverageReuse,
		"every_steps":    r.EverySteps,
		"on_completion":  r.OnCompletion,
	}}}
	if len(r.Binding) > 0 {
		events = append(events, store.EventRecord{Type: domain.EventResourceBound, Payload: map[string]any{
			"key": r.Key, "binding": r.Binding,
		}})
	}
	recorded, err := tx.AppendBatch(ctx, r.ExecutionID, events)
	if err != nil {
		return err
	}
	r.RegisteredSeq = recorded[0].EventSeq
	return tx.PutResource(ctx, *r)
}

func inheritSessionResources(ctx context.Context, tx store.TxStore, exec domain.Execution) error {
	if exec.Session == "" || exec.ParentExecutionID == nil || exec.ForkedFrom != nil {
		return nil
	}
	parent, err := tx.GetExecution(ctx, *exec.ParentExecutionID)
	if err != nil {
		return err
	}
	if parent.Session != exec.Session {
		return nil
	}
	resources, err := tx.ListResources(ctx, parent.ID)
	if err != nil {
		return err
	}
	for _, r := range resources {
		r.ExecutionID = exec.ID
		r.Generation, r.Count = 0, 0
		r.CheckpointRef = ""
		if err := registerResourceTx(ctx, tx, &r); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) RegisterResource(ctx context.Context, execID uuid.UUID, req RegisterResourceRequest) (ResourceView, error) {
	if !resourceKeyPattern.MatchString(req.Key) {
		return ResourceView{}, fmt.Errorf("%w: key must match %s", domain.ErrValidation, resourceKeyPattern)
	}
	if req.DriverID == "" {
		return ResourceView{}, fmt.Errorf("%w: driver_id is required", domain.ErrValidation)
	}
	if req.EverySteps < 0 {
		return ResourceView{}, fmt.Errorf("%w: every_steps must be positive", domain.ErrValidation)
	}
	if req.EverySteps == 0 {
		req.EverySteps = 1
	}
	fingerprint, err := identity.ComputeArgsHash(req.Configuration)
	if err != nil {
		return ResourceView{}, fmt.Errorf("%w: invalid configuration: %v", domain.ErrValidation, err)
	}

	var view ResourceView
	err = k.liveResourceTx(ctx, execID, req.Lease, func(tx store.TxStore) error {
		resources, err := tx.ListResources(ctx, execID)
		if err != nil {
			return err
		}
		if r, ok := findResource(resources, req.Key); ok {
			stored, err := identity.ComputeArgsHash(r.Config)
			if err != nil {
				return err
			}
			if r.DriverID != req.DriverID || stored != fingerprint || r.CoverageReuse != req.CoverageReuse {
				return fmt.Errorf("%w: resource %q is registered with another driver, configuration, or coverage_reuse", domain.ErrConflict, r.Key)
			}
			view, err = resourceView(ctx, tx, r)
			return err
		}
		r := domain.Resource{
			ExecutionID:   execID,
			Key:           req.Key,
			DriverID:      req.DriverID,
			Config:        req.Configuration,
			CoverageReuse: req.CoverageReuse,
			EverySteps:    req.EverySteps,
			OnCompletion:  req.OnCompletion == nil || *req.OnCompletion,
		}
		if err := registerResourceTx(ctx, tx, &r); err != nil {
			return err
		}
		view = ResourceView{Resource: r}
		return nil
	})
	return view, err
}

func (k *Kernel) BindResource(ctx context.Context, execID uuid.UUID, key string, req BindResourceRequest) error {
	if len(req.Binding) == 0 || string(req.Binding) == "null" {
		return fmt.Errorf("%w: binding is required", domain.ErrValidation)
	}
	return k.liveResourceTx(ctx, execID, req.Lease, func(tx store.TxStore) error {
		resources, err := tx.ListResources(ctx, execID)
		if err != nil {
			return err
		}
		r, ok := findResource(resources, key)
		if !ok {
			return domain.ErrNotFound
		}
		if len(r.Binding) > 0 {
			stored, _ := identity.CanonicalizeJSON(r.Binding)
			given, err := identity.CanonicalizeJSON(req.Binding)
			if err != nil || !bytes.Equal(stored, given) {
				return fmt.Errorf("%w: resource %q is already bound", domain.ErrConflict, key)
			}
			return nil
		}
		r.Binding = req.Binding
		if _, err := tx.Append(ctx, execID, domain.EventResourceBound, map[string]any{"key": key, "binding": req.Binding}); err != nil {
			return err
		}
		return tx.PutResource(ctx, r)
	})
}

func (k *Kernel) PublishCheckpoints(ctx context.Context, execID uuid.UUID, req PublishCheckpointsRequest) error {
	return k.liveResourceTx(ctx, execID, req.Lease, func(tx store.TxStore) error {
		latest, err := tx.GetLatestSequence(ctx, execID)
		if err != nil {
			return err
		}
		return recordCaptures(ctx, tx, execID, latest, req.Captures, req.CaptureFailures)
	})
}

func (k *Kernel) checkDeclaredResources(ctx context.Context, execID uuid.UUID, declared []string) error {
	if len(declared) == 0 {
		return nil
	}
	return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		resources, err := tx.ListResources(ctx, execID)
		if err != nil {
			return err
		}
		for _, key := range declared {
			if _, ok := findResource(resources, key); !ok {
				return fmt.Errorf("%w: unknown resource %q", domain.ErrValidation, key)
			}
		}
		return nil
	})
}

func touchResources(resources []domain.Resource, step *domain.Step) []domain.StepResource {
	if len(resources) == 0 {
		return nil
	}
	declared := step.Resources
	affected := []string{}
	var touched []domain.StepResource
	for _, r := range resources {
		affects := step.Kind != domain.StepKindLLM && slices.Contains(declared, r.Key)
		if !affects && r.CoverageReuse {
			continue
		}
		t := domain.StepResource{Key: r.Key, Generation: r.Generation + 1}
		if affects {
			affected = append(affected, r.Key)
			t.Due = (r.Count+1)%r.EverySteps == 0
		}
		touched = append(touched, t)
	}
	step.Resources = affected
	return touched
}

func settleEffect(ctx context.Context, tx store.TxStore, step domain.Step) error {
	if len(step.Resources) == 0 {
		return nil
	}
	resources, err := tx.ListResources(ctx, step.ExecutionID)
	if err != nil {
		return err
	}
	for _, r := range resources {
		if !slices.Contains(step.Resources, r.Key) {
			continue
		}
		r.Count++
		if err := tx.PutResource(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func recordCaptures(ctx context.Context, tx store.TxStore, execID uuid.UUID, coveredSeq int64, captures []ResourceCapture, failures []ResourceCaptureFailure) error {
	if len(captures)+len(failures) == 0 {
		return nil
	}
	resources, err := tx.ListResources(ctx, execID)
	if err != nil {
		return err
	}
	steps, err := tx.ListByExecution(ctx, execID)
	if err != nil {
		return err
	}
	writing := make(map[string]bool)
	for _, s := range steps {
		if s.Status == domain.StepExecuting {
			for _, key := range s.Resources {
				writing[key] = true
			}
		}
	}

	failed := func(key string, generation int64, reason string) store.EventRecord {
		return store.EventRecord{Type: domain.EventResourceCheckpointFailed, Payload: map[string]any{
			"key": key, "generation": generation, "error": reason,
		}}
	}
	var evts []store.EventRecord
	for _, f := range failures {
		evts = append(evts, failed(f.Key, f.Generation, f.Error))
	}
	recorded := make(map[string]bool)
	for _, c := range captures {
		r, ok := findResource(resources, c.Key)
		var refusal string
		switch {
		case !ok:
			refusal = "unknown_resource"
		case c.CheckpointRef == "":
			refusal = "missing_checkpoint_ref"
		case recorded[c.Key]:
			refusal = "duplicate_capture"
		case c.Generation != r.Generation:
			refusal = "stale_generation"
		case writing[c.Key]:
			refusal = "writers_active"
		}
		if refusal != "" {
			evts = append(evts, failed(c.Key, c.Generation, refusal))
			continue
		}
		recorded[c.Key] = true
		if err := tx.AddCheckpoint(ctx, domain.ResourceCheckpoint{
			ExecutionID: execID,
			Key:         c.Key,
			Generation:  c.Generation,
			Ref:         c.CheckpointRef,
			CoveredSeq:  coveredSeq,
		}); err != nil {
			return err
		}
		evts = append(evts, store.EventRecord{Type: domain.EventResourceCheckpointed, Payload: map[string]any{
			"key": c.Key, "generation": c.Generation, "checkpoint_ref": c.CheckpointRef, "covered_seq": coveredSeq,
		}})
	}
	_, err = tx.AppendBatch(ctx, execID, evts)
	return err
}

// Checkpoints are ordered by coverage start.
func selectResource(checkpoints []domain.ResourceCheckpoint, key string, seq int64) domain.ResourceSelection {
	var selected domain.ResourceSelection
	for _, c := range checkpoints {
		if c.Key != key || c.CoveredSeq > seq {
			continue
		}
		covered := c.Covers(seq)
		if selected.Covered && !covered {
			continue
		}
		selected = domain.ResourceSelection{CheckpointRef: c.Ref, CheckpointSeq: c.CoveredSeq, Covered: covered}
	}
	return selected
}

type ForkPoints struct {
	LatestSeq   int64                       `json:"latest_seq"`
	Resources   map[string]int64            `json:"resources"`
	Checkpoints []domain.ResourceCheckpoint `json:"checkpoints"`
}

func (k *Kernel) ForkPoints(ctx context.Context, execID uuid.UUID) (ForkPoints, error) {
	if _, err := authorizedExecution(ctx, k.d.Executions, execID); err != nil {
		return ForkPoints{}, err
	}
	points := ForkPoints{Resources: make(map[string]int64), Checkpoints: []domain.ResourceCheckpoint{}}
	err := k.d.UnitOfWork.RunLocked(ctx, lockKey(execID), func(ctx context.Context) error {
		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			resources, err := tx.ListResources(ctx, execID)
			if err != nil {
				return err
			}
			for _, r := range resources {
				points.Resources[r.Key] = r.RegisteredSeq
			}
			checkpoints, err := tx.ListCheckpoints(ctx, execID)
			if err != nil {
				return err
			}
			points.Checkpoints = append(points.Checkpoints, checkpoints...)
			points.LatestSeq, err = tx.GetLatestSequence(ctx, execID)
			return err
		})
	})
	return points, err
}

func copyResources(
	ctx context.Context, tx store.TxStore, fork domain.Execution,
	resources []domain.Resource, checkpoints []domain.ResourceCheckpoint,
	generations map[string]int64,
	mapSeq func(int64) int64, counts map[string]int,
) error {
	var evts []store.EventRecord
	for _, r := range resources {
		sel, ok := fork.Restoration[r.Key]
		if !ok {
			continue
		}
		r.ExecutionID = fork.ID
		r.RegisteredSeq = mapSeq(r.RegisteredSeq)
		r.Generation = generations[r.Key]
		if !sel.Covered {
			r.Generation++
		}
		r.Count = counts[r.Key]
		r.Binding = nil
		r.CheckpointRef = sel.CheckpointRef
		initialized := map[string]any{"key": r.Key, "generation": r.Generation}
		if sel.CheckpointRef != "" {
			initialized["checkpoint_ref"] = sel.CheckpointRef
		}
		_, err := tx.Append(ctx, fork.ID, domain.EventResourceInitialized, initialized)
		if err != nil {
			return err
		}
		if err := tx.PutResource(ctx, r); err != nil {
			return err
		}
		// Several source checkpoints can land on one fork event; keep the newest.
		bySeq := make(map[int64]domain.ResourceCheckpoint)
		for _, c := range checkpoints {
			if c.Key != r.Key || c.CoveredSeq > fork.ForkSeq {
				continue
			}
			c.ExecutionID = fork.ID
			c.CoveredSeq = mapSeq(c.CoveredSeq)
			if c.InvalidatedSeq > fork.ForkSeq {
				c.InvalidatedSeq = 0
			} else if c.InvalidatedSeq != 0 {
				c.InvalidatedSeq = mapSeq(c.InvalidatedSeq)
			}
			bySeq[c.CoveredSeq] = c
		}
		for _, seq := range slices.Sorted(maps.Keys(bySeq)) {
			c := bySeq[seq]
			if err := tx.AddCheckpoint(ctx, c); err != nil {
				return err
			}
			evts = append(evts, store.EventRecord{Type: domain.EventResourceCheckpointed, Payload: map[string]any{
				"key": c.Key, "generation": c.Generation, "checkpoint_ref": c.Ref, "covered_seq": c.CoveredSeq,
			}})
		}
	}
	_, err := tx.AppendBatch(ctx, fork.ID, evts)
	return err
}

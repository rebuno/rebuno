package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/payload"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store"
)

type ForkRequest struct {
	Session      string `json:"session,omitempty"`
	AtSeq        int64  `json:"at_seq"`
	PolicyBundle string `json:"policy_bundle,omitempty"`
}

func (k *Kernel) ForkExecution(ctx context.Context, sourceID uuid.UUID, req ForkRequest) (domain.Execution, error) {
	source, err := authorizedExecution(ctx, k.d.Executions, sourceID)
	if err != nil {
		return domain.Execution{}, err
	}
	if err := k.requireNewSession(ctx, req.Session); err != nil {
		return domain.Execution{}, err
	}
	latest, err := k.d.Events.GetLatestSequence(ctx, source.ID)
	if err != nil {
		return domain.Execution{}, err
	}
	if req.AtSeq < 1 || req.AtSeq > latest {
		return domain.Execution{}, fmt.Errorf("%w: at_seq must be between 1 and %d", domain.ErrValidation, latest)
	}
	if req.PolicyBundle != "" {
		if !auth.HasScope(ctx, domain.ScopePoliciesWrite) {
			return domain.Execution{}, fmt.Errorf("%w: policy_bundle requires %s", domain.ErrForbidden, domain.ScopePoliciesWrite)
		}
		if _, err := policy.NewRuleEngineFromBundle(req.PolicyBundle); err != nil {
			return domain.Execution{}, fmt.Errorf("%w: invalid policy bundle: %v", domain.ErrValidation, err)
		}
	}
	policyBundle := req.PolicyBundle
	if policyBundle == "" {
		policyBundle = source.PolicyBundle
	}

	now := time.Now().UTC()
	exec := domain.Execution{
		ID:                uuid.Must(uuid.NewV7()),
		AgentID:           source.AgentID,
		Session:           req.Session,
		ParentExecutionID: source.ParentExecutionID,
		ForkedFrom:        &source.ID,
		ForkSeq:           req.AtSeq,
		Input:             source.Input,
		Status:            domain.ExecutionPending,
		PolicyBundle:      policyBundle,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if k.cfg.ExecutionDeadlineTimeout > 0 {
		deadline := now.Add(k.cfg.ExecutionDeadlineTimeout)
		exec.DeadlineAt = &deadline
	}

	created := payload.Execution(exec.ID, exec.Status, nil, "")
	if exec.Session != "" {
		created["session"] = exec.Session
	}
	if exec.DeadlineAt != nil {
		created["deadline_at"] = *exec.DeadlineAt
	}
	if err := k.d.UnitOfWork.RunLocked(ctx, lockKey(source.ID), func(ctx context.Context) error {
		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			prefix, err := readForkPrefix(ctx, tx, source.ID, exec.ForkSeq)
			if err != nil {
				return err
			}
			resources, err := tx.ListResources(ctx, source.ID)
			if err != nil {
				return err
			}
			checkpoints, err := tx.ListCheckpoints(ctx, source.ID)
			if err != nil {
				return err
			}
			for _, r := range resources {
				if r.RegisteredSeq <= exec.ForkSeq {
					if exec.Restoration == nil {
						exec.Restoration = make(map[string]domain.ResourceSelection)
					}
					exec.Restoration[r.Key] = selectResource(checkpoints, r.Key, exec.ForkSeq)
				}
			}
			forked := map[string]any{
				"source_execution_id": source.ID.String(),
				"fork_seq":            exec.ForkSeq,
				"policy_override":     req.PolicyBundle != "",
			}
			if exec.Restoration != nil {
				forked["restoration"] = exec.Restoration
			}
			if err := tx.CreateExecution(ctx, exec); err != nil {
				return err
			}
			if _, err := tx.AppendBatch(ctx, exec.ID, []store.EventRecord{
				{Type: domain.EventExecutionCreated, Payload: created},
				{Type: domain.EventExecutionForked, Payload: forked},
			}); err != nil {
				return err
			}
			mapSeq, counts, err := copyRecordedSteps(ctx, tx, prefix, source.ID, exec.ID)
			if err != nil {
				return err
			}
			if err := copyResources(ctx, tx, exec, resources, checkpoints, prefix.generations, mapSeq, counts); err != nil {
				return err
			}
			if exec.Session != "" {
				return nil
			}
			return k.startExecutionTx(ctx, tx, &exec)
		})
	}); err != nil {
		return domain.Execution{}, err
	}
	k.d.Observer.RecordExecutionCreated()
	if exec.Session == "" {
		return exec, nil
	}
	started, err := k.admitNext(ctx, exec.Session)
	if err != nil {
		k.log.Warn("admit execution failed", "error", err) // the deadline sweep retries
	}
	if started.ID == exec.ID {
		started.Restoration = exec.Restoration
		return started, nil
	}
	return exec, nil
}

const forkEventPage = 1000

type forkPrefix struct {
	events      []domain.Event
	settled     map[string]bool
	generations map[string]int64
}

func readForkPrefix(ctx context.Context, tx store.TxStore, sourceID uuid.UUID, atSeq int64) (forkPrefix, error) {
	prefix := forkPrefix{settled: make(map[string]bool), generations: make(map[string]int64)}
	for after := int64(0); after < atSeq; {
		page, err := tx.GetEvents(ctx, sourceID, after, forkEventPage)
		if err != nil {
			return forkPrefix{}, err
		}
		for _, e := range page {
			if e.EventSeq > atSeq {
				break
			}
			var body struct {
				Key         string           `json:"key"`
				Generation  int64            `json:"generation"`
				Generations map[string]int64 `json:"generations"`
			}
			switch e.Type {
			case domain.EventResourceRegistered:
				prefix.events = append(prefix.events, e)
				continue
			case domain.EventResourceInitialized:
				if err := json.Unmarshal(e.Payload, &body); err != nil {
					return forkPrefix{}, err
				}
				prefix.generations[body.Key] = body.Generation
				prefix.events = append(prefix.events, e)
				continue
			case domain.EventStepExecuting:
				if err := json.Unmarshal(e.Payload, &body); err != nil {
					return forkPrefix{}, err
				}
				maps.Copy(prefix.generations, body.Generations)
			}
			id := eventStepID(e)
			if id == "" {
				continue
			}
			prefix.events = append(prefix.events, e)
			switch e.Type {
			case domain.EventStepSucceeded, domain.EventStepFailed, domain.EventStepDenied:
				prefix.settled[id] = true
			}
		}
		if len(page) < forkEventPage {
			break
		}
		after = page[len(page)-1].EventSeq
	}
	return prefix, nil
}

func copyRecordedSteps(ctx context.Context, tx store.TxStore, prefix forkPrefix, sourceID, forkID uuid.UUID) (func(int64) int64, map[string]int, error) {
	steps, err := tx.ListByExecution(ctx, sourceID)
	if err != nil {
		return nil, nil, err
	}
	renamed := make(map[string]string)
	counts := make(map[string]int)
	for _, s := range steps {
		if !prefix.settled[s.StepID] {
			continue
		}
		switch s.Status {
		case domain.StepSucceeded, domain.StepFailed, domain.StepDenied:
		default:
			continue
		}
		id := identity.ComputeStepID(forkID, s.Kind, s.Target, s.ArgsHash, s.Occurrence)
		renamed[s.StepID] = id
		if s.Status != domain.StepDenied {
			for _, key := range s.Resources {
				counts[key]++
			}
		}
		s.StepID = id
		s.ExecutionID = forkID
		if err := tx.Upsert(ctx, s); err != nil {
			return nil, nil, err
		}
	}

	base, err := tx.GetLatestSequence(ctx, forkID)
	if err != nil {
		return nil, nil, err
	}
	var sourceSeqs []int64
	var records []store.EventRecord
	for _, e := range prefix.events {
		var body map[string]any
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			return nil, nil, err
		}
		if e.Type != domain.EventResourceRegistered && e.Type != domain.EventResourceInitialized {
			stepID, _ := body["step_id"].(string)
			id, ok := renamed[stepID]
			if !ok {
				continue
			}
			body["step_id"] = id
		}
		records = append(records, store.EventRecord{Type: e.Type, Payload: body})
		sourceSeqs = append(sourceSeqs, e.EventSeq)
	}
	mapSeq := func(seq int64) int64 {
		n, _ := slices.BinarySearch(sourceSeqs, seq+1)
		return base + int64(n)
	}
	if len(records) == 0 {
		return mapSeq, counts, nil
	}
	_, err = tx.AppendBatch(ctx, forkID, records)
	return mapSeq, counts, err
}

func eventStepID(e domain.Event) string {
	if !strings.HasPrefix(e.Type, "step.") {
		return ""
	}
	var body struct {
		StepID string `json:"step_id"`
	}
	if err := json.Unmarshal(e.Payload, &body); err != nil {
		return ""
	}
	return body.StepID
}

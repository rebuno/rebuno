package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		PolicyBundle:      req.PolicyBundle,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if k.cfg.ExecutionDeadlineTimeout > 0 {
		deadline := now.Add(k.cfg.ExecutionDeadlineTimeout)
		exec.DeadlineAt = &deadline
	}

	forked := map[string]any{
		"source_execution_id": source.ID.String(),
		"fork_seq":            exec.ForkSeq,
		"policy_override":     req.PolicyBundle != "",
	}
	created := payload.Execution(exec.ID, exec.Status, nil, "")
	if exec.Session != "" {
		created["session"] = exec.Session
	}
	if exec.DeadlineAt != nil {
		created["deadline_at"] = *exec.DeadlineAt
	}
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if err := tx.CreateExecution(ctx, exec); err != nil {
			return err
		}
		if _, err := tx.AppendBatch(ctx, exec.ID, []store.EventRecord{
			{Type: domain.EventExecutionCreated, Payload: created},
			{Type: domain.EventExecutionForked, Payload: forked},
		}); err != nil {
			return err
		}
		if err := copyRecordedSteps(ctx, tx, source.ID, exec.ID, exec.ForkSeq); err != nil {
			return err
		}
		if exec.Session != "" {
			return nil
		}
		return k.startExecutionTx(ctx, tx, &exec)
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
		return started, nil
	}
	return exec, nil
}

const forkEventPage = 1000

func copyRecordedSteps(ctx context.Context, tx store.TxStore, sourceID, forkID uuid.UUID, atSeq int64) error {
	var stepEvents []domain.Event
	settled := make(map[string]bool)
	for after := int64(0); after < atSeq; {
		page, err := tx.GetEvents(ctx, sourceID, after, forkEventPage)
		if err != nil {
			return err
		}
		for _, e := range page {
			if e.EventSeq > atSeq {
				break
			}
			id := eventStepID(e)
			if id == "" {
				continue
			}
			stepEvents = append(stepEvents, e)
			switch e.Type {
			case domain.EventStepSucceeded, domain.EventStepFailed, domain.EventStepDenied:
				settled[id] = true
			}
		}
		if len(page) < forkEventPage {
			break
		}
		after = page[len(page)-1].EventSeq
	}

	steps, err := tx.ListByExecution(ctx, sourceID)
	if err != nil {
		return err
	}
	renamed := make(map[string]string)
	for _, s := range steps {
		if !settled[s.StepID] {
			continue
		}
		switch s.Status {
		case domain.StepSucceeded, domain.StepFailed, domain.StepDenied:
		default:
			continue
		}
		id := identity.ComputeStepID(forkID, s.Kind, s.Target, s.ArgsHash, s.Occurrence)
		renamed[s.StepID] = id
		s.StepID = id
		s.ExecutionID = forkID
		if err := tx.Upsert(ctx, s); err != nil {
			return err
		}
	}

	var records []store.EventRecord
	for _, e := range stepEvents {
		id, ok := renamed[eventStepID(e)]
		if !ok {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			return err
		}
		body["step_id"] = id
		records = append(records, store.EventRecord{Type: e.Type, Payload: body})
	}
	if len(records) == 0 {
		return nil
	}
	_, err = tx.AppendBatch(ctx, forkID, records)
	return err
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

func (k *Kernel) repeatsSourceEffect(ctx context.Context, exec domain.Execution, req SubmitStepRequest, argsHash string, occurrence int) (bool, error) {
	if exec.ForkedFrom == nil || req.Idempotency != "at_most_once" {
		return false, nil
	}
	sourceStep, err := k.d.Steps.GetStep(ctx, identity.ComputeStepID(*exec.ForkedFrom, req.Kind, req.Target, argsHash, occurrence))
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sourceStep.StartedAt != nil, nil
}

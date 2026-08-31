package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) Upsert(ctx context.Context, step domain.Step) error {
	return upsertStep(ctx, s.pool, step)
}

func (q querier) Upsert(ctx context.Context, step domain.Step) error {
	return upsertStep(ctx, q.q, step)
}

func upsertStep(ctx context.Context, q Querier, step domain.Step) error {
	terminalStatuses := []string{string(domain.StepSucceeded), string(domain.StepFailed), string(domain.StepDenied), string(domain.StepCancelled)}
	result := rawArg(step.Result)
	errPayload := rawArg(step.Error)
	argsPayload := rawArg(step.Args)

	_, err := q.Exec(ctx, `
		INSERT INTO steps (
			step_id, execution_id, kind, target, args_hash, occurrence, status,
			idempotency, args, result, error, started_at, completed_at,
			usage_input, usage_output
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13, $15, $16)
		ON CONFLICT (step_id) DO UPDATE SET
			execution_id = EXCLUDED.execution_id,
			kind         = EXCLUDED.kind,
			target       = EXCLUDED.target,
			args_hash    = EXCLUDED.args_hash,
			occurrence   = EXCLUDED.occurrence,
			status       = CASE WHEN steps.status = ANY($14::text[])
							THEN steps.status ELSE EXCLUDED.status END,
			idempotency  = EXCLUDED.idempotency,
			args         = EXCLUDED.args,
			result       = CASE WHEN steps.status = ANY($14::text[])
							THEN steps.result ELSE EXCLUDED.result END,
			error        = CASE WHEN steps.status = ANY($14::text[])
							THEN steps.error ELSE EXCLUDED.error END,
			started_at   = EXCLUDED.started_at,
			completed_at = CASE WHEN steps.status = ANY($14::text[])
							THEN steps.completed_at ELSE EXCLUDED.completed_at END,
			usage_input  = CASE WHEN steps.status = ANY($14::text[])
							THEN steps.usage_input ELSE EXCLUDED.usage_input END,
			usage_output = CASE WHEN steps.status = ANY($14::text[])
							THEN steps.usage_output ELSE EXCLUDED.usage_output END
	`,
		step.StepID, step.ExecutionID.String(), string(step.Kind), step.Target, step.ArgsHash, step.Occurrence,
		string(step.Status), step.Idempotency, argsPayload, result, errPayload,
		timeArg(step.StartedAt), timeArg(step.CompletedAt), terminalStatuses,
		step.UsageInput, step.UsageOutput,
	)
	if err != nil {
		return fmt.Errorf("upsert step: %w", err)
	}
	return nil
}

func (s *Store) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return getStep(ctx, s.pool, stepID)
}

func (q querier) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return getStep(ctx, q.q, stepID)
}

func getStep(ctx context.Context, q Querier, stepID string) (domain.Step, error) {
	row := q.QueryRow(ctx, `
		SELECT step_id, execution_id, kind, target, args_hash, occurrence, status,
		       idempotency, args, result, error, started_at, completed_at,
		       COALESCE(usage_input, 0), COALESCE(usage_output, 0)
		FROM steps
		WHERE step_id = $1
	`, stepID)
	step, err := scanStep(row)
	if err != nil {
		return domain.Step{}, mapNotFound(err)
	}
	return step, nil
}

func (s *Store) DispatchOccurrence(ctx context.Context, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	return dispatchOccurrence(ctx, s.pool, dispatchID, kind, target, argsHash)
}

func (q querier) DispatchOccurrence(ctx context.Context, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	return dispatchOccurrence(ctx, q.q, dispatchID, kind, target, argsHash)
}

func dispatchOccurrence(ctx context.Context, q Querier, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	var consumed int
	err := q.QueryRow(ctx, `
		SELECT consumed + 1 FROM dispatch_step_counters
		WHERE dispatch_id = $1 AND kind = $2 AND target = $3 AND args_hash = $4
	`, dispatchID.String(), string(kind), target, argsHash).Scan(&consumed)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("dispatch occurrence: %w", err)
	}
	return consumed, nil
}

func (s *Store) AdvanceDispatchOccurrence(ctx context.Context, execID uuid.UUID, lease domain.Lease, kind domain.StepKind, target, argsHash string, consumed int) error {
	return advanceDispatchOccurrence(ctx, s.pool, execID, lease, kind, target, argsHash, consumed)
}

func (q querier) AdvanceDispatchOccurrence(ctx context.Context, execID uuid.UUID, lease domain.Lease, kind domain.StepKind, target, argsHash string, consumed int) error {
	return advanceDispatchOccurrence(ctx, q.q, execID, lease, kind, target, argsHash, consumed)
}

func advanceDispatchOccurrence(ctx context.Context, q Querier, execID uuid.UUID, lease domain.Lease, kind domain.StepKind, target, argsHash string, consumed int) error {
	// Selecting the dispatch row makes the fence part of the write, and FOR
	// UPDATE waits for a claim landing mid-statement rather than reading the row
	// stale. GREATEST keeps the counter monotonic if a retry replays an older
	// occurrence.
	res, err := q.Exec(ctx, `
		INSERT INTO dispatch_step_counters (dispatch_id, kind, target, args_hash, consumed)
		SELECT d.id, $2::text, $3::text, $4::text, $5::int
		FROM dispatches d
		WHERE d.id = $1::uuid AND d.execution_id = $6::uuid AND d.attempt = $7::int
		FOR UPDATE
		ON CONFLICT (dispatch_id, kind, target, args_hash)
		DO UPDATE SET consumed = GREATEST(dispatch_step_counters.consumed, EXCLUDED.consumed)
	`, lease.DispatchID.String(), string(kind), target, argsHash, consumed, execID.String(), lease.Attempt)
	if err != nil {
		return fmt.Errorf("advance dispatch occurrence: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrLeaseSuperseded
	}
	return nil
}

func (s *Store) ListByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	return listStepsByExecution(ctx, s.pool, execID)
}

func (q querier) ListByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	return listStepsByExecution(ctx, q.q, execID)
}

func listStepsByExecution(ctx context.Context, q Querier, execID uuid.UUID) ([]domain.Step, error) {
	rows, err := q.Query(ctx, `
		SELECT step_id, execution_id, kind, target, args_hash, occurrence, status,
		       idempotency, args, result, error, started_at, completed_at,
		       COALESCE(usage_input, 0), COALESCE(usage_output, 0)
		FROM steps
		WHERE execution_id = $1
		ORDER BY step_id
	`, execID.String())
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer rows.Close()

	return scanSteps(rows)
}

func executionUsage(ctx context.Context, q Querier, execID uuid.UUID) (int, error) {
	var total int
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(usage_input, 0) + COALESCE(usage_output, 0)), 0)
		FROM steps
		WHERE execution_id = $1
	`, execID.String()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum execution usage: %w", err)
	}
	return total, nil
}

func (s *Store) ExecutionUsage(ctx context.Context, execID uuid.UUID) (int, error) {
	return executionUsage(ctx, s.pool, execID)
}

func (q querier) ExecutionUsage(ctx context.Context, execID uuid.UUID) (int, error) {
	return executionUsage(ctx, q.q, execID)
}

func scanStep(row pgx.Row) (domain.Step, error) {
	var step domain.Step
	var execID string
	var status string
	var kind string
	var args, result, errPayload *string

	if err := row.Scan(
		&step.StepID, &execID, &kind, &step.Target, &step.ArgsHash, &step.Occurrence, &status,
		&step.Idempotency, &args, &result, &errPayload, &step.StartedAt, &step.CompletedAt,
		&step.UsageInput, &step.UsageOutput,
	); err != nil {
		return domain.Step{}, err
	}

	id, err := parseUUID(execID)
	if err != nil {
		return domain.Step{}, fmt.Errorf("parse execution_id: %w", err)
	}
	step.ExecutionID = id
	step.Kind = domain.StepKind(kind)
	step.Status = domain.StepStatus(status)
	step.Args = rawFromPtr(args)
	step.Result = rawFromPtr(result)
	step.Error = rawFromPtr(errPayload)
	return step, nil
}

func scanSteps(rows pgx.Rows) ([]domain.Step, error) {
	var out []domain.Step
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate steps: %w", err)
	}
	return out, nil
}

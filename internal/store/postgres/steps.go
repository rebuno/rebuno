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
	terminalStatuses := []string{string(domain.StepSucceeded), string(domain.StepFailed), string(domain.StepDenied)}
	result := rawArg(step.Result)
	errPayload := rawArg(step.Error)
	argsPayload := rawArg(step.Args)

	_, err := q.Exec(ctx, `
		INSERT INTO steps (
			step_id, execution_id, kind, target, args_hash, occurrence, status,
			idempotency, args, result, error, started_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13)
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
							THEN steps.completed_at ELSE EXCLUDED.completed_at END
	`,
		step.StepID, step.ExecutionID.String(), string(step.Kind), step.Target, step.ArgsHash, step.Occurrence,
		string(step.Status), step.Idempotency, argsPayload, result, errPayload,
		timeArg(step.StartedAt), timeArg(step.CompletedAt), terminalStatuses,
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
		       idempotency, args, result, error, started_at, completed_at
		FROM steps
		WHERE step_id = $1
	`, stepID)
	step, err := scanStep(row)
	if err != nil {
		return domain.Step{}, mapNotFound(err)
	}
	return step, nil
}

func (s *Store) CountOccurrence(ctx context.Context, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	return countOccurrence(ctx, s.pool, execID, kind, target, argsHash)
}

func (q querier) CountOccurrence(ctx context.Context, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	return countOccurrence(ctx, q.q, execID, kind, target, argsHash)
}

func countOccurrence(ctx context.Context, q Querier, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM steps
		WHERE execution_id = $1 AND kind = $2 AND target = $3 AND args_hash = $4
	`, execID.String(), string(kind), target, argsHash).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count occurrences: %w", err)
	}
	return n, nil
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
		       idempotency, args, result, error, started_at, completed_at
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

func scanStep(row pgx.Row) (domain.Step, error) {
	var step domain.Step
	var execID string
	var status string
	var kind string
	var args, result, errPayload *string

	if err := row.Scan(
		&step.StepID, &execID, &kind, &step.Target, &step.ArgsHash, &step.Occurrence, &status,
		&step.Idempotency, &args, &result, &errPayload, &step.StartedAt, &step.CompletedAt,
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

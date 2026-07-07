package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/kernel/internal/domain"
)

func (s *Store) CreateApproval(ctx context.Context, approval domain.Approval) error {
	return createApproval(ctx, s.pool, approval)
}

func (q querier) CreateApproval(ctx context.Context, approval domain.Approval) error {
	return createApproval(ctx, q.q, approval)
}

func createApproval(ctx context.Context, q Querier, approval domain.Approval) error {
	createdAt := approval.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	_, err := q.Exec(ctx, `
		INSERT INTO approvals (id, step_id, execution_id, status, approvers, message, timeout_at, decided_by, decided_at, rationale, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11)
	`,
		approval.ID.String(),
		approval.StepID,
		approval.ExecutionID.String(),
		string(approval.Status),
		rawArg(approval.Approvers),
		approval.Message,
		approval.TimeoutAt,
		approval.DecidedBy,
		timeArg(approval.DecidedAt),
		approval.Rationale,
		createdAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		if isForeignKeyViolation(err) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("create approval: %w", err)
	}
	return nil
}

func (s *Store) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	return getApproval(ctx, s.pool, id)
}

func (q querier) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	return getApproval(ctx, q.q, id)
}

func getApproval(ctx context.Context, q Querier, id uuid.UUID) (domain.Approval, error) {
	row := q.QueryRow(ctx, `
		SELECT id, step_id, execution_id, status, approvers, message, timeout_at, decided_by, decided_at, rationale, created_at
		FROM approvals
		WHERE id = $1
	`, id.String())
	approval, err := scanApproval(row)
	if err != nil {
		return domain.Approval{}, mapNotFound(err)
	}
	return approval, nil
}

func (s *Store) UpdateApproval(ctx context.Context, approval domain.Approval) error {
	return updateApproval(ctx, s.pool, approval)
}

func (q querier) UpdateApproval(ctx context.Context, approval domain.Approval) error {
	return updateApproval(ctx, q.q, approval)
}

func updateApproval(ctx context.Context, q Querier, approval domain.Approval) error {
	res, err := q.Exec(ctx, `
		UPDATE approvals
		SET step_id = $2,
		    status = $3,
		    approvers = $4::jsonb,
		    message = $5,
		    timeout_at = $6,
		    decided_by = $7,
		    decided_at = $8,
		    rationale = $9
		WHERE id = $1
	`,
		approval.ID.String(),
		approval.StepID,
		string(approval.Status),
		rawArg(approval.Approvers),
		approval.Message,
		approval.TimeoutAt,
		approval.DecidedBy,
		timeArg(approval.DecidedAt),
		approval.Rationale,
	)
	if err != nil {
		return fmt.Errorf("update approval: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	return listPendingApprovals(ctx, s.pool)
}

func (q querier) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	return listPendingApprovals(ctx, q.q)
}

func listPendingApprovals(ctx context.Context, q Querier) ([]domain.Approval, error) {
	rows, err := q.Query(ctx, `
		SELECT id, step_id, execution_id, status, approvers, message, timeout_at, decided_by, decided_at, rationale, created_at
		FROM approvals
		WHERE status = 'pending'
		ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	return scanApprovals(rows)
}

func (s *Store) ListExpiredApprovals(ctx context.Context, now time.Time) ([]domain.Approval, error) {
	return listExpiredApprovals(ctx, s.pool, now)
}

func (q querier) ListExpiredApprovals(ctx context.Context, now time.Time) ([]domain.Approval, error) {
	return listExpiredApprovals(ctx, q.q, now)
}

func listExpiredApprovals(ctx context.Context, q Querier, now time.Time) ([]domain.Approval, error) {
	rows, err := q.Query(ctx, `
		SELECT id, step_id, execution_id, status, approvers, message, timeout_at, decided_by, decided_at, rationale, created_at
		FROM approvals
		WHERE status = 'pending' AND timeout_at <= $1
		ORDER BY timeout_at
	`, now)
	if err != nil {
		return nil, fmt.Errorf("list expired approvals: %w", err)
	}
	defer rows.Close()
	return scanApprovals(rows)
}

func scanApproval(row pgx.Row) (domain.Approval, error) {
	var a domain.Approval
	var idStr, execIDStr, status string
	var approvers *string

	if err := row.Scan(
		&idStr, &a.StepID, &execIDStr, &status,
		&approvers, &a.Message, &a.TimeoutAt, &a.DecidedBy, &a.DecidedAt, &a.Rationale, &a.CreatedAt,
	); err != nil {
		return domain.Approval{}, err
	}

	id, err := parseUUID(idStr)
	if err != nil {
		return domain.Approval{}, fmt.Errorf("parse approval id: %w", err)
	}
	execID, err := parseUUID(execIDStr)
	if err != nil {
		return domain.Approval{}, fmt.Errorf("parse execution id: %w", err)
	}

	a.ID = id
	a.ExecutionID = execID
	a.Status = domain.ApprovalStatus(status)
	a.Approvers = rawFromPtr(approvers)
	return a, nil
}

func scanApprovals(rows pgx.Rows) ([]domain.Approval, error) {
	var out []domain.Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvals: %w", err)
	}
	return out, nil
}

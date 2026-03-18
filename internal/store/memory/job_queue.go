package memory

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

type JobQueue struct {
	mu   sync.Mutex
	jobs []domain.Job
}

func NewJobQueue() *JobQueue {
	return &JobQueue{}
}

func (q *JobQueue) Enqueue(_ context.Context, job domain.Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *JobQueue) DequeueForTool(_ context.Context, toolID string) (*domain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.jobs {
		if j.ToolID == toolID {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return &j, nil
		}
	}
	return nil, nil
}

func (q *JobQueue) All(_ context.Context) ([]domain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]domain.Job, len(q.jobs))
	copy(out, q.jobs)
	return out, nil
}

func (q *JobQueue) Remove(_ context.Context, jobID uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.jobs {
		if j.ID == jobID {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return nil
		}
	}
	return nil
}

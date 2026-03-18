package redis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/rebuno/rebuno/internal/domain"
)

const (
	keyPrefix   = "rebuno:jobs:"
	toolsSetKey = "rebuno:jobs:tools"
)

type JobQueue struct {
	client *redis.Client
}

func NewJobQueue(client *redis.Client) *JobQueue {
	return &JobQueue{client: client}
}

func toolListKey(toolID string) string {
	return keyPrefix + toolID
}

func (q *JobQueue) Enqueue(ctx context.Context, job domain.Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}

	pipe := q.client.Pipeline()
	pipe.LPush(ctx, toolListKey(job.ToolID), data)
	pipe.SAdd(ctx, toolsSetKey, job.ToolID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis enqueue: %w", err)
	}
	return nil
}

func (q *JobQueue) DequeueForTool(ctx context.Context, toolID string) (*domain.Job, error) {
	data, err := q.client.RPop(ctx, toolListKey(toolID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis dequeue: %w", err)
	}

	var job domain.Job
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("unmarshaling job: %w", err)
	}
	return &job, nil
}

func (q *JobQueue) All(ctx context.Context) ([]domain.Job, error) {
	toolIDs, err := q.client.SMembers(ctx, toolsSetKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis smembers: %w", err)
	}

	var jobs []domain.Job
	for _, toolID := range toolIDs {
		items, err := q.client.LRange(ctx, toolListKey(toolID), 0, -1).Result()
		if err != nil {
			return nil, fmt.Errorf("redis lrange %s: %w", toolID, err)
		}
		for _, item := range items {
			var job domain.Job
			if err := json.Unmarshal([]byte(item), &job); err != nil {
				return nil, fmt.Errorf("unmarshaling job: %w", err)
			}
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

func (q *JobQueue) Remove(ctx context.Context, jobID uuid.UUID) error {
	toolIDs, err := q.client.SMembers(ctx, toolsSetKey).Result()
	if err != nil {
		return fmt.Errorf("redis smembers: %w", err)
	}

	for _, toolID := range toolIDs {
		key := toolListKey(toolID)
		items, err := q.client.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return fmt.Errorf("redis lrange %s: %w", toolID, err)
		}
		for _, item := range items {
			var job domain.Job
			if err := json.Unmarshal([]byte(item), &job); err != nil {
				continue
			}
			if job.ID == jobID {
				q.client.LRem(ctx, key, 1, item)
				return nil
			}
		}
	}
	return nil
}

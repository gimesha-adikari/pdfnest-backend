package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"pdfnest-backend/internal/analyzer/engine"
)

// JobQueue defines the communication abstraction between the worker and task storage.
type JobQueue interface {
	Receive(ctx context.Context) (*AnalyzerJob, string, error)
	Ack(ctx context.Context, receiptHandle string) error
	Nack(ctx context.Context, receiptHandle string, retryable bool) error
	PublishProgress(ctx context.Context, taskID string, progress TaskProgress) error
	PublishResult(ctx context.Context, taskID string, result *engine.CanonicalAnalysisResult) error
	SendHeartbeat(ctx context.Context, info WorkerInfo, ttl time.Duration) error
	RemoveHeartbeat(ctx context.Context, workerID string) error
	GetActiveWorkers(ctx context.Context) ([]WorkerInfo, error)
	Close() error
}

// RedisJobQueue implements JobQueue against a live Redis cluster/instance.
type RedisJobQueue struct {
	client    *redis.Client
	queueName string
	closed    bool
	mu        sync.Mutex
}

// NewRedisJobQueue creates a Redis-backed JobQueue client.
func NewRedisJobQueue(cfg WorkerConfig) (*RedisJobQueue, error) {
	var client *redis.Client

	if cfg.RedisURL != "" {
		opt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("parse redis url: %w", err)
		}
		client = redis.NewClient(opt)
	} else if cfg.RedisAddr != "" {
		client = redis.NewClient(&redis.Options{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
		})
	} else {
		client = redis.NewClient(&redis.Options{
			Addr: "127.0.0.1:6379",
			DB:   0,
		})
	}

	queueName := cfg.QueueName
	if queueName == "" {
		queueName = DefaultQueueName
	}

	return &RedisJobQueue{
		client:    client,
		queueName: queueName,
	}, nil
}

// NewRedisJobQueueWithClient wraps an existing initialized Redis client into a JobQueue.
func NewRedisJobQueueWithClient(client *redis.Client, cfg WorkerConfig) (*RedisJobQueue, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required")
	}
	queueName := cfg.QueueName
	if queueName == "" {
		queueName = DefaultQueueName
	}
	return &RedisJobQueue{
		client:    client,
		queueName: queueName,
	}, nil
}

// Receive blocks for up to 2 seconds waiting for the next job on the Redis list.
func (q *RedisJobQueue) Receive(ctx context.Context) (*AnalyzerJob, string, error) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, "", ErrQueueClosed
	}
	q.mu.Unlock()

	// BRPop with 2 second polling interval to allow graceful context cancellation
	result, err := q.client.BRPop(ctx, 2*time.Second, q.queueName).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, "", nil // Timeout, no job received in interval
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", ctx.Err()
		}
		return nil, "", fmt.Errorf("redis receive: %w", err)
	}

	if len(result) < 2 {
		return nil, "", nil
	}

	rawJSON := result[1]
	var job AnalyzerJob
	if err := json.Unmarshal([]byte(rawJSON), &job); err != nil {
		return nil, "", fmt.Errorf("unmarshal analyzer job: %w", err)
	}

	return &job, rawJSON, nil
}

func (q *RedisJobQueue) Ack(ctx context.Context, receiptHandle string) error {
	// For standard Redis List BRPOP, the item is popped on receive.
	return nil
}

func (q *RedisJobQueue) Nack(ctx context.Context, receiptHandle string, retryable bool) error {
	if !retryable || receiptHandle == "" {
		return nil
	}
	// Re-push to front of queue if transient failure occurred
	return q.client.LPush(ctx, q.queueName, receiptHandle).Err()
}

func (q *RedisJobQueue) PublishProgress(ctx context.Context, taskID string, progress TaskProgress) error {
	key := fmt.Sprintf("pdfnest:task:%s", taskID)
	data, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("marshal task progress: %w", err)
	}
	// Retain task progress for 24 hours
	return q.client.Set(ctx, key, data, 24*time.Hour).Err()
}

func (q *RedisJobQueue) PublishResult(ctx context.Context, taskID string, result *engine.CanonicalAnalysisResult) error {
	key := fmt.Sprintf("pdfnest:result:%s", taskID)
	data, err := engine.ToCanonicalJSON(result)
	if err != nil {
		return fmt.Errorf("marshal canonical result: %w", err)
	}
	// Retain analysis result for 7 days
	return q.client.Set(ctx, key, data, 7*24*time.Hour).Err()
}

// SendHeartbeat persists the worker's active presence with a crash-safe Redis TTL.
func (q *RedisJobQueue) SendHeartbeat(ctx context.Context, info WorkerInfo, ttl time.Duration) error {
	if info.WorkerID == "" {
		return fmt.Errorf("worker ID is required for heartbeat")
	}
	if ttl <= 0 {
		ttl = DefaultHeartbeatTTL
	}
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal worker heartbeat: %w", err)
	}
	key := WorkerHeartbeatKeyPrefix + info.WorkerID
	pipe := q.client.Pipeline()
	pipe.Set(ctx, key, data, ttl)
	pipe.SAdd(ctx, WorkerRegistryKey, info.WorkerID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis send heartbeat: %w", err)
	}
	return nil
}

// RemoveHeartbeat explicitly unregisters a worker upon graceful shutdown.
func (q *RedisJobQueue) RemoveHeartbeat(ctx context.Context, workerID string) error {
	if workerID == "" {
		return nil
	}
	key := WorkerHeartbeatKeyPrefix + workerID
	pipe := q.client.Pipeline()
	pipe.Del(ctx, key)
	pipe.SRem(ctx, WorkerRegistryKey, workerID)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redis remove heartbeat: %w", err)
	}
	return nil
}

// GetActiveWorkers scans the registry and returns all live, non-expired worker instances.
func (q *RedisJobQueue) GetActiveWorkers(ctx context.Context) ([]WorkerInfo, error) {
	members, err := q.client.SMembers(ctx, WorkerRegistryKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("query worker registry: %w", err)
	}

	var active []WorkerInfo
	var expired []string

	for _, member := range members {
		key := WorkerHeartbeatKeyPrefix + member
		data, err := q.client.Get(ctx, key).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				expired = append(expired, member)
			}
			continue
		}
		var info WorkerInfo
		if err := json.Unmarshal([]byte(data), &info); err == nil {
			active = append(active, info)
		}
	}

	// Lazy cleanup of expired worker IDs from the registry set
	if len(expired) > 0 {
		var ifaces []interface{}
		for _, exp := range expired {
			ifaces = append(ifaces, exp)
		}
		_ = q.client.SRem(ctx, WorkerRegistryKey, ifaces...).Err()
	}

	return active, nil
}

func (q *RedisJobQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	return q.client.Close()
}

// MemoryJobQueue is an in-memory thread-safe implementation of JobQueue for testing.
type MemoryJobQueue struct {
	jobs     chan *AnalyzerJob
	progress map[string]TaskProgress
	results  map[string]*engine.CanonicalAnalysisResult
	workers  map[string]WorkerInfo
	closed   bool
	mu       sync.Mutex
}

// NewMemoryJobQueue initializes an in-memory queue.
func NewMemoryJobQueue(bufferSize int) *MemoryJobQueue {
	if bufferSize <= 0 {
		bufferSize = 32
	}
	return &MemoryJobQueue{
		jobs:     make(chan *AnalyzerJob, bufferSize),
		progress: make(map[string]TaskProgress),
		results:  make(map[string]*engine.CanonicalAnalysisResult),
		workers:  make(map[string]WorkerInfo),
	}
}

func (q *MemoryJobQueue) Push(job *AnalyzerJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	q.jobs <- job
	return nil
}

func (q *MemoryJobQueue) Receive(ctx context.Context) (*AnalyzerJob, string, error) {
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case job, ok := <-q.jobs:
		if !ok {
			return nil, "", ErrQueueClosed
		}
		return job, job.TaskID, nil
	case <-time.After(50 * time.Millisecond):
		return nil, "", nil
	}
}

func (q *MemoryJobQueue) Ack(ctx context.Context, receiptHandle string) error {
	return nil
}

func (q *MemoryJobQueue) Nack(ctx context.Context, receiptHandle string, retryable bool) error {
	return nil
}

func (q *MemoryJobQueue) PublishProgress(ctx context.Context, taskID string, progress TaskProgress) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.progress[taskID] = progress
	return nil
}

func (q *MemoryJobQueue) PublishResult(ctx context.Context, taskID string, result *engine.CanonicalAnalysisResult) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.results[taskID] = result
	return nil
}

func (q *MemoryJobQueue) SendHeartbeat(ctx context.Context, info WorkerInfo, ttl time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	q.workers[info.WorkerID] = info
	return nil
}

func (q *MemoryJobQueue) RemoveHeartbeat(ctx context.Context, workerID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.workers, workerID)
	return nil
}

func (q *MemoryJobQueue) GetActiveWorkers(ctx context.Context) ([]WorkerInfo, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var list []WorkerInfo
	for _, w := range q.workers {
		list = append(list, w)
	}
	return list, nil
}

func (q *MemoryJobQueue) GetProgress(taskID string) (TaskProgress, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.progress[taskID]
	return p, ok
}

func (q *MemoryJobQueue) GetResult(taskID string) (*engine.CanonicalAnalysisResult, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.results[taskID]
	return r, ok
}

func (q *MemoryJobQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.jobs)
	return nil
}

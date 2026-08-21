package worker

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerHeartbeat_RegistrationAndDiscovery(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rClient.Close()

	cfg := DefaultWorkerConfig()
	cfg.WorkerID = "worker-test-alpha"
	cfg.HeartbeatInterval = 50 * time.Millisecond
	cfg.HeartbeatTTL = 500 * time.Millisecond

	queue, err := NewRedisJobQueueWithClient(rClient, cfg)
	require.NoError(t, err)
	defer queue.Close()

	// Initial check - no active workers
	workers, err := queue.GetActiveWorkers(context.Background())
	require.NoError(t, err)
	assert.Empty(t, workers)

	// Send heartbeat
	info := WorkerInfo{
		WorkerID:    cfg.WorkerID,
		Hostname:    "test-host",
		PID:         1234,
		Concurrency: 4,
		ActiveJobs:  1,
		StartedAt:   time.Now().UTC(),
		HeartbeatAt: time.Now().UTC(),
	}
	err = queue.SendHeartbeat(context.Background(), info, cfg.HeartbeatTTL)
	require.NoError(t, err)

	// Discover active worker
	workers, err = queue.GetActiveWorkers(context.Background())
	require.NoError(t, err)
	require.Len(t, workers, 1)
	assert.Equal(t, "worker-test-alpha", workers[0].WorkerID)
	assert.Equal(t, "test-host", workers[0].Hostname)
	assert.Equal(t, 1234, workers[0].PID)
	assert.Equal(t, 1, workers[0].ActiveJobs)

	// Explicit deregistration (graceful shutdown)
	err = queue.RemoveHeartbeat(context.Background(), cfg.WorkerID)
	require.NoError(t, err)

	workers, err = queue.GetActiveWorkers(context.Background())
	require.NoError(t, err)
	assert.Empty(t, workers)
}

func TestWorkerHeartbeat_TTLExpirationOnCrash(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rClient.Close()

	cfg := DefaultWorkerConfig()
	cfg.WorkerID = "worker-crashed"
	cfg.HeartbeatTTL = 100 * time.Millisecond

	queue, err := NewRedisJobQueueWithClient(rClient, cfg)
	require.NoError(t, err)
	defer queue.Close()

	info := WorkerInfo{
		WorkerID:    cfg.WorkerID,
		Hostname:    "crash-host",
		PID:         9999,
		Concurrency: 4,
		StartedAt:   time.Now().UTC(),
		HeartbeatAt: time.Now().UTC(),
	}
	err = queue.SendHeartbeat(context.Background(), info, cfg.HeartbeatTTL)
	require.NoError(t, err)

	// Active before expiration
	workers, err := queue.GetActiveWorkers(context.Background())
	require.NoError(t, err)
	require.Len(t, workers, 1)

	// Advance miniredis clock past TTL
	mr.FastForward(200 * time.Millisecond)

	// Worker should now be recognized as expired and pruned from registry
	workers, err = queue.GetActiveWorkers(context.Background())
	require.NoError(t, err)
	assert.Empty(t, workers, "Expired worker heartbeat must be automatically pruned")
}

package worker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultWorkerConfig(t *testing.T) {
	cfg := DefaultWorkerConfig()
	assert.Equal(t, DefaultQueueName, cfg.QueueName)
	assert.Equal(t, 4, cfg.Concurrency)
	assert.Equal(t, 120*time.Second, cfg.JobTimeout)
	assert.Equal(t, 15*time.Second, cfg.ShutdownTimeout)
	assert.NotEmpty(t, cfg.SandboxBaseDir)

	require.NoError(t, cfg.Validate())
}

func TestWorkerConfigValidationBounds(t *testing.T) {
	cfg := WorkerConfig{
		Concurrency: -1,
		JobTimeout:  0,
	}
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 4, cfg.Concurrency)
	assert.Equal(t, 120*time.Second, cfg.JobTimeout)

	cfg.Concurrency = 100
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 16, cfg.Concurrency, "Concurrency ceiling is capped at 16")
}

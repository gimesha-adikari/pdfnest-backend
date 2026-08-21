package worker

import (
	"errors"
	"os"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/exclusion"
)

// Standard operational defaults for the Go Analyzer Worker.
const (
	DefaultQueueName                = "pdfnest:analyzer:jobs"
	DefaultConcurrency              = 4
	DefaultJobTimeout               = 120 * time.Second
	DefaultShutdownTimeout          = 15 * time.Second
	DefaultHeartbeatInterval        = 5 * time.Second
	DefaultHeartbeatTTL             = 15 * time.Second
	DefaultWatchdogInterval         = 5 * time.Second
	DefaultWorkerUnavailableTimeout = 20 * time.Second
	DefaultMaxQueueWaitTimeout      = 10 * time.Minute
	WorkerHeartbeatKeyPrefix        = "pdfnest:analyzer:worker:heartbeat:"
	WorkerRegistryKey               = "pdfnest:analyzer:worker:registry"
	JobVersion1                     = "1.0.0"
)

var (
	// ErrInvalidJob is returned when an analyzer job has missing or malformed fields.
	ErrInvalidJob = errors.New("worker: invalid or malformed analyzer job")

	// ErrUnsupportedOperation is returned when a requested feature (such as Deep AST or AI) is not supported in Phase 4B.
	ErrUnsupportedOperation = errors.New("worker: requested operation is unsupported in this execution tier")

	// ErrQueueClosed is returned when an operation is attempted on a closed queue.
	ErrQueueClosed = errors.New("worker: job queue is closed")

	// ErrWorkerStopped is returned when the worker daemon is shutting down.
	ErrWorkerStopped = errors.New("worker: analyzer worker has stopped")
)

// TaskStatus represents the lifecycle state of an analysis job.
type TaskStatus string

const (
	StatusQueued     TaskStatus = "QUEUED"
	StatusAcquiring  TaskStatus = "ACQUIRING"
	StatusInventory  TaskStatus = "INVENTORY"
	StatusAnalyzing  TaskStatus = "ANALYZING"
	StatusFinalizing TaskStatus = "FINALIZING"
	StatusCompleted  TaskStatus = "COMPLETED"
	StatusFailed     TaskStatus = "FAILED"
)

// WorkerInfo captures the real-time registration and telemetry of an active analyzer worker.
type WorkerInfo struct {
	WorkerID    string    `json:"workerId"`
	Hostname    string    `json:"hostname"`
	PID         int       `json:"pid"`
	Concurrency int       `json:"concurrency"`
	ActiveJobs  int       `json:"activeJobs"`
	StartedAt   time.Time `json:"startedAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
}

// ScopeConfig encapsulates user-defined exclusion and inclusion parameters.
type ScopeConfig struct {
	CustomPatterns []string `json:"customPatterns,omitempty"`
	EnabledPresets []string `json:"enabledPresets,omitempty"`
	ForceIncludes  []string `json:"forceIncludes,omitempty"`
	GitignoreRules []string `json:"gitignoreRules,omitempty"`
}

// ToExclusionConfig maps ScopeConfig to the Phase 2 exclusion.Config.
func (s ScopeConfig) ToExclusionConfig() exclusion.Config {
	return exclusion.Config{
		CustomPatterns: s.CustomPatterns,
		EnabledPresets: s.EnabledPresets,
		ForceIncludes:  s.ForceIncludes,
		GitignoreRules: s.GitignoreRules,
	}
}

// AnalyzerJob defines the strongly-typed internal job payload consumed by the analyzer worker.
type AnalyzerJob struct {
	JobVersion         string            `json:"jobVersion"`
	TaskID             string            `json:"taskId"`
	SessionID          string            `json:"sessionId"`
	SourceType         engine.SourceType `json:"sourceType"`
	GitURL             string            `json:"gitUrl,omitempty"`
	StagedArchivePath  string            `json:"stagedArchivePath,omitempty"`
	Scope              ScopeConfig       `json:"scope"`
	SelectedDomains    []string          `json:"selectedDomains,omitempty"`
	DeepAst            bool              `json:"deepAst,omitempty"`
	EnableAi           bool              `json:"enableAi,omitempty"`
	SourceArtifactHash string            `json:"sourceArtifactHash,omitempty"`
}

// TaskProgress records the real-time execution state of an ongoing analysis with explicit lifecycle timestamps.
type TaskProgress struct {
	TaskID          string                          `json:"taskId"`
	SessionID       string                          `json:"sessionId"`
	Status          TaskStatus                      `json:"status"`
	ProgressPercent int                             `json:"progressPercent"`
	StageMessage    string                          `json:"stageMessage"`
	ErrorMessage    string                          `json:"errorMessage,omitempty"`
	Result          *engine.CanonicalAnalysisResult `json:"result,omitempty"`
	WorkerID        string                          `json:"workerId,omitempty"`
	QueuedAt        *time.Time                      `json:"queuedAt,omitempty"`
	ClaimedAt       *time.Time                      `json:"claimedAt,omitempty"`
	StartedAt       *time.Time                      `json:"startedAt,omitempty"`
	CompletedAt     *time.Time                      `json:"completedAt,omitempty"`
	FailedAt        *time.Time                      `json:"failedAt,omitempty"`
	UpdatedAt       time.Time                       `json:"updatedAt"`
}

// WorkerConfig encapsulates the runtime settings for the Go Analyzer Worker daemon.
type WorkerConfig struct {
	WorkerID          string
	RedisURL          string
	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	QueueName         string
	Concurrency       int
	JobTimeout        time.Duration
	ShutdownTimeout   time.Duration
	SandboxBaseDir    string
	HeartbeatInterval time.Duration
	HeartbeatTTL      time.Duration
}

// DefaultWorkerConfig returns production-safe configuration defaults.
func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{
		QueueName:         DefaultQueueName,
		Concurrency:       DefaultConcurrency,
		JobTimeout:        DefaultJobTimeout,
		ShutdownTimeout:   DefaultShutdownTimeout,
		SandboxBaseDir:    os.TempDir(),
		HeartbeatInterval: DefaultHeartbeatInterval,
		HeartbeatTTL:      DefaultHeartbeatTTL,
	}
}

// Validate ensures all configuration fields adhere to operational constraints.
func (c *WorkerConfig) Validate() error {
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Concurrency > 16 {
		c.Concurrency = 16
	}
	if c.JobTimeout <= 0 {
		c.JobTimeout = DefaultJobTimeout
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	if c.QueueName == "" {
		c.QueueName = DefaultQueueName
	}
	if c.SandboxBaseDir == "" {
		c.SandboxBaseDir = os.TempDir()
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.HeartbeatTTL <= 0 {
		c.HeartbeatTTL = DefaultHeartbeatTTL
	}
	return nil
}

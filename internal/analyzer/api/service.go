package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/acquisition"
	"pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/analyzer/worker"
)

var (
	ErrSessionNotFound     = errors.New("analyzer: session not found or access denied")
	ErrTaskNotFound        = errors.New("analyzer: task not found or access denied")
	ErrAnalysisRunning     = errors.New("analyzer: analysis is currently in progress")
	ErrAnalysisFailed      = errors.New("analyzer: analysis task failed")
	ErrResultNotReady      = errors.New("analyzer: analysis result is not ready")
	ErrInvalidStorageKey   = errors.New("analyzer: invalid storage key")
	ErrUnsupportedFeatures = errors.New("analyzer: deep AST and AI features are not supported in Phase 5")
)

// Service defines the business contract for analyzer session and task lifecycle management.
type Service struct {
	db                       *gorm.DB
	redis                    *redis.Client
	queueName                string
	workerUnavailableTimeout time.Duration
	maxQueueWaitTimeout      time.Duration
	watchdogInterval         time.Duration
	scopeAdapter             *ScopeConfigAdapter
	progressAdapter          *TaskProgressAdapter
}

// NewService instantiates an Analyzer Service.
func NewService(db *gorm.DB, redisClient *redis.Client, queueName string) *Service {
	if queueName == "" {
		queueName = worker.DefaultQueueName
	}
	return &Service{
		db:                       db,
		redis:                    redisClient,
		queueName:                queueName,
		workerUnavailableTimeout: worker.DefaultWorkerUnavailableTimeout,
		maxQueueWaitTimeout:      worker.DefaultMaxQueueWaitTimeout,
		watchdogInterval:         worker.DefaultWatchdogInterval,
		scopeAdapter:             NewScopeConfigAdapter(),
		progressAdapter:          NewTaskProgressAdapter(),
	}
}

// CreateSession establishes a new analyzer session with strict ownership and validation.
func (s *Service) CreateSession(ctx context.Context, ownerIdentity string, req CreateSessionRequest) (*SessionResponse, error) {
	if ownerIdentity == "" {
		return nil, fmt.Errorf("owner identity is required")
	}

	sessionID := uuid.NewString()
	repoName := strings.TrimSpace(req.RepositoryName)

	var gitURLPtr *string
	var storageKeyPtr *string

	switch req.SourceType {
	case engine.SourceTypeGit:
		if req.GitURL == "" {
			return nil, fmt.Errorf("gitUrl is required for git source type")
		}
		if err := acquisition.ValidateGitURL(req.GitURL); err != nil {
			return nil, fmt.Errorf("git url validation failed: %w", err)
		}
		cleanURL := strings.TrimSpace(req.GitURL)
		gitURLPtr = &cleanURL
		if repoName == "" {
			base := path.Base(cleanURL)
			repoName = strings.TrimSuffix(base, ".git")
		}

	case engine.SourceTypeZip:
		if req.StorageKey == "" {
			return nil, fmt.Errorf("storageKey is required for zip source type")
		}
		cleanKey := strings.TrimSpace(req.StorageKey)
		if strings.Contains(cleanKey, "..") || strings.HasPrefix(cleanKey, "/") {
			return nil, fmt.Errorf("%w: path traversal or absolute path detected", ErrInvalidStorageKey)
		}
		storageKeyPtr = &cleanKey
		if repoName == "" {
			base := path.Base(cleanKey)
			repoName = strings.TrimSuffix(base, ".zip")
		}

	case engine.SourceTypeLocalFolder:
		if repoName == "" {
			repoName = "local-repository"
		}

	default:
		return nil, fmt.Errorf("unsupported sourceType '%s'", req.SourceType)
	}

	if repoName == "" {
		repoName = "repository"
	}

	defaultScope := worker.ScopeConfig{}
	scopeJSON, _ := json.Marshal(defaultScope)

	session := models.AnalyzerSession{
		ID:                  sessionID,
		OwnerIdentity:       ownerIdentity,
		SourceType:          string(req.SourceType),
		GitURL:              gitURLPtr,
		StorageKey:          storageKeyPtr,
		RepositoryName:      repoName,
		ScopeJSON:           string(scopeJSON),
		SelectedDomainsJSON: "[]",
		Status:              "CREATED",
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}

	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return nil, fmt.Errorf("database create session: %w", err)
	}

	return &SessionResponse{
		SessionID:      session.ID,
		SourceType:     req.SourceType,
		GitURL:         req.GitURL,
		StorageKey:     req.StorageKey,
		RepositoryName: session.RepositoryName,
		Status:         session.Status,
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
	}, nil
}

// GetSession retrieves an analyzer session enforcing strict ownership (WHERE id = ? AND owner_identity = ?).
func (s *Service) GetSession(ctx context.Context, ownerIdentity, sessionID string) (*models.AnalyzerSession, error) {
	if ownerIdentity == "" || sessionID == "" {
		return nil, ErrSessionNotFound
	}

	var session models.AnalyzerSession
	err := s.db.WithContext(ctx).
		Where("id = ? AND owner_identity = ?", sessionID, ownerIdentity).
		First(&session).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("database get session: %w", err)
	}

	return &session, nil
}

// GetTree returns cached inventory tree metadata for a session.
func (s *Service) GetTree(ctx context.Context, ownerIdentity, sessionID string) (*TreeResponse, error) {
	session, err := s.GetSession(ctx, ownerIdentity, sessionID)
	if err != nil {
		return nil, err
	}

	// Try reading cached tree from Redis
	treeKey := fmt.Sprintf("pdfnest:analyzer:tree:%s", session.ID)
	val, err := s.redis.Get(ctx, treeKey).Result()
	if err == nil && val != "" {
		var treeResp TreeResponse
		if err := json.Unmarshal([]byte(val), &treeResp); err == nil {
			return &treeResp, nil
		}
	}

	// Fallback empty tree response
	return &TreeResponse{
		SessionID:     session.ID,
		TotalFiles:    0,
		IncludedFiles: 0,
		ExcludedFiles: 0,
		ScopeHash:     "",
		Files:         []TreeNodeDTO{},
	}, nil
}

// UpdateScope validates and stores custom exclusion and domain preferences.
func (s *Service) UpdateScope(ctx context.Context, ownerIdentity, sessionID string, req UpdateScopeRequest) (*ScopeResponse, error) {
	session, err := s.GetSession(ctx, ownerIdentity, sessionID)
	if err != nil {
		return nil, err
	}

	scopeConfig, scopeHash, err := s.scopeAdapter.AdaptAndValidate(req)
	if err != nil {
		return nil, fmt.Errorf("validate scope: %w", err)
	}

	scopeJSON, err := json.Marshal(scopeConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal scope: %w", err)
	}

	domainsJSON, err := json.Marshal(req.SelectedDomains)
	if err != nil {
		return nil, fmt.Errorf("marshal domains: %w", err)
	}

	err = s.db.WithContext(ctx).Model(session).Updates(map[string]interface{}{
		"scope_json":            string(scopeJSON),
		"selected_domains_json": string(domainsJSON),
		"updated_at":            time.Now().UTC(),
	}).Error

	if err != nil {
		return nil, fmt.Errorf("database update scope: %w", err)
	}

	return &ScopeResponse{
		CustomPatterns:  scopeConfig.CustomPatterns,
		EnabledPresets:  scopeConfig.EnabledPresets,
		ForceIncludes:   scopeConfig.ForceIncludes,
		GitignoreRules:  scopeConfig.GitignoreRules,
		SelectedDomains: req.SelectedDomains,
		ScopeHash:       scopeHash,
	}, nil
}

// Analyze submits an analysis job to the Redis queue with complete idempotency and ownership protection.
func (s *Service) Analyze(ctx context.Context, ownerIdentity, sessionID string, req AnalyzeRequest) (*AnalyzeResponse, error) {
	session, err := s.GetSession(ctx, ownerIdentity, sessionID)
	if err != nil {
		return nil, err
	}

	// Idempotency check: if an active task exists, return existing task ID
	if session.CurrentTaskID != nil && *session.CurrentTaskID != "" {
		taskID := *session.CurrentTaskID
		taskKey := fmt.Sprintf("pdfnest:task:%s", taskID)
		taskJSON, err := s.redis.Get(ctx, taskKey).Result()
		if err == nil && taskJSON != "" {
			var progress worker.TaskProgress
			if err := json.Unmarshal([]byte(taskJSON), &progress); err == nil {
				switch progress.Status {
				case worker.StatusQueued, worker.StatusAcquiring, worker.StatusInventory,
					worker.StatusAnalyzing, worker.StatusFinalizing:
					return &AnalyzeResponse{
						TaskID:    taskID,
						SessionID: session.ID,
						Status:    string(progress.Status),
						Message:   "Analysis is already in progress for this session",
					}, nil
				}
			}
		}
	}

	taskID := uuid.NewString()

	var scopeConfig worker.ScopeConfig
	if session.ScopeJSON != "" {
		_ = json.Unmarshal([]byte(session.ScopeJSON), &scopeConfig)
	}

	var selectedDomains []string
	if len(req.SelectedDomains) > 0 {
		selectedDomains = req.SelectedDomains
	} else if session.SelectedDomainsJSON != "" {
		_ = json.Unmarshal([]byte(session.SelectedDomainsJSON), &selectedDomains)
	}

	gitURL := ""
	if session.GitURL != nil {
		gitURL = *session.GitURL
	}
	stagedPath := ""
	if session.StorageKey != nil {
		stagedPath = *session.StorageKey
	}

	job := worker.AnalyzerJob{
		JobVersion:        worker.JobVersion1,
		TaskID:            taskID,
		SessionID:         session.ID,
		SourceType:        engine.SourceType(session.SourceType),
		GitURL:            gitURL,
		StagedArchivePath: stagedPath,
		Scope:             scopeConfig,
		SelectedDomains:   selectedDomains,
		DeepAst:           req.DeepAst,
		EnableAi:          req.EnableAi,
	}

	jobJSON, err := json.Marshal(job)
	if err != nil {
		return nil, fmt.Errorf("marshal analyzer job: %w", err)
	}

	// 1. Initialize Task State in Redis
	now := time.Now().UTC()
	initialProgress := worker.TaskProgress{
		TaskID:          taskID,
		SessionID:       session.ID,
		Status:          worker.StatusQueued,
		ProgressPercent: 0,
		StageMessage:    "Analysis task queued",
		QueuedAt:        &now,
		UpdatedAt:       now,
	}
	progJSON, _ := json.Marshal(initialProgress)
	taskKey := fmt.Sprintf("pdfnest:task:%s", taskID)
	if err := s.redis.Set(ctx, taskKey, progJSON, 24*time.Hour).Err(); err != nil {
		return nil, fmt.Errorf("redis set task progress: %w", err)
	}

	// 2. Enqueue to Redis Job List
	if err := s.redis.LPush(ctx, s.queueName, string(jobJSON)).Err(); err != nil {
		return nil, fmt.Errorf("redis enqueue job: %w", err)
	}

	// 3. Update PostgreSQL Session State
	err = s.db.WithContext(ctx).Model(session).Updates(map[string]interface{}{
		"current_task_id": taskID,
		"status":          "QUEUED",
		"updated_at":      time.Now().UTC(),
	}).Error

	if err != nil {
		return nil, fmt.Errorf("database update session task: %w", err)
	}

	return &AnalyzeResponse{
		TaskID:    taskID,
		SessionID: session.ID,
		Status:    "QUEUED",
		Message:   "Analysis task successfully queued",
	}, nil
}

// GetResult retrieves and validates the canonical analysis result.
func (s *Service) GetResult(ctx context.Context, ownerIdentity, sessionID string) (*engine.CanonicalAnalysisResult, error) {
	session, err := s.GetSession(ctx, ownerIdentity, sessionID)
	if err != nil {
		return nil, err
	}

	if session.CurrentTaskID == nil || *session.CurrentTaskID == "" {
		return nil, ErrResultNotReady
	}

	taskID := *session.CurrentTaskID
	resultKey := fmt.Sprintf("pdfnest:result:%s", taskID)
	rawResult, err := s.redis.Get(ctx, resultKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			// Check if task failed
			taskKey := fmt.Sprintf("pdfnest:task:%s", taskID)
			progJSON, progErr := s.redis.Get(ctx, taskKey).Result()
			if progErr == nil && progJSON != "" {
				var p worker.TaskProgress
				if json.Unmarshal([]byte(progJSON), &p) == nil {
					if p.Status == worker.StatusFailed {
						return nil, fmt.Errorf("%w: %s", ErrAnalysisFailed, p.ErrorMessage)
					}
					return nil, ErrAnalysisRunning
				}
			}
			return nil, ErrResultNotReady
		}
		return nil, fmt.Errorf("redis get result: %w", err)
	}

	result, err := engine.FromCanonicalJSON([]byte(rawResult))
	if err != nil {
		return nil, fmt.Errorf("parse canonical result: %w", err)
	}

	if err := engine.ValidateCanonicalResult(result); err != nil {
		return nil, fmt.Errorf("validate canonical result: %w", err)
	}

	return result, nil
}

// GetTaskStatus retrieves task progress verifying ownership via the associated session.
func (s *Service) GetTaskStatus(ctx context.Context, ownerIdentity, taskID string) (*TaskStatusResponse, error) {
	if ownerIdentity == "" || taskID == "" {
		return nil, ErrTaskNotFound
	}

	// Verify ownership through PostgreSQL session
	var session models.AnalyzerSession
	err := s.db.WithContext(ctx).
		Where("current_task_id = ? AND owner_identity = ?", taskID, ownerIdentity).
		First(&session).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("database query task session: %w", err)
	}

	taskKey := fmt.Sprintf("pdfnest:task:%s", taskID)
	rawProgress, err := s.redis.Get(ctx, taskKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("redis get task progress: %w", err)
	}

	var progress worker.TaskProgress
	if err := json.Unmarshal([]byte(rawProgress), &progress); err != nil {
		return nil, fmt.Errorf("unmarshal task progress: %w", err)
	}

	// Defensive check: if progress is still QUEUED and no workers are registered and threshold exceeded, reconcile
	if progress.Status == worker.StatusQueued {
		queuedAt := progress.QueuedAt
		if queuedAt == nil {
			queuedAt = &session.UpdatedAt
		}
		if time.Since(*queuedAt) > s.workerUnavailableTimeout {
			activeWorkers, _ := s.GetActiveWorkers(ctx)
			if len(activeWorkers) == 0 {
				now := time.Now().UTC()
				s.markTaskFailed(ctx, session.ID, taskID,
					"Repository analysis service is currently unavailable. No active analyzer workers are running.",
					now)
				progress.Status = worker.StatusFailed
				progress.ProgressPercent = 100
				progress.StageMessage = "Analysis failed"
				progress.ErrorMessage = "Repository analysis service is currently unavailable. No active analyzer workers are running."
				progress.UpdatedAt = now
			}
		}
	}

	resp := s.progressAdapter.Adapt(progress)
	return &resp, nil
}

// SetTimeouts configures custom watchdog and timeout durations (useful for tests and tuning).
func (s *Service) SetTimeouts(workerUnavailable, maxQueueWait, watchdog time.Duration) {
	if workerUnavailable > 0 {
		s.workerUnavailableTimeout = workerUnavailable
	}
	if maxQueueWait > 0 {
		s.maxQueueWaitTimeout = maxQueueWait
	}
	if watchdog > 0 {
		s.watchdogInterval = watchdog
	}
}

// GetActiveWorkers returns the slice of non-expired worker registrations.
func (s *Service) GetActiveWorkers(ctx context.Context) ([]worker.WorkerInfo, error) {
	members, err := s.redis.SMembers(ctx, worker.WorkerRegistryKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("query worker registry: %w", err)
	}

	var active []worker.WorkerInfo
	var expired []string

	for _, member := range members {
		key := worker.WorkerHeartbeatKeyPrefix + member
		data, err := s.redis.Get(ctx, key).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				expired = append(expired, member)
			}
			continue
		}
		var info worker.WorkerInfo
		if err := json.Unmarshal([]byte(data), &info); err == nil {
			active = append(active, info)
		}
	}

	if len(expired) > 0 {
		var ifaces []interface{}
		for _, exp := range expired {
			ifaces = append(ifaces, exp)
		}
		_ = s.redis.SRem(ctx, worker.WorkerRegistryKey, ifaces...).Err()
	}

	return active, nil
}

// CheckReadiness inspects Redis, queue, and worker readiness.
func (s *Service) CheckReadiness(ctx context.Context) SubsystemReadiness {
	readiness := SubsystemReadiness{
		RedisReady:  false,
		QueueReady:  false,
		WorkerReady: false,
		IsReady:     false,
	}

	// 1. Check Redis connectivity
	if err := s.redis.Ping(ctx).Err(); err != nil {
		readiness.Message = fmt.Sprintf("Redis connection failure: %v", err)
		return readiness
	}
	readiness.RedisReady = true
	readiness.QueueReady = true

	// 2. Query active workers
	workers, err := s.GetActiveWorkers(ctx)
	if err != nil {
		readiness.Message = fmt.Sprintf("Failed to query active workers: %v", err)
		return readiness
	}

	readiness.Workers = workers
	readiness.ActiveWorkers = len(workers)
	readiness.WorkerReady = len(workers) > 0

	if readiness.WorkerReady {
		readiness.IsReady = true
		readiness.Message = fmt.Sprintf("Analyzer subsystem ready with %d active worker(s)", len(workers))
	} else {
		readiness.Message = "Analyzer subsystem degraded: no active analyzer workers registered"
	}

	return readiness
}

// StartWatchdog starts a background reconciler that periodically sweeps stale or orphaned tasks.
func (s *Service) StartWatchdog(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.watchdogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.ReconcileStaleTasks(ctx)
			}
		}
	}()
}

// ReconcileStaleTasks sweeps tasks in QUEUED or PROCESSING states against worker availability and timeouts.
func (s *Service) ReconcileStaleTasks(ctx context.Context) {
	activeWorkers, err := s.GetActiveWorkers(ctx)
	if err != nil {
		return
	}

	activeWorkerMap := make(map[string]bool)
	for _, w := range activeWorkers {
		activeWorkerMap[w.WorkerID] = true
	}

	var sessions []models.AnalyzerSession
	err = s.db.WithContext(ctx).
		Where("status IN ('QUEUED', 'PROCESSING')").
		Find(&sessions).Error
	if err != nil {
		return
	}

	now := time.Now().UTC()

	for _, session := range sessions {
		if session.CurrentTaskID == nil || *session.CurrentTaskID == "" {
			continue
		}
		taskID := *session.CurrentTaskID
		taskKey := fmt.Sprintf("pdfnest:task:%s", taskID)

		rawProgress, err := s.redis.Get(ctx, taskKey).Result()
		if err != nil {
			continue
		}

		var progress worker.TaskProgress
		if err := json.Unmarshal([]byte(rawProgress), &progress); err != nil {
			continue
		}

		// Handle QUEUED tasks
		if progress.Status == worker.StatusQueued {
			queuedAt := progress.QueuedAt
			if queuedAt == nil {
				queuedAt = &session.UpdatedAt
			}

			// Case A: No workers exist and task waited beyond WorkerUnavailableTimeout
			if len(activeWorkers) == 0 && now.Sub(*queuedAt) > s.workerUnavailableTimeout {
				s.markTaskFailed(ctx, session.ID, taskID,
					"Repository analysis service is currently unavailable. No active analyzer workers are running.",
					now)
				continue
			}

			// Case B: Workers exist, but task has exceeded MaxQueueWaitTimeout
			if now.Sub(*queuedAt) > s.maxQueueWaitTimeout {
				s.markTaskFailed(ctx, session.ID, taskID,
					"Analysis task exceeded maximum queue wait timeout (10 minutes).",
					now)
				continue
			}
		}

		// Handle PROCESSING / ACQUIRING / INVENTORY tasks
		if progress.Status != worker.StatusQueued && progress.Status != worker.StatusCompleted && progress.Status != worker.StatusFailed {
			if progress.WorkerID != "" && !activeWorkerMap[progress.WorkerID] {
				// Worker that claimed the job has disappeared from registry
				if now.Sub(progress.UpdatedAt) > 30*time.Second {
					s.markTaskFailed(ctx, session.ID, taskID,
						"Analyzer worker processing this repository terminated unexpectedly.",
						now)
				}
			}
		}
	}
}

func (s *Service) markTaskFailed(ctx context.Context, sessionID, taskID, errorMsg string, failedAt time.Time) {
	// 1. Update PostgreSQL
	_ = s.db.WithContext(ctx).Model(&models.AnalyzerSession{}).
		Where("id = ?", sessionID).
		Updates(map[string]interface{}{
			"status":     "FAILED",
			"updated_at": failedAt,
		}).Error

	// 2. Update Redis Task Progress
	taskKey := fmt.Sprintf("pdfnest:task:%s", taskID)
	failedProgress := worker.TaskProgress{
		TaskID:          taskID,
		SessionID:       sessionID,
		Status:          worker.StatusFailed,
		ProgressPercent: 100,
		StageMessage:    "Analysis failed",
		ErrorMessage:    errorMsg,
		FailedAt:        &failedAt,
		UpdatedAt:       failedAt,
	}
	progJSON, _ := json.Marshal(failedProgress)
	_ = s.redis.Set(ctx, taskKey, progJSON, 24*time.Hour).Err()

	// 3. Broadcast WebSocket notification if subscribers exist
	wsChan := fmt.Sprintf("pdfnest:progress:%s", taskID)
	_ = s.redis.Publish(ctx, wsChan, progJSON).Err()
}

package api

import (
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/worker"
)

// Standard error codes for Analyzer API
const (
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
	ErrCodeUnauthorized       = "UNAUTHORIZED"
	ErrCodeForbidden          = "FORBIDDEN"
	ErrCodeNotFound           = "NOT_FOUND"
	ErrCodeConflict           = "CONFLICT"
	ErrCodeUnprocessable      = "UNPROCESSABLE_ENTITY"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInternalError      = "INTERNAL_ERROR"
)

// APIError standardizes error responses across Fiber handlers.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CreateSessionRequest defines the payload for initializing an analyzer session.
type CreateSessionRequest struct {
	SourceType     engine.SourceType `json:"sourceType"`
	GitURL         string            `json:"gitUrl,omitempty"`
	StorageKey     string            `json:"storageKey,omitempty"`
	RepositoryName string            `json:"repositoryName,omitempty"`
}

// SessionResponse represents the public summary of an analyzer session.
type SessionResponse struct {
	SessionID      string            `json:"sessionId"`
	SourceType     engine.SourceType `json:"sourceType"`
	GitURL         string            `json:"gitUrl,omitempty"`
	StorageKey     string            `json:"storageKey,omitempty"`
	RepositoryName string            `json:"repositoryName"`
	Status         string            `json:"status"`
	CurrentTaskID  string            `json:"currentTaskId,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// UpdateScopeRequest defines custom scoping preferences sent by the frontend.
type UpdateScopeRequest struct {
	CustomPatterns  []string `json:"customPatterns,omitempty"`
	EnabledPresets  []string `json:"enabledPresets,omitempty"`
	ForceIncludes   []string `json:"forceIncludes,omitempty"`
	GitignoreRules  []string `json:"gitignoreRules,omitempty"`
	SelectedDomains []string `json:"selectedDomains,omitempty"`
}

// ScopeResponse returns the validated scope configuration and its deterministic hash.
type ScopeResponse struct {
	CustomPatterns  []string `json:"customPatterns"`
	EnabledPresets  []string `json:"enabledPresets"`
	ForceIncludes   []string `json:"forceIncludes"`
	GitignoreRules  []string `json:"gitignoreRules"`
	SelectedDomains []string `json:"selectedDomains"`
	ScopeHash       string   `json:"scopeHash"`
}

// TreeNodeDTO represents a single entry in the repository tree for frontend rendering.
type TreeNodeDTO struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Category    string `json:"category"`
	Language    string `json:"language,omitempty"`
	IsExcluded  bool   `json:"isExcluded"`
	Reason      string `json:"reason,omitempty"`
	IsDirectory bool   `json:"isDirectory"`
}

// TreeResponse summarizes the repository inventory tree nodes and scoping metrics.
type TreeResponse struct {
	SessionID     string        `json:"sessionId"`
	TotalFiles    int           `json:"totalFiles"`
	IncludedFiles int           `json:"includedFiles"`
	ExcludedFiles int           `json:"excludedFiles"`
	ScopeHash     string        `json:"scopeHash"`
	Files         []TreeNodeDTO `json:"files"`
}

// AnalyzeRequest triggers the asynchronous full repository analysis.
type AnalyzeRequest struct {
	SelectedDomains []string `json:"selectedDomains,omitempty"`
	DeepAst         bool     `json:"deepAst,omitempty"`
	EnableAi        bool     `json:"enableAi,omitempty"`
}

// AnalyzeResponse contains the dispatched task identifier.
type AnalyzeResponse struct {
	TaskID    string `json:"taskId"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

// TaskStatusResponse reports real-time execution status for a task.
type TaskStatusResponse struct {
	TaskID          string            `json:"taskId"`
	SessionID       string            `json:"sessionId"`
	Status          worker.TaskStatus `json:"status"`
	ProgressPercent int               `json:"progressPercent"`
	StageMessage    string            `json:"stageMessage"`
	ErrorMessage    string            `json:"errorMessage,omitempty"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// SubsystemReadiness reports analyzer infrastructure health and active worker count.
type SubsystemReadiness struct {
	RedisReady    bool                `json:"redisReady"`
	QueueReady    bool                `json:"queueReady"`
	WorkerReady   bool                `json:"workerReady"`
	GitReady      bool                `json:"gitReady"`
	ActiveWorkers int                 `json:"activeWorkers"`
	Workers       []worker.WorkerInfo `json:"workers,omitempty"`
	IsReady       bool                `json:"isReady"`
	Message       string              `json:"message"`
}

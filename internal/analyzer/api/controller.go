package api

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/identity"
)

// Controller provides Fiber HTTP and WebSocket handlers for the Repository Analyzer API.
type Controller struct {
	service *Service
}

// NewController instantiates the Analyzer API Controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

func (ctrl *Controller) resolveIdentityID(c *fiber.Ctx) string {
	if val, ok := c.Locals(identity.LocalIdentityIDKey).(string); ok && val != "" {
		return val
	}
	if id, ok := c.Locals(identity.LocalIdentityKey).(identity.Identity); ok && id.ID != "" {
		return id.ID
	}
	if val := c.Get(identity.HeaderFingerprint); val != "" {
		return "guest:" + strings.TrimSpace(val)
	}
	if val := c.Cookies(identity.CookieGuestID); val != "" {
		return "guest:" + strings.TrimSpace(val)
	}
	return "anonymous"
}

// CreateSession handles POST /api/v1/analyzer/sessions.
func (ctrl *Controller) CreateSession(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)

	var req CreateSessionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    ErrCodeInvalidRequest,
			Message: "Failed to parse JSON body: " + err.Error(),
		})
	}

	resp, err := ctrl.service.CreateSession(c.Context(), ownerIdentity, req)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "ssrf") ||
			strings.Contains(errStr, "protocol") ||
			strings.Contains(errStr, "blocked") ||
			strings.Contains(errStr, "forbidden") ||
			strings.Contains(errStr, "private") ||
			strings.Contains(errStr, "loopback") ||
			strings.Contains(errStr, "link-local") ||
			strings.Contains(errStr, "cloud metadata") ||
			strings.Contains(errStr, "prohibited") {
			return c.Status(fiber.StatusForbidden).JSON(APIError{
				Code:    ErrCodeForbidden,
				Message: err.Error(),
			})
		}
		if errors.Is(err, ErrInvalidStorageKey) {
			return c.Status(fiber.StatusBadRequest).JSON(APIError{
				Code:    ErrCodeInvalidRequest,
				Message: err.Error(),
			})
		}
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    ErrCodeInvalidRequest,
			Message: err.Error(),
		})
	}

	return c.Status(fiber.StatusCreated).JSON(resp)
}

// GetSession handles GET /api/v1/analyzer/sessions/:id.
func (ctrl *Controller) GetSession(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)
	sessionID := c.Params("id")

	session, err := ctrl.service.GetSession(c.Context(), ownerIdentity, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(APIError{
				Code:    ErrCodeNotFound,
				Message: "Analyzer session not found or access denied",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    ErrCodeInternalError,
			Message: err.Error(),
		})
	}

	gitURL := ""
	if session.GitURL != nil {
		gitURL = *session.GitURL
	}
	storageKey := ""
	if session.StorageKey != nil {
		storageKey = *session.StorageKey
	}
	taskID := ""
	if session.CurrentTaskID != nil {
		taskID = *session.CurrentTaskID
	}

	return c.JSON(SessionResponse{
		SessionID:      session.ID,
		SourceType:     engine.SourceType(session.SourceType),
		GitURL:         gitURL,
		StorageKey:     storageKey,
		RepositoryName: session.RepositoryName,
		Status:         session.Status,
		CurrentTaskID:  taskID,
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
	})
}

// GetTree handles GET /api/v1/analyzer/sessions/:id/tree.
func (ctrl *Controller) GetTree(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)
	sessionID := c.Params("id")

	resp, err := ctrl.service.GetTree(c.Context(), ownerIdentity, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(APIError{
				Code:    ErrCodeNotFound,
				Message: "Analyzer session not found or access denied",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    ErrCodeInternalError,
			Message: err.Error(),
		})
	}

	return c.JSON(resp)
}

// UpdateScope handles PUT /api/v1/analyzer/sessions/:id/scope.
func (ctrl *Controller) UpdateScope(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)
	sessionID := c.Params("id")

	var req UpdateScopeRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    ErrCodeInvalidRequest,
			Message: "Failed to parse JSON body: " + err.Error(),
		})
	}

	resp, err := ctrl.service.UpdateScope(c.Context(), ownerIdentity, sessionID, req)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(APIError{
				Code:    ErrCodeNotFound,
				Message: "Analyzer session not found or access denied",
			})
		}
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    ErrCodeInvalidRequest,
			Message: err.Error(),
		})
	}

	return c.JSON(resp)
}

// Analyze handles POST /api/v1/analyzer/sessions/:id/analyze.
func (ctrl *Controller) Analyze(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)
	sessionID := c.Params("id")

	var req AnalyzeRequest
	_ = c.BodyParser(&req) // Optional overrides

	resp, err := ctrl.service.Analyze(c.Context(), ownerIdentity, sessionID, req)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(APIError{
				Code:    ErrCodeNotFound,
				Message: "Analyzer session not found or access denied",
			})
		}
		if errors.Is(err, ErrUnsupportedFeatures) {
			return c.Status(fiber.StatusBadRequest).JSON(APIError{
				Code:    ErrCodeInvalidRequest,
				Message: err.Error(),
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    ErrCodeInternalError,
			Message: err.Error(),
		})
	}

	return c.Status(fiber.StatusAccepted).JSON(resp)
}

// GetResult handles GET /api/v1/analyzer/sessions/:id/result.
func (ctrl *Controller) GetResult(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)
	sessionID := c.Params("id")

	res, err := ctrl.service.GetResult(c.Context(), ownerIdentity, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(APIError{
				Code:    ErrCodeNotFound,
				Message: "Analyzer session not found or access denied",
			})
		}
		if errors.Is(err, ErrResultNotReady) || errors.Is(err, ErrAnalysisRunning) {
			return c.Status(fiber.StatusConflict).JSON(APIError{
				Code:    ErrCodeConflict,
				Message: "Analysis result is not ready or currently processing",
			})
		}
		if errors.Is(err, ErrAnalysisFailed) {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(APIError{
				Code:    ErrCodeUnprocessable,
				Message: err.Error(),
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    ErrCodeInternalError,
			Message: err.Error(),
		})
	}

	return c.JSON(res)
}

// GetTaskStatus handles GET /api/v1/tasks/:id.
func (ctrl *Controller) GetTaskStatus(c *fiber.Ctx) error {
	ownerIdentity := ctrl.resolveIdentityID(c)
	taskID := c.Params("id")

	resp, err := ctrl.service.GetTaskStatus(c.Context(), ownerIdentity, taskID)
	if err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(APIError{
				Code:    ErrCodeNotFound,
				Message: "Task not found or access denied",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    ErrCodeInternalError,
			Message: err.Error(),
		})
	}

	return c.JSON(resp)
}

// HandleWebSocketProgress manages real-time WebSocket progress broadcasts for /api/v1/tasks/:id/progress.
func (ctrl *Controller) HandleWebSocketProgress(c *websocket.Conn) {
	taskID := c.Params("id")
	if taskID == "" {
		_ = c.Close()
		return
	}

	// Resolve identity from connection locals/query
	identityID := ""
	if val, ok := c.Locals(identity.LocalIdentityIDKey).(string); ok && val != "" {
		identityID = val
	} else if val := c.Query(identity.HeaderFingerprint); val != "" {
		identityID = "guest:" + strings.TrimSpace(val)
	} else if val := c.Cookies(identity.CookieGuestID); val != "" {
		identityID = "guest:" + strings.TrimSpace(val)
	} else {
		identityID = "anonymous"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, err := ctrl.service.GetTaskStatus(ctx, identityID, taskID)
			if err != nil {
				if errors.Is(err, ErrTaskNotFound) {
					// Unauthorized or non-existent task
					_ = c.WriteJSON(APIError{
						Code:    ErrCodeNotFound,
						Message: "Task not found or access denied",
					})
					_ = c.Close()
					return
				}
				continue
			}

			if err := c.WriteJSON(status); err != nil {
				log.Printf("[Analyzer WebSocket] Client disconnect on task %s: %v", taskID, err)
				return
			}

			if status.Status == "COMPLETED" || status.Status == "FAILED" {
				return
			}
		}
	}
}

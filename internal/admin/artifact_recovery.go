package admin

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"pdfnest-backend/internal/tasks"

	"github.com/gofiber/fiber/v2"
)

func (ctrl *Controller) ListArtifactRecoveryHolds(c *fiber.Ctx) error {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid recovery case limit"})
		}
		limit = parsed
	}
	var before time.Time
	if raw := c.Query("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid recovery case cursor"})
		}
		before = parsed
	}
	items, err := tasks.ListArtifactRecoveryHolds(c.UserContext(), limit, before)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Artifact recovery service is unavailable"})
	}
	return c.JSON(fiber.Map{"cases": items})
}

func (ctrl *Controller) GetArtifactRecoveryHold(c *fiber.Ctx) error {
	item, err := tasks.GetArtifactRecoveryHold(c.UserContext(), c.Params("id"))
	if err != nil {
		if errors.Is(err, tasks.ErrRecoveryCaseNotFound) {
			return c.SendStatus(fiber.StatusNotFound)
		}
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Artifact recovery service is unavailable"})
	}
	return c.JSON(item)
}

func (ctrl *Controller) ResolveArtifactRecoveryHold(c *fiber.Ctx) error {
	if len(c.Body()) > 1024 {
		return c.SendStatus(fiber.StatusRequestEntityTooLarge)
	}
	var request struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid recovery action"})
	}
	adminID, _ := c.Locals("user_id").(string)
	result, err := tasks.ExecuteArtifactRecoveryAction(c.UserContext(), c.Params("id"), adminID, adminOperationKey(c), request.Action, strings.TrimSpace(request.Reason))
	if err != nil {
		switch {
		case errors.Is(err, tasks.ErrRecoveryInvalidRequest):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid recovery action"})
		case errors.Is(err, tasks.ErrRecoveryCaseNotFound):
			return c.SendStatus(fiber.StatusNotFound)
		case errors.Is(err, tasks.ErrRecoveryCaseNotHeld), errors.Is(err, tasks.ErrRecoveryIdempotencyConflict), errors.Is(err, tasks.ErrRecoveryActionInProgress), errors.Is(err, tasks.ErrRecoveryAttemptsExhausted):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Recovery action conflicts with current case state"})
		default:
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Artifact recovery service is unavailable"})
		}
	}
	return c.JSON(result)
}

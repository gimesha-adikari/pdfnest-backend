package storage

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ctrl *Controller) PresignUploads(c *fiber.Ctx) error {
	var req PresignRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_R2_PRESIGN_REQUEST",
			Message: "Invalid presign request body: " + err.Error(),
		})
	}

	if len(req.Files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_FILES",
			Message: "No files provided for presigning.",
		})
	}

	store, err := Default()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "R2_NOT_CONFIGURED",
			Message: err.Error(),
		})
	}

	resp, err := store.PresignUploads(context.Background(), req, 15*time.Minute)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "R2_PRESIGN_FAILED",
			Message: err.Error(),
		})
	}

	return c.JSON(resp)
}

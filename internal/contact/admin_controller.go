package contact

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
)

type AdminController struct {
	service *AdminService
}

func NewAdminController() *AdminController {
	return &AdminController{service: NewAdminService()}
}

func (ctrl *AdminController) Dashboard(c *fiber.Ctx) error {
	data, err := ctrl.service.Dashboard()
	if err != nil {
		return c.Status(500).JSON(APIError{Code: "DASHBOARD_FAILED", Message: err.Error()})
	}
	return c.JSON(data)
}

func (ctrl *AdminController) ListTickets(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))

	result, err := ctrl.service.ListTickets(AdminTicketQuery{
		Status:   strings.TrimSpace(c.Query("status")),
		Category: strings.TrimSpace(c.Query("category")),
		Priority: strings.TrimSpace(c.Query("priority")),
		Search:   strings.TrimSpace(c.Query("search")),
		Sort:     strings.TrimSpace(c.Query("sort")),
		Page:     page,
		Limit:    limit,
	})
	if err != nil {
		return c.Status(500).JSON(APIError{Code: "LIST_TICKETS_FAILED", Message: err.Error()})
	}
	return c.JSON(result)
}

func (ctrl *AdminController) GetTicket(c *fiber.Ctx) error {
	ticket, err := ctrl.service.GetTicket(c.Params("id"))
	if err != nil {
		return c.Status(404).JSON(APIError{Code: "TICKET_NOT_FOUND", Message: "Ticket not found"})
	}
	return c.JSON(fiber.Map{"ticket": ticket})
}

func (ctrl *AdminController) UpdateStatus(c *fiber.Ctx) error {
	var req struct {
		Status string `json:"status"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_BODY", Message: "Invalid request body"})
	}
	if err := ctrl.service.UpdateStatus(c.Params("id"), req.Status); err != nil {
		return c.Status(400).JSON(APIError{Code: "STATUS_UPDATE_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func (ctrl *AdminController) UpdatePriority(c *fiber.Ctx) error {
	var req struct {
		Priority string `json:"priority"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_BODY", Message: "Invalid request body"})
	}
	if err := ctrl.service.UpdatePriority(c.Params("id"), req.Priority); err != nil {
		return c.Status(400).JSON(APIError{Code: "PRIORITY_UPDATE_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func (ctrl *AdminController) UpdateNotes(c *fiber.Ctx) error {
	var req struct {
		InternalNotes string `json:"internal_notes"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_BODY", Message: "Invalid request body"})
	}
	if err := ctrl.service.UpdateNotes(c.Params("id"), req.InternalNotes); err != nil {
		return c.Status(400).JSON(APIError{Code: "NOTES_UPDATE_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func (ctrl *AdminController) ReplyTicket(c *fiber.Ctx) error {
	var req struct {
		Message string `json:"message"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_BODY", Message: "Invalid request body"})
	}
	if err := ctrl.service.Reply(c.Params("id"), req.Message); err != nil {
		return c.Status(400).JSON(APIError{Code: "REPLY_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Reply sent"})
}

func (ctrl *AdminController) ListCategories(c *fiber.Ctx) error {
	items, err := ctrl.service.ListCategories()
	if err != nil {
		return c.Status(500).JSON(APIError{Code: "LIST_CATEGORIES_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"items": items})
}

func (ctrl *AdminController) CreateCategory(c *fiber.Ctx) error {
	var req CategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_BODY", Message: "Invalid request body"})
	}
	category, err := ctrl.service.CreateCategory(req)
	if err != nil {
		return c.Status(400).JSON(APIError{Code: "CATEGORY_CREATE_FAILED", Message: err.Error()})
	}
	return c.Status(201).JSON(fiber.Map{"success": true, "item": category})
}

func (ctrl *AdminController) UpdateCategory(c *fiber.Ctx) error {
	var req CategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(APIError{Code: "INVALID_BODY", Message: "Invalid request body"})
	}
	if err := ctrl.service.UpdateCategory(c.Params("id"), req); err != nil {
		return c.Status(400).JSON(APIError{Code: "CATEGORY_UPDATE_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func (ctrl *AdminController) DeleteCategory(c *fiber.Ctx) error {
	if err := ctrl.service.DeleteCategory(c.Params("id")); err != nil {
		return c.Status(400).JSON(APIError{Code: "CATEGORY_DELETE_FAILED", Message: err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func (ctrl *AdminController) Reports(c *fiber.Ctx) error {
	data, err := ctrl.service.Reports()
	if err != nil {
		return c.Status(500).JSON(APIError{Code: "REPORTS_FAILED", Message: err.Error()})
	}
	return c.JSON(data)
}

package contact

import (
	"mime/multipart"
	"strings"

	"github.com/gofiber/fiber/v2"
)

type CreateTicketRequest struct {
	UserID *string

	Name     string
	Email    string
	Category string
	Subject  string
	Message  string

	Attachments []*multipart.FileHeader

	IPAddress string
	UserAgent string
}

type Controller struct {
	service *Service
}

func NewController() *Controller {
	return &Controller{
		service: NewService(),
	}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ctrl *Controller) CreateTicket(c *fiber.Ctx) error {
	email := strings.TrimSpace(c.FormValue("email"))
	category := strings.TrimSpace(c.FormValue("category"))
	subject := strings.TrimSpace(c.FormValue("subject"))
	message := strings.TrimSpace(c.FormValue("message"))
	name := strings.TrimSpace(c.FormValue("name"))

	if email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_EMAIL",
			Message: "Email address is required.",
		})
	}

	if category == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_CATEGORY",
			Message: "Category is required.",
		})
	}

	if subject == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_SUBJECT",
			Message: "Subject is required.",
		})
	}

	if message == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_MESSAGE",
			Message: "Message is required.",
		})
	}

	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_MULTIPART_FORM",
			Message: "Unable to read submitted form.",
		})
	}

	var attachments []*multipart.FileHeader

	if form != nil {
		attachments = form.File["attachments"]
	}

	req := CreateTicketRequest{
		Name:        name,
		Email:       email,
		Category:    category,
		Subject:     subject,
		Message:     message,
		Attachments: attachments,

		IPAddress: c.IP(),
		UserAgent: c.Get("User-Agent"),
	}

	userID := c.Locals("user_id")
	if userID != nil {
		id := userID.(string)
		req.UserID = &id
	}

	ticket, err := ctrl.service.CreateTicket(req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "CONTACT_CREATE_FAILED",
			Message: err.Error(),
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success":      true,
		"ticketNumber": ticket.TicketNumber,
		"message":      "Your message has been received successfully.",
	})
}

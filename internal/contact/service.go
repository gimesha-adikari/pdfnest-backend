package contact

import (
	"fmt"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"pdfnest-backend/config"

	"github.com/google/uuid"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) CreateTicket(req CreateTicketRequest) (*config.ContactTicket, error) {
	if err := s.validateAttachments(req.Attachments); err != nil {
		return nil, err
	}

	now := time.Now()

	ticketNumber, err := s.generateTicketNumber()
	if err != nil {
		return nil, err
	}

	var safeUserID *string
	if req.UserID != nil {
		cleanID := strings.TrimSpace(*req.UserID)
		if cleanID != "" && cleanID != "undefined" && cleanID != "null" {
			var count int64
			config.DB.Model(&config.User{}).Where("id = ?", cleanID).Count(&count)
			if count > 0 {
				safeUserID = &cleanID
			}
		}
	}

	ticket := &config.ContactTicket{
		ID:           uuid.New().String(),
		TicketNumber: ticketNumber,
		UserID:       safeUserID,
		Email:        req.Email,
		Category:     req.Category,
		Subject:      req.Subject,
		Message:      req.Message,
		Status:       "open",
		Priority:     "normal",
		Source:       "website",
		EmailStatus:  "pending",
		IPAddress:    req.IPAddress,
		UserAgent:    req.UserAgent,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if req.Name != "" {
		ticket.Name = &req.Name
	}

	if err := config.DB.Create(ticket).Error; err != nil {
		return nil, err
	}

	if err := s.sendSupportEmail(ticket, req.Attachments); err != nil {
		config.DB.Model(ticket).Update("email_status", "failed")
		return ticket, nil
	}

	config.DB.Model(ticket).Update("email_status", "sent")

	return ticket, nil
}

func (s *Service) validateAttachments(files []*multipart.FileHeader) error {
	const (
		maxFiles    = 5
		maxFileSize = 10 * 1024 * 1024
	)

	if len(files) > maxFiles {
		return fmt.Errorf("maximum %d attachments are allowed", maxFiles)
	}

	allowedExtensions := map[string]bool{
		".png":  true,
		".jpg":  true,
		".jpeg": true,
		".gif":  true,
		".webp": true,
		".pdf":  true,
	}

	for _, file := range files {
		if file.Size > maxFileSize {
			return fmt.Errorf(
				"%s exceeds the maximum allowed size of 10 MB",
				file.Filename,
			)
		}

		ext := strings.ToLower(filepath.Ext(file.Filename))

		if !allowedExtensions[ext] {
			return fmt.Errorf(
				"%s is not a supported attachment type",
				file.Filename,
			)
		}
	}

	return nil
}

func (s *Service) generateTicketNumber() (string, error) {
	var sequence int64

	if err := config.DB.Raw(
		"SELECT nextval('contact_ticket_sequence')",
	).Scan(&sequence).Error; err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"SUP-%d-%06d",
		time.Now().Year(),
		sequence,
	), nil
}

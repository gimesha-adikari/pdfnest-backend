package contact

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"os"
	"pdfnest-backend/config"

	"github.com/resend/resend-go/v2"
)

type SupportEmailData struct {
	TicketNumber string

	Status   string
	Priority string
	Category string
	Source   string

	Name  string
	Email string

	Subject string
	Message string

	IPAddress string
	UserAgent string

	CreatedAt string

	HasAttachments bool
	Attachments    []string
}

func (s *Service) sendSupportEmail(
	ticket *config.ContactTicket,
	files []*multipart.FileHeader,
) error {

	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("RESEND_API_KEY is not configured")
	}

	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		return fmt.Errorf("FROM_EMAIL is not configured")
	}

	supportEmail := os.Getenv("SUPPORT_EMAIL")
	if supportEmail == "" {
		return fmt.Errorf("SUPPORT_EMAIL is not configured")
	}

	htmlBody, err := buildSupportEmailHTML(ticket, files)
	if err != nil {
		return err
	}

	client := resend.NewClient(apiKey)

	params := &resend.SendEmailRequest{
		From: fromEmail,
		To:   []string{supportEmail},

		Subject: fmt.Sprintf(
			"[%s] %s",
			ticket.TicketNumber,
			ticket.Subject,
		),

		Html: htmlBody,

		Text: fmt.Sprintf(
			`New Support Ticket

Ticket: %s

Category: %s

Email: %s

Subject:
%s

Message:

%s`,
			ticket.TicketNumber,
			ticket.Category,
			ticket.Email,
			ticket.Subject,
			ticket.Message,
		),
	}

	for _, file := range files {

		f, err := file.Open()
		if err != nil {
			return err
		}

		content, err := io.ReadAll(f)
		f.Close()

		if err != nil {
			return err
		}

		params.Attachments = append(
			params.Attachments,
			&resend.Attachment{
				Filename: file.Filename,
				Content:  content,
			},
		)
	}

	email, err := client.Emails.Send(params)
	if err != nil {
		log.Printf("Resend error: %v", err)
		return err
	}

	log.Printf("Support email sent successfully: %+v", email)

	return nil
}

func buildSupportEmailHTML(
	ticket *config.ContactTicket,
	files []*multipart.FileHeader,
) (string, error) {

	tmpl, err := template.ParseFiles(
		"internal/contact/templates/support_email.html",
	)
	if err != nil {
		return "", err
	}

	var attachmentNames []string

	for _, file := range files {
		attachmentNames = append(attachmentNames, file.Filename)
	}

	data := SupportEmailData{
		TicketNumber: ticket.TicketNumber,

		Status:   ticket.Status,
		Priority: ticket.Priority,
		Category: ticket.Category,
		Source:   ticket.Source,

		Name:  stringOrDash(ticket.Name),
		Email: ticket.Email,

		Subject: ticket.Subject,
		Message: ticket.Message,

		IPAddress: ticket.IPAddress,
		UserAgent: ticket.UserAgent,

		CreatedAt: ticket.CreatedAt.Format("2006-01-02 15:04:05"),

		HasAttachments: len(attachmentNames) > 0,
		Attachments:    attachmentNames,
	}

	var body bytes.Buffer

	if err := tmpl.Execute(&body, data); err != nil {
		return "", err
	}

	return body.String(), nil
}

func stringOrDash(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}

	return *value
}

package contact

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"os"
	"strings"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/mailer"
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

	ContactURL string
}

//go:embed templates/support_email.html
var supportEmailTemplates embed.FS

func (s *Service) sendSupportEmail(
	ticket *config.ContactTicket,
	files []*multipart.FileHeader,
) error {
	supportEmail := os.Getenv("SUPPORT_EMAIL")
	if supportEmail == "" {
		return fmt.Errorf("SUPPORT_EMAIL is not configured")
	}

	frontendURL := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}
	contactURL := fmt.Sprintf("%s/contact", frontendURL)

	log.Printf("[CONTACT EMAIL] preparing support email ticket=%s attachments=%d", ticket.TicketNumber, len(files))

	htmlBody, err := buildSupportEmailHTML(ticket, files, contactURL)
	if err != nil {
		log.Printf("[CONTACT EMAIL] template render failed for ticket=%s: %v", ticket.TicketNumber, err)
		return err
	}

	textBody := fmt.Sprintf(
		`New Support Ticket

Ticket: %s

Category: %s

Email: %s

Subject:
%s

Message:

%s

Need help with the website?
%s`,
		ticket.TicketNumber,
		ticket.Category,
		ticket.Email,
		ticket.Subject,
		ticket.Message,
		contactURL,
	)

	var attachments []mailer.Attachment

	for _, file := range files {
		log.Printf("[CONTACT EMAIL] attaching file=%s ticket=%s", file.Filename, ticket.TicketNumber)

		f, err := file.Open()
		if err != nil {
			log.Printf("[CONTACT EMAIL] failed opening attachment=%s ticket=%s: %v", file.Filename, ticket.TicketNumber, err)
			return err
		}

		content, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			log.Printf("[CONTACT EMAIL] failed reading attachment=%s ticket=%s: %v", file.Filename, ticket.TicketNumber, err)
			return err
		}

		attachments = append(attachments, mailer.Attachment{
			Filename: file.Filename,
			Content:  content,
		})
	}

	err = mailer.Send(mailer.Email{
		To: []string{
			supportEmail,
		},
		Subject:     fmt.Sprintf("[%s] %s", ticket.TicketNumber, ticket.Subject),
		Text:        textBody,
		Html:        htmlBody,
		Attachments: attachments,
	})
	if err != nil {
		log.Printf("[CONTACT EMAIL] send failed ticket=%s: %v", ticket.TicketNumber, err)
		return err
	}

	log.Printf("[CONTACT EMAIL] support email sent successfully ticket=%s to=%s", ticket.TicketNumber, supportEmail)
	return nil
}

func buildSupportEmailHTML(
	ticket *config.ContactTicket,
	files []*multipart.FileHeader,
	contactURL string,
) (string, error) {
	log.Printf("[CONTACT EMAIL] rendering support template ticket=%s", ticket.TicketNumber)

	tmpl, err := template.ParseFS(supportEmailTemplates, "templates/support_email.html")
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

		ContactURL: contactURL,
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		log.Printf("[CONTACT EMAIL] template execute failed ticket=%s: %v", ticket.TicketNumber, err)
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

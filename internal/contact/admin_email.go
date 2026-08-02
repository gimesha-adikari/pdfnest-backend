package contact

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"os"
	"strings"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/mailer"
)

//go:embed templates/admin_reply_email.html
var adminReplyTemplates embed.FS

type AdminReplyEmailData struct {
	TicketNumber string
	Message      string
	ContactURL   string
}

func (s *AdminService) sendReplyEmail(ticket *config.ContactTicket, message string) error {
	supportEmail := strings.TrimSpace(ticket.Email)
	if supportEmail == "" {
		return fmt.Errorf("ticket has no recipient email")
	}

	frontendURL := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	contactURL := fmt.Sprintf("%s/contact", frontendURL)

	htmlBody, err := buildAdminReplyHTML(ticket, message, contactURL)
	if err != nil {
		return err
	}

	textBody := fmt.Sprintf(
		`Reply from Platen Support

Ticket: %s

%s

Contact page:
%s`,
		ticket.TicketNumber,
		message,
		contactURL,
	)

	err = mailer.Send(mailer.Email{
		To:      []string{supportEmail},
		Subject: fmt.Sprintf("Re: [%s] %s", ticket.TicketNumber, ticket.Subject),
		Text:    textBody,
		Html:    htmlBody,
	})
	if err != nil {
		log.Printf("[CONTACT ADMIN REPLY] send failed ticket=%s: %v", ticket.TicketNumber, err)
		return err
	}

	log.Printf("[CONTACT ADMIN REPLY] reply sent successfully ticket=%s to=%s", ticket.TicketNumber, supportEmail)
	return nil
}

func buildAdminReplyHTML(ticket *config.ContactTicket, message, contactURL string) (string, error) {
	tmpl, err := template.ParseFS(adminReplyTemplates, "templates/admin_reply_email.html")
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, AdminReplyEmailData{
		TicketNumber: ticket.TicketNumber,
		Message:      message,
		ContactURL:   contactURL,
	}); err != nil {
		return "", err
	}

	return body.String(), nil
}

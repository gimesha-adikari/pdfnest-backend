package mailer

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/resend/resend-go/v2"
)

type Attachment struct {
	Filename string
	Content  []byte
}

type Email struct {
	To          []string
	Subject     string
	Text        string
	Html        string
	Attachments []Attachment
}

func Send(email Email) error {
	if os.Getenv("LOCAL") == "true" && os.Getenv("APP_ENV") != "production" {
		if outbox := strings.TrimSpace(os.Getenv("MAILER_OUTBOX_DIR")); outbox != "" {
			return writeLocalOutbox(outbox, email)
		}
	}

	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("RESEND_API_KEY is not configured")
	}

	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		return fmt.Errorf("FROM_EMAIL is not configured")
	}

	client := resend.NewClient(apiKey)

	params := &resend.SendEmailRequest{
		From:    fromEmail,
		To:      email.To,
		Subject: email.Subject,
		Text:    email.Text,
		Html:    email.Html,
	}

	for _, attachment := range email.Attachments {
		params.Attachments = append(params.Attachments, &resend.Attachment{
			Filename: attachment.Filename,
			Content:  attachment.Content,
		})
	}

	sent, err := client.Emails.Send(params)
	if err != nil {
		log.Printf("Resend error: %v", err)
		return err
	}

	log.Printf("Email sent: %+v", sent)
	return nil
}

// writeLocalOutbox is an explicit, opt-in local/test delivery sink. It uses
// the same Email payload as the production Resend path, but never activates in
// production and never sends a message to an external recipient.
func writeLocalOutbox(dir string, email Email) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create local mail outbox: %w", err)
	}
	data, err := json.MarshalIndent(email, "", "  ")
	if err != nil {
		return fmt.Errorf("encode local email: %w", err)
	}
	filename := filepath.Join(dir, fmt.Sprintf("mail-%d.json", time.Now().UnixNano()))
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create local email artifact: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write local email artifact: %w", err)
	}
	return file.Sync()
}

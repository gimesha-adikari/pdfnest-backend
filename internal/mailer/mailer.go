package mailer

import (
	"fmt"
	"log"
	"os"

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

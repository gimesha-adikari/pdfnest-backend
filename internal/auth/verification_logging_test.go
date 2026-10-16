package auth

import (
	"bytes"
	"encoding/json"
	"html"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdfnest-backend/internal/mailer"

	"github.com/stretchr/testify/require"
)

func TestVerificationEmailDoesNotLogBearerURL(t *testing.T) {
	// Environment and the process logger are restored after each nonparallel
	// subtest. The local outbox returns before provider dispatch, even on error.
	for _, scenario := range []struct {
		name, frontend, base string
		failOutbox           bool
	}{
		{"custom_frontend", "https://verification.example.invalid/app///", "https://verification.example.invalid/app", false},
		{"default_frontend", "", "http://localhost:3000", false},
		{"outbox_failure", "https://verification.example.invalid", "https://verification.example.invalid", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			outbox := filepath.Join(root, "outbox")
			if scenario.failOutbox {
				require.NoError(t, os.WriteFile(outbox, []byte("occupied"), 0600))
			}
			t.Setenv("LOCAL", "true")
			t.Setenv("APP_ENV", "development")
			t.Setenv("MAILER_OUTBOX_DIR", outbox)
			t.Setenv("FRONTEND_URL", scenario.frontend)
			t.Setenv("RESEND_API_KEY", "")
			t.Setenv("FROM_EMAIL", "")

			var captured bytes.Buffer
			previousWriter := log.Writer()
			log.SetOutput(&captured)
			t.Cleanup(func() { log.SetOutput(previousWriter) })

			const recipient = "verification-log-test@example.invalid"
			rawToken := strings.Repeat("ad", 32) // Synthetic 64-character hex bearer.
			verifyURL := scenario.base + "/verify-email?token=" + rawToken
			err := sendVerificationEmail(recipient, rawToken)
			messages := captured.String()
			require.True(t, strings.Contains(messages, "[AUTH EMAIL] preparing verification email"))
			require.True(t, strings.Contains(messages, "[AUTH EMAIL] rendering verification template"))
			if scenario.failOutbox {
				require.Error(t, err)
				require.False(t, strings.Contains(err.Error(), rawToken), "local delivery error must not disclose the bearer")
				require.True(t, strings.Contains(messages, "[AUTH EMAIL] send failed"))
				require.False(t, strings.Contains(messages, "[AUTH EMAIL] verification email sent"))
				entries, readErr := os.ReadDir(root)
				require.NoError(t, readErr)
				require.Len(t, entries, 1, "failure must leave only the blocking file")
				data, readErr := os.ReadFile(outbox)
				require.NoError(t, readErr)
				require.Equal(t, "occupied", string(data))
			} else {
				require.NoError(t, err)
				require.True(t, strings.Contains(messages, "[AUTH EMAIL] verification email sent"))
				entries, readErr := os.ReadDir(outbox)
				require.NoError(t, readErr)
				require.Len(t, entries, 1, "exactly one authorized synthetic email")
				require.False(t, entries[0].IsDir())
				data, readErr := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
				require.NoError(t, readErr)
				var email mailer.Email
				require.NoError(t, json.Unmarshal(data, &email))
				require.Equal(t, []string{recipient}, email.To)
				require.Equal(t, "Verify your Platen PDF email", email.Subject)
				require.Empty(t, email.Attachments)
				require.True(t, strings.Contains(email.Text, verifyURL), "text email must retain its verification link")
				require.True(t, strings.Contains(email.Html, `href="`+html.EscapeString(verifyURL)+`"`), "embedded HTML template must retain its verification link")
			}

			// Boolean assertions avoid printing capabilities into test diagnostics
			// if a future regression reintroduces the disclosure.
			for _, value := range []struct{ name, text string }{
				{"raw bearer", rawToken}, {"complete verification URL", verifyURL}, {"token-bearing query", "?token=" + rawToken},
			} {
				require.False(t, strings.Contains(messages, value.text), "%s must not be logged", value.name)
			}
		})
	}
}

package ai

import (
	"regexp"
)

var (
	// Private key blocks (RSA, EC, OpenSSH, Generic)
	rePrivateKey = regexp.MustCompile(`(?i)-----BEGIN[ A-Z0-9_-]*PRIVATE KEY-----[\s\S]*?-----END[ A-Z0-9_-]*PRIVATE KEY-----`)

	// Bearer authorization tokens
	reBearerToken = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9\-_.~+/=]{16,}`)

	// JWT strings (eyJ...)
	reJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9-_=]+\.[A-Za-z0-9-_=]+\.?[A-Za-z0-9-_.+/=]*\b`)

	// AWS Access Key IDs
	reAWSKey = regexp.MustCompile(`\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)

	// Google API Keys
	reGoogleKey = regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`)

	// Database Connection Strings with Credentials
	reConnString = regexp.MustCompile(`(?i)\b(postgres|postgresql|mysql|mongodb|redis|amqp)://([^:]+):([^@\s]+)@`)

	// Key/Value Secret Assignments (password=..., secret: ..., api_key=...)
	reSecretAssign = regexp.MustCompile(`(?i)\b(password|secret|api_key|apikey|access_token|refresh_token)\s*[:=]\s*["']?([^\s"';,]{4,})["']?`)
)

// ScrubSecrets performs defense-in-depth sanitization of prompt buffers, replacing detected credentials with redacted tokens.
// Standalone uppercase environment variable names (e.g. DATABASE_URL, API_KEY) without assigned values remain untouched.
func ScrubSecrets(input string) string {
	if input == "" {
		return ""
	}

	result := input
	result = rePrivateKey.ReplaceAllString(result, "[REDACTED_PRIVATE_KEY]")
	result = reBearerToken.ReplaceAllString(result, "${1}[REDACTED_TOKEN]")
	result = reJWT.ReplaceAllString(result, "[REDACTED_JWT]")
	result = reAWSKey.ReplaceAllString(result, "[REDACTED_AWS_KEY]")
	result = reGoogleKey.ReplaceAllString(result, "[REDACTED_GOOGLE_KEY]")
	result = reConnString.ReplaceAllString(result, "${1}://${2}:[REDACTED]@")
	result = reSecretAssign.ReplaceAllString(result, "${1}=[REDACTED_SECRET]")

	return result
}

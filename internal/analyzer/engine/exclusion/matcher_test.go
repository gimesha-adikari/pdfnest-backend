package exclusion

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		path     string
		expected bool
	}{
		{"Exact file match", "package.json", "package.json", true},
		{"Exact nested match", "src/index.ts", "src/index.ts", true},
		{"Single star extension", "*.ts", "index.ts", true},
		{"Single star extension in subfolder", "*.ts", "src/index.ts", true},
		{"Single star mismatch", "*.ts", "index.js", false},
		{"Double star prefix", "**/node_modules/**", "node_modules/foo/bar.js", true},
		{"Double star prefix nested", "**/node_modules/**", "packages/app/node_modules/foo/bar.js", true},
		{"Double star prefix filename", "**/*.pem", "certs/server.pem", true},
		{"Double star prefix nested filename", "**/*.pem", "a/b/c/cert.pem", true},
		{"Directory prefix pattern", "dist/**", "dist/bundle.js", true},
		{"Directory prefix pattern nested", "dist/**", "dist/static/css/app.css", true},
		{"Directory prefix mismatch", "dist/**", "src/dist/bundle.js", false},
		{"Wildcard id_rsa pattern", "**/id_rsa*", "id_rsa", true},
		{"Wildcard id_rsa pub pattern", "**/id_rsa*", "id_rsa.pub", true},
		{"Wildcard id_rsa nested", "**/id_rsa*", ".ssh/id_rsa_backup", true},
		{"Single question mark", "file?.txt", "file1.txt", true},
		{"Normalized slash on Windows path", "src/**/*.ts", "src/utils/math.ts", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchGlob(tt.pattern, tt.path)
			assert.Equal(t, tt.expected, result, "pattern=%s, path=%s", tt.pattern, tt.path)
		})
	}
}

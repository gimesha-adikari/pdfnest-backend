package inventory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyFile(t *testing.T) {
	tests := []struct {
		relPath      string
		expectedCat  FileCategory
		expectedLang string
	}{
		{"package.json", CategoryManifest, "JavaScript"},
		{"go.mod", CategoryManifest, "Go"},
		{"Cargo.toml", CategoryManifest, "Rust"},
		{"requirements.txt", CategoryManifest, "Python"},
		{"pom.xml", CategoryManifest, "Java"},
		{"composer.json", CategoryManifest, "PHP"},
		{"Gemfile", CategoryManifest, "Ruby"},

		{"src/app.ts", CategorySource, "TypeScript"},
		{"components/Header.tsx", CategorySource, "TypeScript"},
		{"lib/server.go", CategorySource, "Go"},
		{"app/main.py", CategorySource, "Python"},
		{"src/lib.rs", CategorySource, "Rust"},
		{"App.java", CategorySource, "Java"},

		{"__tests__/api.test.ts", CategoryTest, "TypeScript"},
		{"src/service_test.go", CategoryTest, "Go"},
		{"tests/test_auth.py", CategoryTest, "Python"},
		{"spec/models_spec.rb", CategoryTest, "Ruby"},

		{"README.md", CategoryDocumentation, "Markdown"},
		{"LICENSE", CategoryDocumentation, "Markdown"},
		{"docs/architecture.rst", CategoryDocumentation, "Markdown"},

		{"Dockerfile", CategoryConfig, "Config"},
		{"docker-compose.yml", CategoryConfig, "Config"},
		{"tsconfig.json", CategoryConfig, "Config"},
		{"vite.config.ts", CategoryConfig, "Config"},
		{"next.config.mjs", CategoryConfig, "Config"},
		{".env.example", CategoryConfig, "Config"},

		{"app.exe", CategoryBinary, ""},
		{"libsqlite3.so", CategoryBinary, ""},
		{"archive.zip", CategoryBinary, ""},

		{"logo.png", CategoryAsset, ""},
		{"icon.svg", CategoryAsset, ""},
		{"font.woff2", CategoryAsset, ""},
		{"document.pdf", CategoryAsset, ""},

		{"data.binraw", CategoryUnknown, ""},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			cat, lang := ClassifyFile(tt.relPath)
			assert.Equal(t, tt.expectedCat, cat, "Path: %s Category", tt.relPath)
			if tt.expectedLang != "" {
				assert.Equal(t, tt.expectedLang, lang, "Path: %s Language", tt.relPath)
			}
		})
	}
}

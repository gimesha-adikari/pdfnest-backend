package inventory

import (
	"path/filepath"
	"strings"
)

// ClassifyFile deterministically maps a relative file path to its FileCategory and detected Language name.
// Phase 2 scope: Classification ONLY. Does NOT attempt framework/database detection.
func ClassifyFile(relPath string) (FileCategory, string) {
	norm := filepath.ToSlash(filepath.Clean(relPath))
	base := filepath.Base(norm)
	lowerBase := strings.ToLower(base)
	ext := strings.ToLower(filepath.Ext(base))

	// 1. Check Manifests (Highest priority classification)
	if isManifestFile(lowerBase) {
		return CategoryManifest, manifestLanguage(lowerBase)
	}

	// 2. Check Tests
	if isTestFile(norm, lowerBase, ext) {
		return CategoryTest, extensionLanguage(ext, lowerBase)
	}

	// 3. Check Documentation
	if isDocumentationFile(lowerBase, ext) {
		return CategoryDocumentation, "Markdown"
	}

	// 4. Check Config & Build files
	if isConfigFile(lowerBase, ext) {
		return CategoryConfig, "Config"
	}

	// 5. Check Binaries
	if isBinaryExtension(ext) {
		return CategoryBinary, ""
	}

	// 6. Check Assets & Media
	if isAssetExtension(ext) {
		return CategoryAsset, ""
	}

	// 7. Check Source code files
	lang := extensionLanguage(ext, lowerBase)
	if lang != "" {
		return CategorySource, lang
	}

	return CategoryUnknown, ""
}

func isManifestFile(lowerBase string) bool {
	manifests := map[string]bool{
		"package.json":         true,
		"package-lock.json":    true,
		"yarn.lock":            true,
		"pnpm-lock.yaml":       true,
		"go.mod":               true,
		"go.sum":               true,
		"cargo.toml":           true,
		"cargo.lock":           true,
		"requirements.txt":     true,
		"requirements-dev.txt": true,
		"pyproject.toml":       true,
		"pipfile":              true,
		"pipfile.lock":         true,
		"pom.xml":              true,
		"build.gradle":         true,
		"build.gradle.kts":     true,
		"composer.json":        true,
		"composer.lock":        true,
		"gemfile":              true,
		"gemfile.lock":         true,
	}
	return manifests[lowerBase]
}

func manifestLanguage(lowerBase string) string {
	switch lowerBase {
	case "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml":
		return "JavaScript"
	case "go.mod", "go.sum":
		return "Go"
	case "cargo.toml", "cargo.lock":
		return "Rust"
	case "requirements.txt", "requirements-dev.txt", "pyproject.toml", "pipfile", "pipfile.lock":
		return "Python"
	case "pom.xml", "build.gradle", "build.gradle.kts":
		return "Java"
	case "composer.json", "composer.lock":
		return "PHP"
	case "gemfile", "gemfile.lock":
		return "Ruby"
	default:
		return "Config"
	}
}

func isTestFile(norm string, lowerBase string, ext string) bool {
	if strings.Contains(norm, "/__tests__/") ||
		strings.HasPrefix(norm, "__tests__/") ||
		strings.Contains(norm, "/tests/") ||
		strings.HasPrefix(norm, "tests/") ||
		strings.Contains(norm, "/spec/") ||
		strings.HasPrefix(norm, "spec/") {
		return true
	}

	testSuffixes := []string{
		"_test.go",
		".test.ts",
		".spec.ts",
		".test.tsx",
		".spec.tsx",
		".test.js",
		".spec.js",
		".test.jsx",
		".spec.jsx",
		"_test.py",
		"_spec.rb",
	}

	for _, s := range testSuffixes {
		if strings.HasSuffix(lowerBase, s) {
			return true
		}
	}

	if strings.HasPrefix(lowerBase, "test_") && ext == ".py" {
		return true
	}

	return false
}

func isDocumentationFile(lowerBase string, ext string) bool {
	docNames := map[string]bool{
		"readme":          true,
		"license":         true,
		"contributing":    true,
		"changelog":       true,
		"authors":         true,
		"architecture":    true,
		"code_of_conduct": true,
	}

	nameWithoutExt := strings.TrimSuffix(lowerBase, ext)
	if docNames[nameWithoutExt] {
		return true
	}

	docExts := map[string]bool{
		".md":       true,
		".markdown": true,
		".rst":      true,
		".adoc":     true,
	}
	return docExts[ext]
}

func isConfigFile(lowerBase string, ext string) bool {
	configNames := map[string]bool{
		"dockerfile":          true,
		"docker-compose.yml":  true,
		"docker-compose.yaml": true,
		"compose.yml":         true,
		"compose.yaml":        true,
		"tsconfig.json":       true,
		"jsconfig.json":       true,
		"tailwind.config.js":  true,
		"tailwind.config.ts":  true,
		"vite.config.js":      true,
		"vite.config.ts":      true,
		"next.config.js":      true,
		"next.config.mjs":     true,
		"next.config.ts":      true,
		"webpack.config.js":   true,
		"jest.config.js":      true,
		"jest.config.ts":      true,
		"vitest.config.ts":    true,
		"eslint.config.js":    true,
		".eslintrc.json":      true,
		".prettierrc":         true,
		".gitignore":          true,
		".env.example":        true,
		".env.sample":         true,
		".env.template":       true,
	}
	if configNames[lowerBase] {
		return true
	}

	configExts := map[string]bool{
		".yaml":   true,
		".yml":    true,
		".toml":   true,
		".ini":    true,
		".conf":   true,
		".proto":  true,
		".prisma": true,
	}
	return configExts[ext]
}

func isBinaryExtension(ext string) bool {
	binaryExts := map[string]bool{
		".exe":   true,
		".dll":   true,
		".so":    true,
		".dylib": true,
		".bin":   true,
		".wasm":  true,
		".class": true,
		".o":     true,
		".a":     true,
		".zip":   true,
		".tar":   true,
		".gz":    true,
	}
	return binaryExts[ext]
}

func isAssetExtension(ext string) bool {
	assetExts := map[string]bool{
		".png":   true,
		".jpg":   true,
		".jpeg":  true,
		".gif":   true,
		".svg":   true,
		".ico":   true,
		".webp":  true,
		".mp4":   true,
		".mp3":   true,
		".pdf":   true,
		".woff":  true,
		".woff2": true,
		".ttf":   true,
		".eot":   true,
	}
	return assetExts[ext]
}

func extensionLanguage(ext string, lowerBase string) string {
	switch ext {
	case ".ts":
		return "TypeScript"
	case ".tsx":
		return "TypeScript"
	case ".js", ".mjs", ".cjs":
		return "JavaScript"
	case ".jsx":
		return "JavaScript"
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".c", ".h":
		return "C"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "C++"
	case ".cs":
		return "C#"
	case ".php":
		return "PHP"
	case ".rb":
		return "Ruby"
	case ".swift":
		return "Swift"
	case ".kt", ".kts":
		return "Kotlin"
	case ".scala":
		return "Scala"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".scss", ".sass", ".less":
		return "CSS"
	case ".sql":
		return "SQL"
	case ".sh", ".bash", ".zsh":
		return "Shell"
	case ".json":
		return "JSON"
	default:
		if lowerBase == "dockerfile" {
			return "Dockerfile"
		}
		return ""
	}
}

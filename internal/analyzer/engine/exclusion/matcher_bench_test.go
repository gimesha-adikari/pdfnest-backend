package exclusion

import (
	"fmt"
	"testing"
)

func BenchmarkMatchGlob_Shallow(b *testing.B) {
	pattern := "package.json"
	path := "package.json"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !MatchGlob(pattern, path) {
			b.Fatal("match failed")
		}
	}
}

func BenchmarkMatchGlob_Deep(b *testing.B) {
	pattern := "**/node_modules/**"
	path := "apps/web/node_modules/@tanstack/react-virtual/build/esm/index.js"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !MatchGlob(pattern, path) {
			b.Fatal("match failed")
		}
	}
}

func BenchmarkMatchGlob_SecurityPattern(b *testing.B) {
	pattern := "**/*.pem"
	path := "services/auth/certs/production/tls_server.pem"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !MatchGlob(pattern, path) {
			b.Fatal("match failed")
		}
	}
}

func BenchmarkExclusionEngine_Evaluate_Batch10k(b *testing.B) {
	engine := NewEngine(Config{
		CustomPatterns: []string{"legacy/**", "docs/drafts/**", "tests/fixtures/**"},
		GitignoreRules: []string{"*.log", "temp/**", ".cache/**"},
		EnabledPresets: []string{"preset-node-modules", "preset-build-dist", "preset-coverage"},
		ForceIncludes:  []string{"!legacy/keeper.ts", "!dist/bundle.js"},
	})

	// Prepare 1,000 synthetic paths of varying depths
	paths := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		switch i % 5 {
		case 0:
			paths[i] = fmt.Sprintf("apps/web/node_modules/pkg_%d/index.js", i)
		case 1:
			paths[i] = fmt.Sprintf("services/api/internal/handler_%d.go", i)
		case 2:
			paths[i] = fmt.Sprintf("docs/drafts/spec_%d.md", i)
		case 3:
			paths[i] = fmt.Sprintf("services/worker/app/tasks_%d.py", i)
		case 4:
			paths[i] = fmt.Sprintf("certs/sec_%d.pem", i)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for _, p := range paths {
			_ = engine.Evaluate(p)
		}
	}
}

func BenchmarkExclusionEngine_Evaluate_CaseInsensitiveSecurity(b *testing.B) {
	engine := NewEngine(Config{})

	paths := []string{
		".ENV.PRODUCTION",
		"ID_RSA_BACKUP",
		"certs/SERVER.PEM",
		"config/CREDENTIALS.JSON",
		"src/components/App.tsx",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for _, p := range paths {
			_ = engine.Evaluate(p)
		}
	}
}

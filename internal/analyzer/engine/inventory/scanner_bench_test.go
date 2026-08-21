package inventory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/internal/analyzer/engine/exclusion"
)

// createEnterpriseMonorepo creates a deterministic synthetic repository fixture.
// Total file count approx 25,000 files.
func createEnterpriseMonorepo(b *testing.B, mode string) string {
	b.Helper()
	dir := b.TempDir()

	switch mode {
	case "Pruned":
		// 20,000 files in pruned directories (node_modules, .git, dist) + 5,000 analyzable files
		populateFiles(b, filepath.Join(dir, "node_modules", "package-a"), 10000, "js")
		populateFiles(b, filepath.Join(dir, ".git", "objects"), 6000, "pack")
		populateFiles(b, filepath.Join(dir, "dist", "static"), 4000, "map")
		populateFiles(b, filepath.Join(dir, "apps", "web", "src"), 2500, "tsx")
		populateFiles(b, filepath.Join(dir, "services", "api", "cmd"), 1500, "go")
		populateFiles(b, filepath.Join(dir, "services", "worker", "app"), 1000, "py")

	case "Analyzable":
		// 25,000 analyzable files across frontend, backend, shared packages, and services
		populateFiles(b, filepath.Join(dir, "apps", "web", "components"), 8000, "tsx")
		populateFiles(b, filepath.Join(dir, "apps", "web", "lib"), 4000, "ts")
		populateFiles(b, filepath.Join(dir, "services", "api", "internal"), 6000, "go")
		populateFiles(b, filepath.Join(dir, "services", "worker", "tasks"), 4000, "py")
		populateFiles(b, filepath.Join(dir, "packages", "ui", "src"), 3000, "tsx")

	case "Mixed":
		// 12,500 analyzable + 12,500 pruned
		populateFiles(b, filepath.Join(dir, "node_modules", "lodash"), 8000, "js")
		populateFiles(b, filepath.Join(dir, "dist", "chunks"), 4500, "js")
		populateFiles(b, filepath.Join(dir, "apps", "web", "src"), 5000, "tsx")
		populateFiles(b, filepath.Join(dir, "services", "api"), 4500, "go")
		populateFiles(b, filepath.Join(dir, "services", "worker"), 3000, "py")
	}

	return dir
}

func populateFiles(b *testing.B, parentDir string, count int, ext string) {
	b.Helper()
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		b.Fatalf("mkdir failed: %v", err)
	}

	for i := 0; i < count; i++ {
		filename := filepath.Join(parentDir, fmt.Sprintf("file_%05d.%s", i, ext))
		// Write a minimal 10-byte deterministic payload
		if err := os.WriteFile(filename, []byte("export = 1"), 0644); err != nil {
			b.Fatalf("write file failed: %v", err)
		}
	}
}

func BenchmarkScanRepository_25kFiles(b *testing.B) {
	modes := []string{"Pruned", "Analyzable", "Mixed"}

	for _, mode := range modes {
		b.Run(mode, func(b *testing.B) {
			repoDir := createEnterpriseMonorepo(b, mode)
			exEngine := exclusion.NewEngine(exclusion.Config{})
			opts := DefaultScannerOptions(exEngine)
			opts.MaxFiles = 50000
			opts.MaxTotalBytes = 500 * 1024 * 1024

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				inv, err := ScanRepository(context.Background(), repoDir, opts)
				if err != nil {
					b.Fatalf("scan failed: %v", err)
				}
				if inv == nil {
					b.Fatal("nil inventory")
				}
			}
		})
	}
}

func BenchmarkScanRepository_Cancellation(b *testing.B) {
	repoDir := createEnterpriseMonorepo(b, "Analyzable")
	exEngine := exclusion.NewEngine(exclusion.Config{})
	opts := DefaultScannerOptions(exEngine)
	opts.MaxFiles = 50000

	b.ResetTimer()
	b.ReportAllocs()

	var totalCancellationLatency time.Duration
	var cancellationRuns int

	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()

		// Trigger cancellation after 1ms into walk
		go func() {
			time.Sleep(1 * time.Millisecond)
			cancel()
		}()

		_, err := ScanRepository(ctx, repoDir, opts)
		elapsed := time.Since(start)

		if err == context.Canceled {
			totalCancellationLatency += elapsed
			cancellationRuns++
		}
	}

	if cancellationRuns > 0 {
		b.ReportMetric(float64(totalCancellationLatency.Milliseconds())/float64(cancellationRuns), "ms/cancel")
	}
}

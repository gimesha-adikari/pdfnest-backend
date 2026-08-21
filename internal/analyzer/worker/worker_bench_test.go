package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
)

func createBenchmarkRepoZip(b *testing.B, fileCount int) string {
	b.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)

	// Manifest
	f, err := w.Create("package.json")
	if err != nil {
		b.Fatalf("create manifest: %v", err)
	}
	_, _ = f.Write([]byte(`{"name":"benchmark-repo","dependencies":{"express":"^4.18.2"}}`))

	// Source files
	for i := 0; i < fileCount; i++ {
		sf, err := w.Create(fmt.Sprintf("src/component_%d.ts", i))
		if err != nil {
			b.Fatalf("create source file: %v", err)
		}
		_, _ = sf.Write([]byte(fmt.Sprintf("export const val%d = %d;", i, i)))
	}

	if err := w.Close(); err != nil {
		b.Fatalf("close zip: %v", err)
	}

	zipPath := filepath.Join(b.TempDir(), "bench_repo.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		b.Fatalf("write zip file: %v", err)
	}

	return zipPath
}

func BenchmarkWorkerConcurrency(b *testing.B) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	workerCounts := []int{1, 2, 4, 8, 16}
	zipPath := createBenchmarkRepoZip(b, 50)

	for _, count := range workerCounts {
		b.Run(fmt.Sprintf("Workers_%d", count), func(b *testing.B) {
			q := NewMemoryJobQueue(b.N + count + 100)

			cfg := DefaultWorkerConfig()
			cfg.Concurrency = count
			cfg.SandboxBaseDir = b.TempDir()

			w, err := NewAnalyzerWorker(cfg, q)
			if err != nil {
				b.Fatalf("new worker: %v", err)
			}

			// Pre-populate queue with b.N jobs
			for i := 0; i < b.N; i++ {
				job := &AnalyzerJob{
					JobVersion:        JobVersion1,
					TaskID:            fmt.Sprintf("task-bench-%d-%d", count, i),
					SessionID:         fmt.Sprintf("session-bench-%d-%d", count, i),
					SourceType:        engine.SourceTypeZip,
					StagedArchivePath: zipPath,
				}
				if err := q.Push(job); err != nil {
					b.Fatalf("push job: %v", err)
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() {
				_ = w.Start(ctx)
			}()

			b.ResetTimer()
			b.ReportAllocs()

			// Wait until all b.N jobs are completed
			for {
				if len(q.results) >= b.N {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}

			b.StopTimer()
			cancel()
			_ = w.Stop(500 * time.Millisecond)
		})
	}
}

func BenchmarkWorkerCancellationLatency(b *testing.B) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	zipPath := createBenchmarkRepoZip(b, 100)

	b.ResetTimer()
	b.ReportAllocs()

	var totalLatency time.Duration
	var runs int64

	for i := 0; i < b.N; i++ {
		q := NewMemoryJobQueue(10)
		cfg := DefaultWorkerConfig()
		cfg.Concurrency = 1
		cfg.SandboxBaseDir = b.TempDir()

		w, err := NewAnalyzerWorker(cfg, q)
		if err != nil {
			b.Fatalf("new worker: %v", err)
		}

		job := &AnalyzerJob{
			JobVersion:        JobVersion1,
			TaskID:            fmt.Sprintf("task-cancel-%d", i),
			SessionID:         fmt.Sprintf("session-cancel-%d", i),
			SourceType:        engine.SourceTypeZip,
			StagedArchivePath: zipPath,
		}
		_ = q.Push(job)

		ctx, cancel := context.WithCancel(context.Background())

		workerStarted := make(chan struct{})
		go func() {
			close(workerStarted)
			_ = w.Start(ctx)
		}()

		<-workerStarted
		time.Sleep(2 * time.Millisecond) // Let job pick up

		start := time.Now()
		cancel()
		_ = w.Stop(500 * time.Millisecond)
		elapsed := time.Since(start)

		totalLatency += elapsed
		atomic.AddInt64(&runs, 1)
	}

	if runs > 0 {
		b.ReportMetric(float64(totalLatency.Milliseconds())/float64(runs), "ms/cancel")
	}
}

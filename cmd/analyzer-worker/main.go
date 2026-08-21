package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"pdfnest-backend/internal/analyzer/worker"
)

func main() {
	_ = godotenv.Load()

	cfg := worker.DefaultWorkerConfig()

	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		cfg.RedisURL = redisURL
	}
	if queueName := os.Getenv("ANALYZER_QUEUE"); queueName != "" {
		cfg.QueueName = queueName
	}
	if concurrencyStr := os.Getenv("ANALYZER_CONCURRENCY"); concurrencyStr != "" {
		if c, err := strconv.Atoi(concurrencyStr); err == nil && c > 0 {
			cfg.Concurrency = c
		}
	}
	if timeoutStr := os.Getenv("ANALYZER_JOB_TIMEOUT"); timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			cfg.JobTimeout = d
		}
	}
	if baseDir := os.Getenv("ANALYZER_SANDBOX_BASE_DIR"); baseDir != "" {
		cfg.SandboxBaseDir = baseDir
	}

	queue, err := worker.NewRedisJobQueue(cfg)
	if err != nil {
		log.Fatalf("[Analyzer Worker Daemon] Failed to initialize Redis queue: %v", err)
	}

	analyzerWorker, err := worker.NewAnalyzerWorker(cfg, queue)
	if err != nil {
		log.Fatalf("[Analyzer Worker Daemon] Failed to initialize worker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful termination signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("[Analyzer Worker Daemon] Caught signal %v, stopping...", sig)
		cancel()
		_ = analyzerWorker.Stop(cfg.ShutdownTimeout)
	}()

	log.Printf("[Analyzer Worker Daemon] Starting Go Analyzer Worker daemon v1.0.0...")
	if err := analyzerWorker.Start(ctx); err != nil && err != context.Canceled {
		log.Printf("[Analyzer Worker Daemon] Worker exited with error: %v", err)
	} else {
		log.Printf("[Analyzer Worker Daemon] Worker exited cleanly")
	}
}

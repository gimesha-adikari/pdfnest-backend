package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"pdfnest-backend/config"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

const (
	TaskKeyPrefix = "pdfnest:tasks:"
	TaskTTL       = 1 * time.Hour
)

type TaskStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	ResultURL string `json:"resultUrl,omitempty"`
	Error     string `json:"error,omitempty"`
}

type TaskRegistry struct {
	client *redis.Client
}

var Registry = &TaskRegistry{}

func (r *TaskRegistry) getClient() *redis.Client {
	if r.client != nil {
		return r.client
	}
	return config.Redis
}

func (r *TaskRegistry) Get(id string) (*TaskStatus, error) {
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)
	val, err := client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read task state from redis: %w", err)
	}

	var status TaskStatus
	if err := json.Unmarshal([]byte(val), &status); err != nil {
		return nil, fmt.Errorf("failed to unmarshal task status JSON: %w", err)
	}

	return &status, nil
}

func (r *TaskRegistry) Set(id string, status string, progress int, resultURL string, errStr string) error {
	client := r.getClient()
	if client == nil {
		log.Printf("[TASK REGISTRY ERROR] Redis client not configured for Set task %s", id)
		return errors.New("redis client not configured")
	}

	task := &TaskStatus{
		ID:        id,
		Status:    status,
		Progress:  progress,
		ResultURL: resultURL,
		Error:     errStr,
	}

	data, err := json.Marshal(task)
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Failed to marshal task %s: %v", id, err)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)
	if err := client.Set(ctx, key, string(data), TaskTTL).Err(); err != nil {
		log.Printf("[TASK REGISTRY ERROR] Redis Set failed for task %s: %v", id, err)
		return err
	}

	return nil
}

func getTaskProgress(id string) *TaskStatus {
	task, err := Registry.Get(id)
	if err != nil {
		return &TaskStatus{ID: id, Status: "FAILED", Progress: 0, Error: "Task storage service unavailable"}
	}
	if task == nil {
		return &TaskStatus{ID: id, Status: "FAILED", Progress: 0, Error: "Task not found"}
	}
	return task
}

func handleGetTaskStatus(c *fiber.Ctx) error {
	id := c.Params("id")
	task, err := Registry.Get(id)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "TASK_STORAGE_UNAVAILABLE",
			"message": "Task persistence service is temporarily unavailable. Please retry your request.",
		})
	}
	if task == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Task not found"})
	}
	return c.JSON(task)
}

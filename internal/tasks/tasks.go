package tasks

import (
	"sync"

	"github.com/gofiber/fiber/v2"
)

type TaskStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	ResultURL string `json:"resultUrl,omitempty"`
	Error     string `json:"error,omitempty"`
}

type TaskRegistry struct {
	mu    sync.RWMutex
	tasks map[string]*TaskStatus
}

var Registry = &TaskRegistry{
	tasks: make(map[string]*TaskStatus),
}

func (r *TaskRegistry) Get(id string) *TaskStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if task, exists := r.tasks[id]; exists {
		return &TaskStatus{
			ID:        task.ID,
			Status:    task.Status,
			Progress:  task.Progress,
			ResultURL: task.ResultURL,
			Error:     task.Error,
		}
	}
	return nil
}

func (r *TaskRegistry) Set(id string, status string, progress int, resultURL string, errStr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[id] = &TaskStatus{
		ID:        id,
		Status:    status,
		Progress:  progress,
		ResultURL: resultURL,
		Error:     errStr,
	}
}

func getTaskProgress(id string) *TaskStatus {
	task := Registry.Get(id)
	if task == nil {
		return &TaskStatus{ID: id, Status: "FAILED", Progress: 0, Error: "Task not found"}
	}
	return task
}

func handleGetTaskStatus(c *fiber.Ctx) error {
	id := c.Params("id")
	task := Registry.Get(id)
	if task == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Task not found"})
	}
	return c.JSON(task)
}

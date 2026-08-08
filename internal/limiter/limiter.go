package limiter

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
)

var ErrCapacityExhausted = errors.New("server capacity exhausted")

type Governor struct {
	mu       sync.Mutex
	capacity int
	active   int
}

func GetEnvInt(key string, defaultValue int) int {
	valStr := strings.TrimSpace(os.Getenv(key))
	if valStr == "" {
		return defaultValue
	}
	val, err := strconv.Atoi(valStr)
	if err != nil || val <= 0 {
		return defaultValue
	}
	return val
}

func NewGovernor() *Governor {
	cap := GetEnvInt("MAX_CONCURRENT_HEAVY_JOBS", 4)
	return &Governor{
		capacity: cap,
		active:   0,
	}
}

func NewGovernorWithCapacity(capacity int) *Governor {
	if capacity <= 0 {
		capacity = 4
	}
	return &Governor{
		capacity: capacity,
		active:   0,
	}
}

var Default = NewGovernor()

func (g *Governor) TryAcquire() (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.active >= g.capacity {
		return nil, false
	}

	g.active++
	var once sync.Once
	releaseFunc := func() {
		once.Do(func() {
			g.mu.Lock()
			if g.active > 0 {
				g.active--
			}
			g.mu.Unlock()
		})
	}
	return releaseFunc, true
}

func (g *Governor) ActiveCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

func (g *Governor) Capacity() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capacity
}

func (g *Governor) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		release, ok := g.TryAcquire()
		if !ok {
			c.Set("Retry-After", "5")
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"code":        "SERVER_BUSY",
				"message":     "Server processing capacity reached. Please try again in a few seconds.",
				"retry_after": 5,
			})
		}
		defer release()
		return c.Next()
	}
}

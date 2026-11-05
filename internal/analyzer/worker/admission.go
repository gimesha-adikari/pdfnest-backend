package worker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// This admission boundary is shared by every worker and direct pipeline caller
// in ONE process. Multiple worker processes need an independent distributed
// admission authority; this map is never evidence for a fleet-wide limit.
type admission struct {
	mu      sync.Mutex
	active  int
	users   map[string]int
	changed chan struct{}
}

var pipelineAdmission = admission{users: make(map[string]int), changed: make(chan struct{})}

func admissionLimit(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 16 || strconv.Itoa(n) != value {
		return 0, fmt.Errorf("%s must be an integer from 1 to 16", name)
	}
	return n, nil
}

func (a *admission) acquire(ctx context.Context, owner string) (func(), error) {
	global, err := admissionLimit("ANALYZER_MAX_ACTIVE_JOBS", 4)
	if err != nil {
		return nil, err
	}
	perUser, err := admissionLimit("ANALYZER_MAX_ACTIVE_JOBS_PER_USER", 1)
	if err != nil {
		return nil, err
	}
	if perUser > global {
		return nil, fmt.Errorf("per-user analyzer concurrency exceeds global concurrency")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if ctx.Err() == nil && a.active < global && a.users[owner] < perUser {
			a.active++
			a.users[owner]++
			a.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					a.mu.Lock()
					defer a.mu.Unlock()
					a.active--
					a.users[owner]--
					if a.users[owner] == 0 {
						delete(a.users, owner)
					}
					close(a.changed)
					a.changed = make(chan struct{})
				})
			}, nil
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

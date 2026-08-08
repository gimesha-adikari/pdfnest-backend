package billing

import (
	"context"
	"encoding/json"
	"log"
	"pdfnest-backend/config"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type TaskStatusRef struct {
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

func StartJanitorSweeper(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		log.Printf("[BILLING JANITOR] Automated background reservation sweeper initialized (interval: %v)", interval)
		for range ticker.C {
			SweepAbandonedReservations(30 * time.Minute)
		}
	}()
}

func SweepAbandonedReservations(abandonedThreshold time.Duration) {
	if config.DB == nil {
		return
	}

	cutoff := time.Now().Add(-abandonedThreshold)
	var candidateReservations []config.BillingReservation

	err := config.DB.Where("status = ? AND created_at < ?", "reserved", cutoff).
		Limit(100).
		Find(&candidateReservations).Error
	if err != nil || len(candidateReservations) == 0 {
		return
	}

	redisClient := config.Redis
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	releasedCount := 0
	for _, res := range candidateReservations {
		taskID := strings.TrimSpace(res.TaskID)
		shouldRelease := false

		if taskID == "" || redisClient == nil {
			shouldRelease = true
		} else {
			redisKey := "pdfnest:tasks:" + taskID
			valStr, err := redisClient.Get(ctx, redisKey).Result()
			if err == redis.Nil || valStr == "" {
				shouldRelease = true
			} else if err == nil {
				var statusRef TaskStatusRef
				if err := json.Unmarshal([]byte(valStr), &statusRef); err == nil {
					now := time.Now().Unix()
					if statusRef.Status == "COMPLETED" {
						shouldRelease = false
					} else if statusRef.Status == "FAILED" {
						shouldRelease = true
					} else if (statusRef.Status == "PROCESSING" || statusRef.Status == "PENDING") && (now-statusRef.UpdatedAt) >= 900 {
						shouldRelease = true
					} else {
						// Healthy task (<15m inactive)
						shouldRelease = false
					}
				}
			}
		}

		if shouldRelease {
			if err := Default.Release(res.ID); err == nil {
				releasedCount++
			}
		}
	}

	if releasedCount > 0 {
		log.Printf("[BILLING JANITOR] Successfully swept and released %d abandoned reservations", releasedCount)
	}
}

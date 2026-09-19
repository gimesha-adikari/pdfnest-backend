package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"pdfnest-backend/config"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

const MasterAdminEmail = "gimeshaadikari23@gmail.com"

const maxAdminOperationKeyLength = 128

var (
	errAdminTargetNotFound        = errors.New("admin target user not found")
	errAdminIdempotencyConflict   = errors.New("admin idempotency key reused with a different request")
	errAdminIdempotencyInProgress = errors.New("admin idempotency operation is still processing")
)

type updateUserTierRequest struct {
	Tier          string `json:"tier"`
	Status        string `json:"status"`
	CustomCredits int    `json:"custom_credits"`
	DaysToPlus    int    `json:"days_to_plus"`
}

type adminSubscriptionMutationResult struct {
	Tier          string
	Status        string
	CustomCredits int
}

func adminOperationKey(c *fiber.Ctx) string {
	key := strings.TrimSpace(c.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(c.Get("X-Idempotency-Key"))
	}
	return key
}

func adminMutationFingerprint(targetID string, req updateUserTierRequest) string {
	payload := struct {
		TargetID      string `json:"target_id"`
		Tier          string `json:"tier"`
		Status        string `json:"status"`
		CustomCredits int    `json:"custom_credits"`
		DaysToPlus    int    `json:"days_to_plus"`
	}{
		TargetID:      targetID,
		Tier:          req.Tier,
		Status:        req.Status,
		CustomCredits: req.CustomCredits,
		DaysToPlus:    req.DaysToPlus,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (ctrl *Controller) ListUsers(c *fiber.Ctx) error {
	var users []config.User
	if err := config.DB.Select("id, email, role, status, created_at").Order("created_at desc").Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed scanning user indexes"})
	}
	return c.JSON(users)
}

func (ctrl *Controller) ToggleBanUser(c *fiber.Ctx) error {
	targetID := c.Params("id")
	var user config.User

	if err := config.DB.First(&user, "id = ?", targetID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "User not found"})
	}

	if user.Email == MasterAdminEmail {
		return c.Status(403).JSON(fiber.Map{"error": "Security Restriction: Master Administrator account cannot be suspended."})
	}

	if user.Status == "active" {
		user.Status = "banned"
	} else {
		user.Status = "active"
	}

	config.DB.Save(&user)
	return c.JSON(fiber.Map{"status": "success", "updated_status": user.Status})
}

func (ctrl *Controller) UpdateUserRole(c *fiber.Ctx) error {
	targetID := c.Params("id")
	var req struct {
		Role string `json:"role"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid payload"})
	}

	if req.Role != "user" && req.Role != "admin" {
		return c.Status(400).JSON(fiber.Map{"error": "Role must be 'user' or 'admin'"})
	}

	var user config.User
	if err := config.DB.First(&user, "id = ?", targetID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "User not found"})
	}

	if user.Email == MasterAdminEmail {
		return c.Status(403).JSON(fiber.Map{"error": "Security Restriction: Master Administrator role privileges cannot be altered."})
	}

	user.Role = req.Role
	config.DB.Save(&user)

	return c.JSON(fiber.Map{"status": "success", "new_role": user.Role})
}

func (ctrl *Controller) ListSubscriptions(c *fiber.Ctx) error {
	var subscriptions []config.Subscription
	if err := config.DB.Order("created_at desc").Find(&subscriptions).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to retrieve subscriptions"})
	}
	return c.JSON(subscriptions)
}

func (ctrl *Controller) UpdateUserTier(c *fiber.Ctx) error {
	targetID := c.Params("id")
	operationKey := adminOperationKey(c)
	if operationKey == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":  "IDEMPOTENCY_KEY_REQUIRED",
			"error": "Idempotency-Key is required for admin subscription mutations",
		})
	}
	if len(operationKey) > maxAdminOperationKeyLength {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":  "IDEMPOTENCY_KEY_INVALID",
			"error": "Idempotency-Key is too long",
		})
	}

	var req updateUserTierRequest

	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid payload"})
	}
	if req.CustomCredits < 0 || req.DaysToPlus < 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":  "INVALID_ADMIN_SUBSCRIPTION_MUTATION",
			"error": "Credit grants and added time must not be negative",
		})
	}

	fingerprint := adminMutationFingerprint(targetID, req)
	var adminUserID *string
	if actorID, ok := c.Locals("user_id").(string); ok && strings.TrimSpace(actorID) != "" {
		actorID = strings.TrimSpace(actorID)
		adminUserID = &actorID
	}

	var result adminSubscriptionMutationResult
	isReplay := false
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		mutation := config.AdminSubscriptionMutation{
			ID:                 uuid.NewString(),
			OperationKey:       operationKey,
			UserID:             targetID,
			AdminUserID:        adminUserID,
			RequestFingerprint: fingerprint,
			RequestedTier:      req.Tier,
			RequestedStatus:    req.Status,
			RequestedCredits:   req.CustomCredits,
			RequestedDays:      req.DaysToPlus,
			State:              "processing",
			CreatedAt:          now,
			UpdatedAt:          now,
		}

		claim := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "operation_key"}},
			DoNothing: true,
		}).Create(&mutation)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			var existing config.AdminSubscriptionMutation
			if err := tx.Where("operation_key = ?", operationKey).First(&existing).Error; err != nil {
				return err
			}
			if existing.UserID != targetID || existing.RequestFingerprint != fingerprint {
				return errAdminIdempotencyConflict
			}
			if existing.State != "completed" {
				return errAdminIdempotencyInProgress
			}
			result = adminSubscriptionMutationResult{
				Tier:          existing.ResultTier,
				Status:        existing.ResultStatus,
				CustomCredits: existing.ResultCustomCredits,
			}
			isReplay = true
			return nil
		}

		var target config.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&target, "id = ?", targetID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errAdminTargetNotFound
			}
			return err
		}

		var sub config.Subscription
		subErr := tx.Where("user_id = ?", targetID).First(&sub).Error
		if subErr != nil && !errors.Is(subErr, gorm.ErrRecordNotFound) {
			return subErr
		}
		if errors.Is(subErr, gorm.ErrRecordNotFound) {
			sub = config.Subscription{
				ID:               uuid.NewString(),
				UserID:           targetID,
				Tier:             "free",
				Status:           "active",
				CurrentPeriodEnd: now,
				CreatedAt:        now,
			}
		}

		if req.Tier != "" {
			sub.Tier = req.Tier
		}
		if req.Status != "" {
			sub.Status = req.Status
		}
		// The endpoint remains additive: each new operation key represents a
		// separate intentional grant. Duplicate execution is stopped by the
		// durable operation claim above.
		sub.CustomCredits += req.CustomCredits
		if req.DaysToPlus > 0 {
			sub.CurrentPeriodEnd = now.AddDate(0, 0, req.DaysToPlus)
		}
		sub.UpdatedAt = now

		if err := tx.Save(&sub).Error; err != nil {
			return err
		}

		mutation.State = "completed"
		mutation.ResultTier = sub.Tier
		mutation.ResultStatus = sub.Status
		mutation.ResultCustomCredits = sub.CustomCredits
		mutation.UpdatedAt = time.Now().UTC()
		if err := tx.Model(&mutation).Updates(map[string]interface{}{
			"state":                 mutation.State,
			"result_tier":           mutation.ResultTier,
			"result_status":         mutation.ResultStatus,
			"result_custom_credits": mutation.ResultCustomCredits,
			"updated_at":            mutation.UpdatedAt,
		}).Error; err != nil {
			return err
		}

		result = adminSubscriptionMutationResult{
			Tier:          sub.Tier,
			Status:        sub.Status,
			CustomCredits: sub.CustomCredits,
		}
		return nil
	})
	if errors.Is(err, errAdminTargetNotFound) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
	}
	if errors.Is(err, errAdminIdempotencyConflict) {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"code":  "IDEMPOTENCY_KEY_REUSE_WITH_DIFFERENT_PAYLOAD",
			"error": "The provided Idempotency-Key was previously used for a different request payload.",
		})
	}
	if errors.Is(err, errAdminIdempotencyInProgress) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"code":  "IDEMPOTENCY_REQUEST_IN_PROGRESS",
			"error": "A request with this Idempotency-Key is already being processed.",
		})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to process subscription row"})
	}

	return c.JSON(fiber.Map{
		"status":            "success",
		"new_tier":          result.Tier,
		"custom_credits":    result.CustomCredits,
		"idempotent_replay": isReplay,
	})
}

func (ctrl *Controller) GetUserDetails(c *fiber.Ctx) error {
	targetID := c.Params("id")

	var user config.User
	if err := config.DB.Select("id, email, role, status, created_at").First(&user, "id = ?", targetID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "User profile not found"})
	}

	var subscription config.Subscription
	config.DB.Where("user_id = ?", targetID).Limit(1).Find(&subscription)

	var transactions []config.Transaction
	config.DB.Where("user_id = ?", targetID).Order("created_at desc").Find(&transactions)

	var usageLogs []config.UsageLog
	config.DB.Where("user_id = ?", targetID).Order("created_at desc").Limit(100).Find(&usageLogs)

	return c.JSON(fiber.Map{
		"user":         user,
		"subscription": subscription,
		"transactions": transactions,
		"usage_logs":   usageLogs,
	})
}

func (ctrl *Controller) GetDashboardMetrics(c *fiber.Ctx) error {
	var toolUsage []struct {
		ToolName string `json:"tool_name"`
		Count    int    `json:"count"`
	}
	config.DB.Model(&config.UsageLog{}).
		Select("tool_name, count(*) as count").
		Group("tool_name").
		Scan(&toolUsage)

	var dailyTrend []struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
	}
	config.DB.Model(&config.UsageLog{}).
		Select("TO_CHAR(created_at, 'YYYY-MM-DD') as date, count(*) as count").
		Where("created_at >= ?", time.Now().AddDate(0, 0, -30)).
		Group("TO_CHAR(created_at, 'YYYY-MM-DD')").
		Order("date ASC").
		Scan(&dailyTrend)

	var totalRevenue float64
	config.DB.Model(&config.Transaction{}).Select("COALESCE(sum(amount), 0)").Scan(&totalRevenue)

	var totalUsers int64
	config.DB.Model(&config.User{}).Count(&totalUsers)

	var subDistribution []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	config.DB.Model(&config.Subscription{}).
		Select("status, count(*) as count").
		Group("status").
		Scan(&subDistribution)

	return c.JSON(fiber.Map{
		"tool_usage":       toolUsage,
		"daily_trend":      dailyTrend,
		"total_revenue":    totalRevenue,
		"total_users":      totalUsers,
		"sub_distribution": subDistribution,
	})
}

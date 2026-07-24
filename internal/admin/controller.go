package admin

import (
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

const MasterAdminEmail = "gimeshaadikari23@gmail.com"

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

	var req struct {
		Tier          string `json:"tier"`
		Status        string `json:"status"`
		CustomCredits int    `json:"custom_credits"`
		DaysToPlus    int    `json:"days_to_plus"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid payload"})
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", targetID).FirstOrInit(&sub).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to process subscription row"})
	}

	if sub.ID == "" {
		sub.ID = uuid.New().String()
		sub.UserID = targetID
		sub.CreatedAt = time.Now()
	}

	if req.Tier != "" {
		sub.Tier = req.Tier
	}
	if req.Status != "" {
		sub.Status = req.Status
	}
	sub.CustomCredits += req.CustomCredits

	if req.DaysToPlus > 0 {
		sub.CurrentPeriodEnd = time.Now().AddDate(0, 0, req.DaysToPlus)
	}

	sub.UpdatedAt = time.Now()

	config.DB.Save(&sub)
	return c.JSON(fiber.Map{"status": "success", "new_tier": sub.Tier, "custom_credits": sub.CustomCredits})
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

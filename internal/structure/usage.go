package structure

import (
	"strings"

	"pdfnest-backend/config"
	"pdfnest-backend/helper"

	"github.com/gofiber/fiber/v2"
)

func logUsageTimes(c *fiber.Ctx, userID, usageKey string, times int) {
	if times <= 0 {
		return
	}
	if strings.TrimSpace(userID) == "" {
		return
	}

	creditOK := helper.CheckCreditUsage(c)

	for i := 0; i < times; i++ {
		config.LogToolUsage(userID, usageKey, creditOK)
	}
}

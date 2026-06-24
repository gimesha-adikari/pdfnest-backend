package helper

import "github.com/gofiber/fiber/v2"

func CheckCreditUsage(c *fiber.Ctx) bool {
	if val, ok := c.Locals("consumed_via_credit").(bool); ok {
		return val
	}
	return false
}

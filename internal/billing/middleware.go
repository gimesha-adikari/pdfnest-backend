package billing

import (
	"errors"
	"log"
	"strings"

	"pdfnest-backend/internal/identity"

	"github.com/gofiber/fiber/v2"
)

func Use(tool Tool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasSuffix(path, "/markup/highlight") ||
			strings.HasSuffix(path, "/markup/strikeout") ||
			strings.HasSuffix(path, "/markup/underline") {
			return c.Next()
		}

		identityType, _ := c.Locals(identity.LocalIdentityType).(string)
		identityID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
		if strings.TrimSpace(identityID) == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "missing identity",
			})
		}

		if identityType == string(identity.TypeGuest) {
			if GuestQuota == nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error": "guest quota store not configured",
				})
			}

			pages, images, err := EstimateFromRequest(c, tool)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": err.Error(),
				})
			}

			ctx := identity.RequestContext(c)
			reservation, err := GuestQuota.Reserve(ctx, identityID, tool, pages, images, c.Path())
			if err != nil {
				var berr *BillingError
				if errors.As(err, &berr) {
					berr.Tool = tool.Name
					return c.Status(fiber.StatusTooManyRequests).JSON(berr)
				}

				return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
					"code":    "BILLING_ERROR",
					"title":   "Unable to process request",
					"message": err.Error(),
					"tool":    tool.Name,
				})
			}

			c.Locals("billing_reservation_id", reservation.ID)
			c.Locals("billing_tool", tool.Name)
			c.Locals("billing_kind", "guest")

			err = c.Next()
			if err != nil {
				_ = GuestQuota.Release(ctx, reservation.ID)
				return err
			}

			if c.Response().StatusCode() >= 400 {
				_ = GuestQuota.Release(ctx, reservation.ID)
				return nil
			}

			if err := GuestQuota.Commit(ctx, reservation.ID); err != nil {
				log.Printf("[BILLING] guest commit failed: %v", err)
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error": "failed to finalize guest usage",
				})
			}

			return nil
		}

		pages, images, err := EstimateFromRequest(c, tool)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": err.Error(),
			})
		}

		reservation, err := Default.Reserve(identityID, tool, pages, images, c.Path())
		if err != nil {
			log.Printf("[BILLING] reserve failed user=%s tool=%s path=%s err=%v", identityID, tool.Name, c.Path(), err)

			var berr *BillingError
			if errors.As(err, &berr) {
				berr.Tool = tool.Name
				return c.Status(fiber.StatusTooManyRequests).JSON(berr)
			}

			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"code":    "BILLING_ERROR",
				"title":   "Unable to process request",
				"message": err.Error(),
				"tool":    tool.Name,
			})
		}

		c.Locals("billing_reservation_id", reservation.ID)
		c.Locals("billing_tool", tool.Name)
		c.Locals("consumed_via_credit", reservation.CreditUnits > 0)

		err = c.Next()
		if err != nil {
			_ = Default.Release(reservation.ID)
			return err
		}

		if c.Response().StatusCode() >= 400 {
			_ = Default.Release(reservation.ID)
			return nil
		}

		if err := Default.Commit(reservation.ID); err != nil {
			_ = Default.Release(reservation.ID)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "failed to finalize billing",
			})
		}

		return nil
	}
}

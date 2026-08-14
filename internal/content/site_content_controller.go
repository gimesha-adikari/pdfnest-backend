package content

import (
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"time"

	"github.com/gofiber/fiber/v2"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

// ──────────────────────────────────────────────────────────────────────────────
// HOME PAGE
// ──────────────────────────────────────────────────────────────────────────────

// homeAllowedFields is the exhaustive whitelist of JSON keys that may be
// updated via the admin API. Any key not listed here is silently ignored.
var homeAllowedFields = map[string]struct{}{
	"heroBadgeGuest":         {},
	"heroBadgeFree":          {},
	"heroBadgePlus":          {},
	"heroBadgePro":           {},
	"heroWelcomeBack":        {},
	"heroTitleGuest":         {},
	"heroTitlePlus":          {},
	"heroTitlePro":           {},
	"heroSubtitleGuest":      {},
	"heroSubtitleGuestBold":  {},
	"authBannerProAccess":    {},
	"authBannerFreeUsage":    {},
	"authBannerFreeAction":   {},
	"feature1Title":          {},
	"feature1Description":    {},
	"feature2Title":          {},
	"feature2Description":    {},
	"feature3Title":          {},
	"feature3Description":    {},
	"searchPlaceholder":      {},
	"searchScopeSuffix":      {},
	"searchEmptyTitle":       {},
	"searchEmptyDescription": {},
	"popularToolTitle":       {},
	"popularToolDescription": {},
	"popularToolAction":      {},
	"categoryOrganizeTitle":  {},
	"categoryOrganizeDesc":   {},
	"categoryEditingTitle":   {},
	"categoryEditingDesc":    {},
	"categoryConvertTitle":   {},
	"categoryConvertDesc":    {},
	"categoryCreateTitle":    {},
	"categoryCreateDesc":     {},
	"categorySecurityTitle":  {},
	"categorySecurityDesc":   {},
	"categoryOptimizeTitle":  {},
	"categoryOptimizeDesc":   {},
	"categoryStudioTitle":    {},
	"categoryStudioDesc":     {},
}

// filterAllowed returns a new map containing only the keys present in the
// allowlist, plus the server-set updatedAt timestamp. Unknown keys are dropped.
func filterAllowed(src map[string]interface{}, allowed map[string]struct{}) map[string]interface{} {
	out := make(map[string]interface{}, len(allowed)+1)
	for k, v := range src {
		if _, ok := allowed[k]; ok {
			out[k] = v
		}
	}
	out["updatedAt"] = time.Now()
	return out
}

func (ctrl *Controller) GetHomePageContent(c *fiber.Ctx) error {
	var content models.HomePageContent
	if err := config.DB.First(&content, 1).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "home page content records missing"})
	}
	return c.JSON(content)
}

func (ctrl *Controller) UpdateHomePageContent(c *fiber.Ctx) error {
	var bodyMap map[string]interface{}
	if err := c.BodyParser(&bodyMap); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "malformed structural payload data"})
	}

	safeMap := filterAllowed(bodyMap, homeAllowedFields)

	var existing models.HomePageContent
	if err := config.DB.First(&existing, 1).Error; err != nil {
		// No record yet — seed from body via typed struct (safe: BodyParser maps
		// only known json-tagged fields, not arbitrary keys).
		var payload models.HomePageContent
		_ = c.BodyParser(&payload)
		payload.ID = 1
		payload.UpdatedAt = time.Now()
		if err := config.DB.Create(&payload).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "failed to write home configuration"})
		}
		return c.JSON(payload)
	}

	if err := config.DB.Model(&existing).Updates(safeMap).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "failed to write home configuration override updates"})
	}

	var refreshed models.HomePageContent
	config.DB.First(&refreshed, 1)
	return c.JSON(refreshed)
}

// ──────────────────────────────────────────────────────────────────────────────
// SUBSCRIBE PAGE
// ──────────────────────────────────────────────────────────────────────────────

var subscribeAllowedFields = map[string]struct{}{
	"heroBadge":           {},
	"heroTitle":           {},
	"heroTitleGradient":   {},
	"heroSubtitle":        {},
	"premiumSectionTitle": {},
	"studioTitle":         {},
	"studioDescription":   {},
	"studioBulletPoints":  {},
	"canvasTitle":         {},
	"canvasDescription":   {},
	"canvasBulletPoints":  {},
	"speedTitle":          {},
	"speedDescription":    {},
	"speedBulletPoints":   {},
	"freeTitle":           {},
	"freePrice":           {},
	"freeSubtitle":        {},
	"freeBulletPoints":    {},
	"plusTitle":           {},
	"plusMonthlyPrice":    {},
	"plusYearlyPrice":     {},
	"plusSubtitle":        {},
	"plusBulletPoints":    {},
	"proTitle":            {},
	"proMonthlyPrice":     {},
	"proYearlyPrice":      {},
	"proSubtitle":         {},
	"proBulletPoints":     {},
	"trialText":           {},
	"securityTitle":       {},
	"securitySubtitle":    {},
	"securityTags":        {},
	"ctaGuestTitle":       {},
	"ctaFreeTitle":        {},
	"ctaFreeSubtitle":     {},
	"ctaPlusTitle":        {},
	"ctaPlusSubtitle":     {},
	"ctaProTitle":         {},
	"ctaProSubtitle":      {},
	"faqsJson":            {},
}

func (ctrl *Controller) GetSubscribePageContent(c *fiber.Ctx) error {
	var content models.SubscribePageContent
	if err := config.DB.First(&content, 1).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "subscription template definitions missing"})
	}
	return c.JSON(content)
}

func (ctrl *Controller) UpdateSubscribePageContent(c *fiber.Ctx) error {
	var bodyMap map[string]interface{}
	if err := c.BodyParser(&bodyMap); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "malformed structural payload data"})
	}

	safeMap := filterAllowed(bodyMap, subscribeAllowedFields)

	var existing models.SubscribePageContent
	if err := config.DB.First(&existing, 1).Error; err != nil {
		var payload models.SubscribePageContent
		_ = c.BodyParser(&payload)
		payload.ID = 1
		payload.UpdatedAt = time.Now()
		if err := config.DB.Create(&payload).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "failed to write subscription matrix configuration"})
		}
		return c.JSON(payload)
	}

	if err := config.DB.Model(&existing).Updates(safeMap).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "failed to write subscription matrix configuration updates"})
	}

	var refreshed models.SubscribePageContent
	config.DB.First(&refreshed, 1)
	return c.JSON(refreshed)
}

// ──────────────────────────────────────────────────────────────────────────────
// ABOUT PAGE
// ──────────────────────────────────────────────────────────────────────────────

var aboutAllowedFields = map[string]struct{}{
	"heroTag":             {},
	"heroTitle":           {},
	"heroDescription":     {},
	"statsJson":           {},
	"sectionTitle":        {},
	"sectionSubtitle":     {},
	"highlightsJson":      {},
	"studioTitle":         {},
	"studioDescription":   {},
	"studioFeaturesJson":  {},
	"canvasTitle":         {},
	"canvasDescription":   {},
	"canvasFeaturesJson":  {},
	"securityTitle":       {},
	"securityDescription": {},
	"roadmapTitle":        {},
	"roadmapDescription":  {},
	"roadmapJson":         {},
	"missionTitle":        {},
	"missionDescription":  {},
}

func (ctrl *Controller) GetAboutPageContent(c *fiber.Ctx) error {
	var content models.AboutPageContent
	if err := config.DB.First(&content, 1).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "about page content records missing"})
	}
	return c.JSON(content)
}

func (ctrl *Controller) UpdateAboutPageContent(c *fiber.Ctx) error {
	var bodyMap map[string]interface{}
	if err := c.BodyParser(&bodyMap); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "malformed structural payload data"})
	}

	safeMap := filterAllowed(bodyMap, aboutAllowedFields)

	var existing models.AboutPageContent
	if err := config.DB.First(&existing, 1).Error; err != nil {
		var payload models.AboutPageContent
		_ = c.BodyParser(&payload)
		payload.ID = 1
		payload.CreatedAt = time.Now()
		payload.UpdatedAt = time.Now()
		if err := config.DB.Create(&payload).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "failed to write about configuration"})
		}
		return c.JSON(payload)
	}

	if err := config.DB.Model(&existing).Updates(safeMap).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "failed to write about configuration override updates"})
	}

	var refreshed models.AboutPageContent
	config.DB.First(&refreshed, 1)
	return c.JSON(refreshed)
}

package content

import (
	"encoding/json"
	"fmt"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type ToolController struct{}

func NewToolController() *ToolController {
	return &ToolController{}
}

// Allowed tool categories
var allowedCategories = map[string]bool{
	"organize": true,
	"edit":     true,
	"convert":  true,
	"create":   true,
	"security": true,
	"optimize": true,
	"studio":   true,
}

func normalizeHref(href string) string {
	cleaned := strings.TrimSpace(href)
	if cleaned == "" {
		return ""
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	if len(cleaned) > 1 && strings.HasSuffix(cleaned, "/") {
		cleaned = strings.TrimSuffix(cleaned, "/")
	}
	return cleaned
}

func validateCategory(cat string) bool {
	if cat == "" {
		return true // Optional in partial updates, handled at creation
	}
	return allowedCategories[strings.ToLower(strings.TrimSpace(cat))]
}

func validateJSONString(jsonStr string) bool {
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" || trimmed == "[]" || trimmed == "{}" {
		return true
	}
	return json.Valid([]byte(trimmed))
}

func normalizeMapKeys(rawMap map[string]interface{}) map[string]interface{} {
	normalized := make(map[string]interface{})

	for k, v := range rawMap {
		lower := strings.ToLower(k)

		switch lower {
		case "id":
			normalized["id"] = v
		case "href", "slug":
			if strVal, ok := v.(string); ok {
				normalized["href"] = normalizeHref(strVal)
			} else {
				normalized["href"] = v
			}
		case "title", "name":
			normalized["title"] = v
		case "description":
			normalized["description"] = v
		case "category":
			if strVal, ok := v.(string); ok {
				normalized["category"] = strings.ToLower(strings.TrimSpace(strVal))
			} else {
				normalized["category"] = v
			}
		case "keywordsjson", "keywords":
			if strSlice, ok := v.([]interface{}); ok {
				bytes, _ := json.Marshal(strSlice)
				normalized["keywordsJson"] = string(bytes)
			} else {
				normalized["keywordsJson"] = v
			}
		case "seotitle":
			normalized["seoTitle"] = v
		case "seodescription":
			normalized["seoDescription"] = v
		case "intent":
			normalized["intent"] = v
		case "relatedjson", "related":
			if strSlice, ok := v.([]interface{}); ok {
				bytes, _ := json.Marshal(strSlice)
				normalized["relatedJson"] = string(bytes)
			} else {
				normalized["relatedJson"] = v
			}
		case "faqjson", "faq":
			if sliceVal, ok := v.([]interface{}); ok {
				bytes, _ := json.Marshal(sliceVal)
				normalized["faqJson"] = string(bytes)
			} else {
				normalized["faqJson"] = v
			}
		case "featuresjson", "features":
			if strSlice, ok := v.([]interface{}); ok {
				bytes, _ := json.Marshal(strSlice)
				normalized["featuresJson"] = string(bytes)
			} else {
				normalized["featuresJson"] = v
			}
		case "isnew":
			normalized["isNew"] = v
		case "accept":
			normalized["accept"] = v
		case "multiple":
			normalized["multiple"] = v
		case "iconname", "icon":
			normalized["iconName"] = v
		case "sortorder":
			normalized["sortOrder"] = v
		case "isactive", "is_active":
			normalized["isActive"] = v
		case "createdat", "created_at":
			// Preserve existing
		case "updatedat", "updated_at":
			// Handled automatically
		default:
			normalized[k] = v
		}
	}

	return normalized
}

func (ctrl *ToolController) GetPublicTools(c *fiber.Ctx) error {
	var tools []models.DynamicToolItem
	err := config.DB.Where("is_active = ?", true).Order("sort_order asc, id asc").Find(&tools).Error

	if err != nil && err != gorm.ErrRecordNotFound {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to read platform modules matrix layout profiles"})
	}

	if tools == nil {
		tools = []models.DynamicToolItem{}
	}

	return c.JSON(tools)
}

func validateToolMap(m map[string]interface{}, isCreation bool) error {
	href, _ := m["href"].(string)
	cleanHref := normalizeHref(href)
	if cleanHref == "" {
		return fmt.Errorf("tool href is required and cannot be empty")
	}

	title, _ := m["title"].(string)
	if isCreation && strings.TrimSpace(title) == "" {
		return fmt.Errorf("tool title is mandatory for tool %s", cleanHref)
	}

	category, _ := m["category"].(string)
	if category != "" && !validateCategory(category) {
		return fmt.Errorf("invalid category '%s' for tool %s", category, cleanHref)
	}

	jsonFields := []string{"keywordsJson", "relatedJson", "faqJson", "featuresJson"}
	for _, field := range jsonFields {
		if rawVal, exists := m[field]; exists {
			if strVal, ok := rawVal.(string); ok {
				if !validateJSONString(strVal) {
					return fmt.Errorf("invalid JSON content in '%s' for tool %s", field, cleanHref)
				}
			}
		}
	}

	return nil
}

func (ctrl *ToolController) UpdateToolConfiguration(c *fiber.Ctx) error {
	var rawMap map[string]interface{}
	if err := c.BodyParser(&rawMap); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Malformed JSON payload"})
	}

	updatesMap := normalizeMapKeys(rawMap)

	hrefVal, _ := updatesMap["href"].(string)
	cleanHref := normalizeHref(hrefVal)
	if cleanHref == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Href is required and cannot be empty"})
	}
	updatesMap["href"] = cleanHref

	var existing models.DynamicToolItem
	err := config.DB.Where("href = ?", cleanHref).First(&existing).Error
	isCreation := err != nil

	if validationErr := validateToolMap(updatesMap, isCreation); validationErr != nil {
		return c.Status(400).JSON(fiber.Map{"error": validationErr.Error()})
	}

	updatesMap["updatedAt"] = time.Now()

	if !isCreation {
		delete(updatesMap, "id")
		delete(updatesMap, "createdAt")
		delete(updatesMap, "created_at")

		if err := config.DB.Model(&existing).Updates(updatesMap).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed updating tool specifications"})
		}

		var refreshed models.DynamicToolItem
		config.DB.Where("id = ?", existing.ID).First(&refreshed)
		return c.JSON(refreshed)
	}

	// Tool Creation flow
	newItem := models.DynamicToolItem{
		Href:        cleanHref,
		Title:       updatesMap["title"].(string),
		Description: "",
		Category:    "organize",
		IconName:    "FileText",
		IsActive:    true,
		SortOrder:   0,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if cat, ok := updatesMap["category"].(string); ok && cat != "" {
		newItem.Category = cat
	}
	if desc, ok := updatesMap["description"].(string); ok {
		newItem.Description = desc
	}
	if kw, ok := updatesMap["keywordsJson"].(string); ok {
		newItem.KeywordsJson = kw
	}
	if st, ok := updatesMap["seoTitle"].(string); ok {
		newItem.SeoTitle = st
	}
	if sd, ok := updatesMap["seoDescription"].(string); ok {
		newItem.SeoDescription = sd
	}
	if intent, ok := updatesMap["intent"].(string); ok {
		newItem.Intent = intent
	}
	if rel, ok := updatesMap["relatedJson"].(string); ok {
		newItem.RelatedJson = rel
	}
	if faq, ok := updatesMap["faqJson"].(string); ok {
		newItem.FaqJson = faq
	}
	if feat, ok := updatesMap["featuresJson"].(string); ok {
		newItem.FeaturesJson = feat
	}
	if isNew, ok := updatesMap["isNew"].(bool); ok {
		newItem.IsNew = isNew
	}
	if accept, ok := updatesMap["accept"].(string); ok {
		newItem.Accept = accept
	}
	if mult, ok := updatesMap["multiple"].(bool); ok {
		newItem.Multiple = mult
	}
	if icon, ok := updatesMap["iconName"].(string); ok && icon != "" {
		newItem.IconName = icon
	}
	if sort, ok := updatesMap["sortOrder"].(float64); ok {
		newItem.SortOrder = int(sort)
	}
	if active, ok := updatesMap["isActive"].(bool); ok {
		newItem.IsActive = active
	}

	if err := config.DB.Create(&newItem).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed creating custom tool"})
	}

	return c.JSON(newItem)
}

func (ctrl *ToolController) BulkUpdateToolConfiguration(c *fiber.Ctx) error {
	var rawList []map[string]interface{}
	if err := c.BodyParser(&rawList); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Malformed bulk JSON array payload"})
	}

	if len(rawList) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Bulk payload cannot be empty"})
	}

	seenHrefs := make(map[string]bool)
	normalizedList := make([]map[string]interface{}, len(rawList))

	for i, rawItem := range rawList {
		normMap := normalizeMapKeys(rawItem)
		hrefVal, _ := normMap["href"].(string)
		cleanHref := normalizeHref(hrefVal)

		if cleanHref == "" {
			return c.Status(400).JSON(fiber.Map{"error": fmt.Sprintf("Item at index %d has empty href", i)})
		}

		if seenHrefs[cleanHref] {
			return c.Status(400).JSON(fiber.Map{"error": fmt.Sprintf("Duplicate href '%s' found in bulk payload", cleanHref)})
		}
		seenHrefs[cleanHref] = true
		normMap["href"] = cleanHref
		normalizedList[i] = normMap
	}

	// Load existing DB records for all hrefs
	var existingTools []models.DynamicToolItem
	existingMap := make(map[string]models.DynamicToolItem)
	if err := config.DB.Find(&existingTools).Error; err == nil {
		for _, t := range existingTools {
			existingMap[normalizeHref(t.Href)] = t
		}
	}

	for i, normMap := range normalizedList {
		cleanHref := normMap["href"].(string)
		_, exists := existingMap[cleanHref]
		if err := validateToolMap(normMap, !exists); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": fmt.Sprintf("Validation failed at index %d (%s): %v", i, cleanHref, err)})
		}
	}

	updatedCount := 0
	createdCount := 0

	txErr := config.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()

		for _, normMap := range normalizedList {
			cleanHref := normMap["href"].(string)
			existing, exists := existingMap[cleanHref]

			normMap["updatedAt"] = now

			if exists {
				delete(normMap, "id")
				delete(normMap, "createdAt")
				delete(normMap, "created_at")

				if err := tx.Model(&models.DynamicToolItem{}).Where("id = ?", existing.ID).Updates(normMap).Error; err != nil {
					return fmt.Errorf("failed updating tool %s: %w", cleanHref, err)
				}
				updatedCount++
			} else {
				newItem := models.DynamicToolItem{
					Href:         cleanHref,
					Title:        normMap["title"].(string),
					Description:  "",
					Category:     "organize",
					IconName:     "FileText",
					IsActive:     true,
					SortOrder:    0,
					KeywordsJson: "[]",
					RelatedJson:  "[]",
					FaqJson:      "[]",
					FeaturesJson: "[]",
					CreatedAt:    now,
					UpdatedAt:    now,
				}

				if desc, ok := normMap["description"].(string); ok {
					newItem.Description = desc
				}
				if cat, ok := normMap["category"].(string); ok && cat != "" {
					newItem.Category = cat
				}
				if kw, ok := normMap["keywordsJson"].(string); ok {
					newItem.KeywordsJson = kw
				}
				if st, ok := normMap["seoTitle"].(string); ok {
					newItem.SeoTitle = st
				}
				if sd, ok := normMap["seoDescription"].(string); ok {
					newItem.SeoDescription = sd
				}
				if intent, ok := normMap["intent"].(string); ok {
					newItem.Intent = intent
				}
				if rel, ok := normMap["relatedJson"].(string); ok {
					newItem.RelatedJson = rel
				}
				if faq, ok := normMap["faqJson"].(string); ok {
					newItem.FaqJson = faq
				}
				if feat, ok := normMap["featuresJson"].(string); ok {
					newItem.FeaturesJson = feat
				}
				if isNew, ok := normMap["isNew"].(bool); ok {
					newItem.IsNew = isNew
				}
				if accept, ok := normMap["accept"].(string); ok {
					newItem.Accept = accept
				}
				if mult, ok := normMap["multiple"].(bool); ok {
					newItem.Multiple = mult
				}
				if icon, ok := normMap["iconName"].(string); ok && icon != "" {
					newItem.IconName = icon
				}
				if sort, ok := normMap["sortOrder"].(float64); ok {
					newItem.SortOrder = int(sort)
				}
				if active, ok := normMap["isActive"].(bool); ok {
					newItem.IsActive = active
				}

				if err := tx.Create(&newItem).Error; err != nil {
					return fmt.Errorf("failed creating tool %s: %w", cleanHref, err)
				}
				createdCount++
			}
		}

		return nil
	})

	if txErr != nil {
		return c.Status(500).JSON(fiber.Map{"error": txErr.Error()})
	}

	var allTools []models.DynamicToolItem
	config.DB.Order("sort_order asc, id asc").Find(&allTools)

	return c.JSON(fiber.Map{
		"success": true,
		"updated": updatedCount,
		"created": createdCount,
		"total":   len(allTools),
		"tools":   allTools,
	})
}

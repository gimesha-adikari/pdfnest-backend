package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"pdfnest-backend/config"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type createCheckoutRequest struct {
	Tier     string `json:"tier"`     // plus | pro
	Interval string `json:"interval"` // monthly | yearly
}

type paddleCheckoutItem struct {
	PriceID  string `json:"price_id"`
	Quantity int    `json:"quantity"`
}

type paddleCheckoutCreatePayload struct {
	Items      []paddleCheckoutItem `json:"items"`
	CustomData map[string]any       `json:"custom_data"`
}

type paddleCheckoutCreateResponse struct {
	Data struct {
		URL         string `json:"url"`
		CheckoutURL string `json:"checkout_url"`
	} `json:"data"`
	URL         string `json:"url"`
	CheckoutURL string `json:"checkout_url"`
}

func (ctrl *Controller) CreateCheckout(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	if strings.TrimSpace(userID) == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "missing authenticated user",
		})
	}

	var req createCheckoutRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body",
		})
	}

	req.Tier = strings.ToLower(strings.TrimSpace(req.Tier))
	if req.Tier != "plus" && req.Tier != "pro" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "tier must be plus or pro",
		})
	}

	priceID := priceIDForPlan(req.Tier, req.Interval)
	if strings.TrimSpace(priceID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing price id for selected plan",
		})
	}

	// Make sure a subscription row exists so the webhook can update it later.
	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		now := time.Now()
		sub = config.Subscription{
			ID:                   uuid.New().String(),
			UserID:               userID,
			PaddleCustomerID:     "pending-customer-" + uuid.New().String(),
			PaddleSubscriptionID: "pending-subscription-" + uuid.New().String(),
			Status:               "free",
			Tier:                 "free",
			CurrentPeriodEnd:     now,
			Window3HResetAt:      now.Add(3 * time.Hour),
			WindowDailyResetAt:   nextMidnight(now),
			WindowMonthlyResetAt: nextMonthStart(now),
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		if err := config.DB.Create(&sub).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "failed to prepare subscription record",
			})
		}
	}

	apiBase := strings.TrimRight(getEnv("PADDLE_API_BASE_URL", "https://api.paddle.com"), "/")
	checkoutPath := getEnv("PADDLE_CHECKOUT_CREATE_PATH", "/checkouts")
	apiKey := strings.TrimSpace(os.Getenv("PADDLE_API_KEY"))
	if apiKey == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "paddle api key not configured",
		})
	}

	payload := paddleCheckoutCreatePayload{
		Items: []paddleCheckoutItem{
			{PriceID: priceID, Quantity: 1},
		},
		CustomData: map[string]any{
			"user_id":          userID,
			"package_type":     req.Tier,
			"billing_interval": req.Interval,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to encode checkout payload",
		})
	}

	log.Println("[PADDLE] Payload:")
	log.Println(string(body))

	url := apiBase + checkoutPath
	log.Println("[PADDLE] URL:", url)

	httpReq, err := http.NewRequest(
		http.MethodPost,
		url,
		bytes.NewReader(body),
	)

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to build checkout request",
		})
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	httpRes, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "failed to contact paddle",
		})
	}
	defer httpRes.Body.Close()

	raw, _ := io.ReadAll(httpRes.Body)

	log.Printf("[PADDLE] Status: %d", httpRes.StatusCode)
	log.Printf("[PADDLE] Response: %s", string(raw))

	if httpRes.StatusCode < 200 || httpRes.StatusCode >= 300 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   "paddle checkout creation failed",
			"details": string(raw),
		})
	}

	var decoded paddleCheckoutCreateResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "failed to parse paddle response",
		})
	}

	checkoutURL := firstNonEmpty(
		decoded.Data.URL,
		decoded.Data.CheckoutURL,
		decoded.URL,
		decoded.CheckoutURL,
	)

	if checkoutURL == "" {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "paddle response did not include a checkout url",
		})
	}

	return c.JSON(fiber.Map{
		"checkout_url": checkoutURL,
	})
}

type createCreditCheckoutRequest struct {
	Credits int `json:"credits"`
}

func (ctrl *Controller) CreateCreditCheckout(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	if strings.TrimSpace(userID) == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "missing authenticated user",
		})
	}

	var req createCreditCheckoutRequest
	if err := c.BodyParser(&req); err != nil || req.Credits <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid credits request",
		})
	}

	priceID := creditPriceID(req.Credits)
	if strings.TrimSpace(priceID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing price id for selected credit pack",
		})
	}

	apiBase := strings.TrimRight(getEnv("PADDLE_API_BASE_URL", "https://api.paddle.com"), "/")
	checkoutPath := getEnv("PADDLE_CHECKOUT_CREATE_PATH", "/checkouts")
	apiKey := strings.TrimSpace(os.Getenv("PADDLE_API_KEY"))
	if apiKey == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "paddle api key not configured",
		})
	}

	payload := paddleCheckoutCreatePayload{
		Items: []paddleCheckoutItem{
			{PriceID: priceID, Quantity: 1},
		},
		CustomData: map[string]any{
			"user_id":       userID,
			"purchase_type": "credits",
			"package_type":  fmt.Sprintf("addon_pack_%d", req.Credits),
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to encode checkout payload",
		})
	}

	log.Println("[PADDLE] Payload:")
	log.Println(string(body))

	url := apiBase + checkoutPath
	log.Println("[PADDLE] URL:", url)

	httpReq, err := http.NewRequest(
		http.MethodPost,
		url,
		bytes.NewReader(body),
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to build checkout request",
		})
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	httpRes, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "failed to contact paddle",
		})
	}
	defer httpRes.Body.Close()

	raw, _ := io.ReadAll(httpRes.Body)

	log.Printf("[PADDLE] Status: %d", httpRes.StatusCode)
	log.Printf("[PADDLE] Response: %s", string(raw))

	if httpRes.StatusCode < 200 || httpRes.StatusCode >= 300 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   "paddle checkout creation failed",
			"details": string(raw),
		})
	}

	var decoded paddleCheckoutCreateResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "failed to parse paddle response",
		})
	}

	checkoutURL := firstNonEmpty(
		decoded.Data.URL,
		decoded.Data.CheckoutURL,
		decoded.URL,
		decoded.CheckoutURL,
	)

	if checkoutURL == "" {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "paddle response did not include a checkout url",
		})
	}

	return c.JSON(fiber.Map{
		"checkout_url": checkoutURL,
	})
}

func creditPriceID(credits int) string {
	switch credits {
	case 10:
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_CREDITS_10"))
	case 20:
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_CREDITS_20"))
	case 50:
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_CREDITS_50"))
	case 100:
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_CREDITS_100"))
	case 200:
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_CREDITS_200"))
	case 500:
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_CREDITS_500"))
	default:
		return ""
	}
}

func priceIDForPlan(tier, interval string) string {
	tier = strings.ToLower(strings.TrimSpace(tier))
	interval = strings.ToLower(strings.TrimSpace(interval))

	switch tier {
	case "plus":
		if interval == "yearly" {
			return strings.TrimSpace(os.Getenv("PADDLE_PRICE_PLUS_YEARLY"))
		}
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_PLUS_MONTHLY"))

	case "pro":
		if interval == "yearly" {
			return strings.TrimSpace(os.Getenv("PADDLE_PRICE_PRO_YEARLY"))
		}
		return strings.TrimSpace(os.Getenv("PADDLE_PRICE_PRO_MONTHLY"))
	default:
		return ""
	}
}

func getEnv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

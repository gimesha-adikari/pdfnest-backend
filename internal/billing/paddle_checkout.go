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

type createCreditCheckoutRequest struct {
	Credits int `json:"credits"`
}

type paddleTransactionItem struct {
	PriceID  string `json:"price_id"`
	Quantity int    `json:"quantity"`
}

type paddleTransactionCheckout struct {
	URL *string `json:"url"`
}

type paddleTransactionCreatePayload struct {
	Items          []paddleTransactionItem   `json:"items"`
	CollectionMode string                    `json:"collection_mode"`
	Checkout       paddleTransactionCheckout `json:"checkout"`
	CustomData     map[string]any            `json:"custom_data,omitempty"`
}

type paddleTransactionCreateResponse struct {
	Data struct {
		ID       string `json:"id"`
		Checkout struct {
			URL *string `json:"url"`
		} `json:"checkout"`
	} `json:"data"`
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

	if _, err := ensureSubscriptionRow(userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to prepare subscription record",
		})
	}

	checkoutURL, raw, err := createPaddleTransactionCheckout(priceID, map[string]any{
		"user_id":          userID,
		"package_type":     req.Tier,
		"billing_interval": req.Interval,
		"purchase_type":    "subscription",
	})
	if err != nil {
		log.Printf("[PADDLE] subscription checkout failed: %v", err)
		log.Printf("[PADDLE] response: %s", raw)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   "paddle checkout creation failed",
			"details": raw,
		})
	}

	return c.JSON(fiber.Map{
		"checkout_url": checkoutURL,
	})
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

	if _, err := ensureSubscriptionRow(userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to prepare subscription record",
		})
	}

	checkoutURL, raw, err := createPaddleTransactionCheckout(priceID, map[string]any{
		"user_id":       userID,
		"purchase_type": "credits",
		"package_type":  fmt.Sprintf("addon_pack_%d", req.Credits),
	})
	if err != nil {
		log.Printf("[PADDLE] credit checkout failed: %v", err)
		log.Printf("[PADDLE] response: %s", raw)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   "paddle checkout creation failed",
			"details": raw,
		})
	}

	return c.JSON(fiber.Map{
		"checkout_url": checkoutURL,
	})
}

func createPaddleTransactionCheckout(priceID string, customData map[string]any) (string, string, error) {
	apiBase := strings.TrimRight(getEnv("PADDLE_API_BASE_URL", "https://api.paddle.com"), "/")
	apiURL := apiBase + "/transactions"
	apiKey := strings.TrimSpace(os.Getenv("PADDLE_API_KEY"))
	if apiKey == "" {
		return "", "", fmt.Errorf("paddle api key not configured")
	}

	defaultPaymentURL := strings.TrimSpace(os.Getenv("PADDLE_DEFAULT_PAYMENT_URL"))

	var checkoutURL *string
	if defaultPaymentURL != "" {
		checkoutURL = &defaultPaymentURL
	}

	payload := paddleTransactionCreatePayload{
		Items: []paddleTransactionItem{
			{PriceID: priceID, Quantity: 1},
		},
		CollectionMode: "automatic",
		Checkout:       paddleTransactionCheckout{URL: checkoutURL},
		CustomData:     customData,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("failed to encode checkout payload: %w", err)
	}

	log.Println("[PADDLE] Payload:")
	log.Println(string(body))
	log.Println("[PADDLE] URL:", apiURL)

	httpReq, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("failed to build checkout request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	httpRes, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to contact paddle: %w", err)
	}
	defer httpRes.Body.Close()

	raw, _ := io.ReadAll(httpRes.Body)

	log.Printf("[PADDLE] Status: %d", httpRes.StatusCode)
	log.Printf("[PADDLE] Response: %s", string(raw))

	if httpRes.StatusCode < 200 || httpRes.StatusCode >= 300 {
		return "", string(raw), fmt.Errorf("paddle checkout creation failed")
	}

	var decoded paddleTransactionCreateResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", string(raw), fmt.Errorf("failed to parse paddle response: %w", err)
	}

	if decoded.Data.Checkout.URL == nil || strings.TrimSpace(*decoded.Data.Checkout.URL) == "" {
		return "", string(raw), fmt.Errorf("paddle response did not include a checkout url")
	}

	return *decoded.Data.Checkout.URL, string(raw), nil
}

func ensureSubscriptionRow(userID string) (*config.Subscription, error) {
	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err == nil {
		return &sub, nil
	}

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
		return nil, err
	}
	return &sub, nil
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

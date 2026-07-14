// file: internal/billing/errors.go
package billing

type ErrorCode string

const (
	ErrHourlyLimit    ErrorCode = "HOURLY_LIMIT_REACHED"
	ErrDailyLimit     ErrorCode = "DAILY_LIMIT_REACHED"
	ErrMonthlyLimit   ErrorCode = "MONTHLY_LIMIT_REACHED"
	ErrCredits        ErrorCode = "CREDITS_EXHAUSTED"
	ErrSubscription   ErrorCode = "SUBSCRIPTION_REQUIRED"
	ErrUnknownBilling ErrorCode = "BILLING_ERROR"
)

type BillingError struct {
	Code               string `json:"code"`
	Title              string `json:"title"`
	Message            string `json:"message"`
	Description        string `json:"description,omitempty"`
	Window             string `json:"window,omitempty"`
	ResetAt            string `json:"resetAt,omitempty"`
	UpgradeRecommended bool   `json:"upgradeRecommended"`
	RemainingCredits   int    `json:"remainingCredits,omitempty"`
	RequestedUnits     int    `json:"requestedUnits,omitempty"`
	Tool               string `json:"tool,omitempty"`
}

func (e *BillingError) Error() string {
	return e.Message
}

func NewBillingError(
	code ErrorCode,
	title, message, description, window string,
	requestedUnits int,
) *BillingError {
	return &BillingError{
		Code:               string(code),
		Title:              title,
		Message:            message,
		Description:        description,
		Window:             window,
		UpgradeRecommended: true,
		RequestedUnits:     requestedUnits,
	}
}

func HourlyLimitError(requestedUnits int) *BillingError {
	return NewBillingError(
		ErrHourlyLimit,
		"Usage limit reached",
		"You've reached your 3-hour usage limit.",
		"Please wait until your usage window resets or upgrade your plan for a higher allowance.",
		"3h",
		requestedUnits,
	)
}

func DailyLimitError(requestedUnits int) *BillingError {
	return NewBillingError(
		ErrDailyLimit,
		"Daily limit reached",
		"You've used all available units for today.",
		"Your daily allowance has been exhausted. Try again tomorrow or upgrade your plan.",
		"daily",
		requestedUnits,
	)
}

func MonthlyLimitError(requestedUnits int) *BillingError {
	return NewBillingError(
		ErrMonthlyLimit,
		"Monthly allowance reached",
		"You've used your monthly unit allowance.",
		"Upgrade your plan or wait until your monthly allowance resets.",
		"monthly",
		requestedUnits,
	)
}

func CreditsExhaustedError(requestedUnits int) *BillingError {
	return NewBillingError(
		ErrCredits,
		"No credits remaining",
		"You've used all of your included units and extra credits.",
		"Purchase additional credits or upgrade your subscription to continue.",
		"",
		requestedUnits,
	)
}

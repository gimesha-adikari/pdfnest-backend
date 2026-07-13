package billing

import "time"

type WindowName string

const (
	Window3H    WindowName = "3h"
	WindowDay   WindowName = "daily"
	WindowMonth WindowName = "monthly"
)

type Reservation struct {
	UserID      string
	ToolName    string
	Pages       int
	Images      int
	Units       int
	PlanUnits   int
	CreditUnits int
	RequestPath string
	CreatedAt   time.Time
}

type ToolEstimate struct {
	ToolName    string
	BaseUnits   int
	Pages       int
	Images      int
	PageFactor  float64
	ImageFactor float64
	Units       int
}

type TierLimits struct {
	Units3H    int
	UnitsDay   int
	UnitsMonth int
}

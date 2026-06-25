package models

import "time"

type HomePageContent struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Badges & Hero Main Copy Loops
	HeroBadgeGuest        string `gorm:"type:varchar(255);not null" json:"heroBadgeGuest"`
	HeroBadgeFree         string `gorm:"type:varchar(255);not null" json:"heroBadgeFree"`
	HeroBadgePro          string `gorm:"type:varchar(255);not null" json:"heroBadgePro"`
	HeroWelcomeBack       string `gorm:"type:varchar(255);not null" json:"heroWelcomeBack"`
	HeroTitleGuest        string `gorm:"type:varchar(255);not null" json:"heroTitleGuest"`
	HeroTitlePro          string `gorm:"type:varchar(255);not null" json:"heroTitlePro"`
	HeroSubtitleGuest     string `gorm:"type:text;not null" json:"heroSubtitleGuest"`
	HeroSubtitleGuestBold string `gorm:"type:varchar(255);not null" json:"heroSubtitleGuestBold"`

	// Interactive Subscription Badges Sub-States
	AuthBannerProAccess  string `gorm:"type:text;not null" json:"authBannerProAccess"`
	AuthBannerFreeUsage  string `gorm:"type:text;not null" json:"authBannerFreeUsage"`
	AuthBannerFreeAction string `gorm:"type:varchar(255);not null" json:"authBannerFreeAction"`

	// 3 Column Value Props Grid Section
	Feature1Title       string `gorm:"type:varchar(255);not null" json:"feature1Title"`
	Feature1Description string `gorm:"type:text;not null" json:"feature1Description"`
	Feature2Title       string `gorm:"type:varchar(255);not null" json:"feature2Title"`
	Feature2Description string `gorm:"type:text;not null" json:"feature2Description"`
	Feature3Title       string `gorm:"type:varchar(255);not null" json:"feature3Title"`
	Feature3Description string `gorm:"type:text;not null" json:"feature3Description"`

	// Search Matrices Variables
	SearchPlaceholder      string `gorm:"type:varchar(255);not null" json:"searchPlaceholder"`
	SearchScopeSuffix      string `gorm:"type:varchar(255);not null" json:"searchScopeSuffix"`
	SearchEmptyTitle       string `gorm:"type:varchar(255);not null" json:"searchEmptyTitle"`
	SearchEmptyDescription string `gorm:"type:varchar(255);not null" json:"searchEmptyDescription"`

	// Promoted Tool Block Card
	PopularToolTitle       string `gorm:"type:varchar(255);not null" json:"popularToolTitle"`
	PopularToolDescription string `gorm:"type:text;not null" json:"popularToolDescription"`
	PopularToolAction      string `gorm:"type:varchar(255);not null" json:"popularToolAction"`

	// Category Headers
	// Category Headers
	CategoryOrganizeTitle string `gorm:"type:varchar(255);not null" json:"categoryOrganizeTitle"`
	CategoryOrganizeDesc  string `gorm:"type:text;not null" json:"categoryOrganizeDesc"`

	CategoryEditingTitle string `gorm:"type:varchar(255);not null" json:"categoryEditingTitle"`
	CategoryEditingDesc  string `gorm:"type:text;not null" json:"categoryEditingDesc"`

	CategoryConvertTitle string `gorm:"type:varchar(255);not null" json:"categoryConvertTitle"`
	CategoryConvertDesc  string `gorm:"type:text;not null" json:"categoryConvertDesc"`

	CategoryCreateTitle string `gorm:"type:varchar(255);not null" json:"categoryCreateTitle"`
	CategoryCreateDesc  string `gorm:"type:text;not null" json:"categoryCreateDesc"`

	CategorySecurityTitle string `gorm:"type:varchar(255);not null" json:"categorySecurityTitle"`
	CategorySecurityDesc  string `gorm:"type:text;not null" json:"categorySecurityDesc"`

	CategoryOptimizeTitle string `gorm:"type:varchar(255);not null" json:"categoryOptimizeTitle"`
	CategoryOptimizeDesc  string `gorm:"type:text;not null" json:"categoryOptimizeDesc"`

	CategoryStudioTitle string `gorm:"type:varchar(255);not null" json:"categoryStudioTitle"`
	CategoryStudioDesc  string `gorm:"type:text;not null" json:"categoryStudioDesc"`

	UpdatedAt time.Time `json:"updatedAt"`
}

type SubscribePageContent struct {
	ID                uint   `gorm:"primaryKey" json:"id"`
	HeroBadge         string `gorm:"type:varchar(255);not null" json:"heroBadge"`
	HeroTitle         string `gorm:"type:varchar(255);not null" json:"heroTitle"`
	HeroTitleGradient string `gorm:"type:varchar(255);not null" json:"heroTitleGradient"`
	HeroSubtitle      string `gorm:"type:text;not null" json:"heroSubtitle"`

	// Features Section
	PremiumSectionTitle string `gorm:"type:varchar(255)" json:"premiumSectionTitle"`
	StudioTitle         string `gorm:"type:varchar(255)" json:"studioTitle"`
	StudioDescription   string `gorm:"type:text" json:"studioDescription"`
	StudioBulletPoints  string `gorm:"type:text" json:"studioBulletPoints"` // Saved as "Edit pages,Watermarks,Metadata..."
	CanvasTitle         string `gorm:"type:varchar(255)" json:"canvasTitle"`
	CanvasDescription   string `gorm:"type:text" json:"canvasDescription"`
	CanvasBulletPoints  string `gorm:"type:text" json:"canvasBulletPoints"`
	SpeedTitle          string `gorm:"type:varchar(255)" json:"speedTitle"`
	SpeedDescription    string `gorm:"type:text" json:"speedDescription"`
	SpeedBulletPoints   string `gorm:"type:text" json:"speedBulletPoints"`

	// Pricing Info
	FreeTitle        string `gorm:"type:varchar(255)" json:"freeTitle"`
	FreePrice        string `gorm:"type:varchar(50)" json:"freePrice"`
	FreeSubtitle     string `gorm:"type:varchar(255)" json:"freeSubtitle"`
	FreeBulletPoints string `gorm:"type:text" json:"freeBulletPoints"`

	PlusTitle        string `gorm:"type:varchar(255)" json:"plusTitle"`
	PlusPrice        string `gorm:"type:varchar(50);not null" json:"plusPrice"`
	PlusSubtitle     string `gorm:"type:varchar(255)" json:"plusSubtitle"`
	PlusBulletPoints string `gorm:"type:text" json:"plusBulletPoints"`

	ProTitle        string `gorm:"type:varchar(255)" json:"proTitle"`
	ProPrice        string `gorm:"type:varchar(50);not null" json:"proPrice"`
	ProSubtitle     string `gorm:"type:varchar(255)" json:"proSubtitle"`
	ProBulletPoints string `gorm:"type:text" json:"proBulletPoints"`

	// Security section at bottom
	SecurityTitle    string `gorm:"type:varchar(255)" json:"securityTitle"`
	SecuritySubtitle string `gorm:"type:text" json:"securitySubtitle"`
	SecurityTags     string `gorm:"type:text" json:"securityTags"` // Saved as comma separated

	// Bottom dynamic states text mapping
	CtaGuestTitle   string `gorm:"type:text" json:"ctaGuestTitle"`
	CtaFreeTitle    string `gorm:"type:text" json:"ctaFreeTitle"`
	CtaFreeSubtitle string `gorm:"type:text" json:"ctaFreeSubtitle"`
	CtaPlusTitle    string `gorm:"type:text" json:"ctaPlusTitle"`
	CtaProTitle     string `gorm:"type:text" json:"ctaProTitle"`
	CtaProSubtitle  string `gorm:"type:text" json:"ctaProSubtitle"`

	FaqsJSON  string    `gorm:"type:text;not null" json:"faqsJson"`
	UpdatedAt time.Time `json:"updatedAt"`
}

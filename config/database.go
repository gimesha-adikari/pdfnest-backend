package config

import (
	"log"
	"os"
	"pdfnest-backend/internal/models"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

type User struct {
	ID                   string  `gorm:"type:uuid;primaryKey"`
	Email                string  `gorm:"type:varchar(255);uniqueIndex;not null"`
	PasswordHash         string  `gorm:"type:varchar(255);nullable"`
	GoogleID             *string `gorm:"type:varchar(255);uniqueIndex;nullable"`
	Role                 string  `gorm:"type:varchar(50);default:'user'"`
	Status               string  `gorm:"type:varchar(50);default:'pending'"`
	EmailVerified        bool    `gorm:"default:false"`
	EmailVerifyTokenHash string  `gorm:"type:varchar(255);index"`
	EmailVerifyExpiresAt time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	DeletedAt            gorm.DeletedAt `gorm:"index"`
}

type Subscription struct {
	ID                   string    `gorm:"type:uuid;primaryKey"`
	UserID               string    `gorm:"type:uuid;index;not null"`
	User                 User      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	PaddleCustomerID     string    `gorm:"type:varchar(255);uniqueIndex"`
	PaddleSubscriptionID string    `gorm:"type:varchar(255);uniqueIndex"`
	Status               string    `gorm:"type:varchar(50);not null"`
	Tier                 string    `gorm:"type:varchar(50);default:'free'"`
	BillingInterval      string    `gorm:"type:varchar(20);default:'monthly'"` // monthly | yearly
	TrialEndsAt          time.Time `gorm:""`
	CustomCredits        int       `gorm:"default:0;not null"`
	UpdateURL            string    `gorm:"type:text"`
	CancelURL            string    `gorm:"type:text"`
	CurrentPeriodEnd     time.Time `gorm:"not null"`

	UsedUnits3h          int       `gorm:"default:0;not null"`
	UsedUnitsDaily       int       `gorm:"default:0;not null"`
	UsedUnitsMonthly     int       `gorm:"default:0;not null"`
	Window3HResetAt      time.Time `gorm:"not null"`
	WindowDailyResetAt   time.Time `gorm:"not null"`
	WindowMonthlyResetAt time.Time `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

type BillingReservation struct {
	ID          string    `gorm:"type:uuid;primaryKey"`
	UserID      string    `gorm:"type:uuid;index;not null"`
	User        User      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	ToolName    string    `gorm:"type:varchar(100);not null"`
	PagesCount  int       `gorm:"default:0;not null"`
	ImagesCount int       `gorm:"default:0;not null"`
	Units       int       `gorm:"default:0;not null"`
	PlanUnits   int       `gorm:"default:0;not null"`
	CreditUnits int       `gorm:"default:0;not null"`
	Status      string    `gorm:"type:varchar(20);default:'reserved';not null"`
	RequestPath string    `gorm:"type:text"`
	ExpiresAt   time.Time `gorm:"index;not null"`
	CreatedAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
}

type Transaction struct {
	ID                  string  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID              string  `gorm:"type:uuid;index;not null"`
	User                User    `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	SubscriptionID      string  `gorm:"type:uuid;index"`
	PaddleTransactionID string  `gorm:"type:varchar(255);uniqueIndex"`
	Amount              float64 `gorm:"type:decimal(10,2);not null"`
	Currency            string  `gorm:"type:varchar(10);not null"`
	Status              string  `gorm:"type:varchar(50);not null"`
	CreatedAt           time.Time
}

type UsageLog struct {
	ID         string    `gorm:"type:uuid;primaryKey"`
	UserID     string    `gorm:"type:uuid;index;not null"`
	User       User      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	ToolName   string    `gorm:"type:varchar(100);not null"`
	IsCredit   bool      `gorm:"default:false;not null"`
	PagesCount int       `gorm:"default:0"`
	CreatedAt  time.Time `gorm:"index"`
}

type WebhookLog struct {
	ID        string `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	EventID   string `gorm:"type:varchar(255);uniqueIndex;not null"`
	EventType string `gorm:"type:varchar(100);not null"`
	Status    string `gorm:"type:varchar(50);default:'processed'"`
	CreatedAt time.Time
}

type UserSetting struct {
	ID     string `gorm:"type:uuid;primaryKey"`
	UserID string `gorm:"type:uuid;uniqueIndex;not null"`
	User   User   `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	EmailNotifications bool   `gorm:"default:true;not null"`
	ProductUpdates     bool   `gorm:"default:true;not null"`
	BillingEmails      bool   `gorm:"default:true;not null"`
	SecurityAlerts     bool   `gorm:"default:true;not null"`
	Theme              string `gorm:"type:varchar(20);default:'system'"`
	Language           string `gorm:"type:varchar(20);default:'en'"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

type ContactCategory struct {
	ID          string `gorm:"type:uuid;primaryKey"`
	Name        string `gorm:"type:varchar(120);not null"`
	Slug        string `gorm:"type:varchar(120);uniqueIndex;not null"`
	Type        string `gorm:"type:varchar(60);index;not null"` // billing, technical, security, feedback, account, other
	Description string `gorm:"type:text"`
	Color       string `gorm:"type:varchar(30)"`
	SortOrder   int    `gorm:"default:0;index"`
	IsActive    bool   `gorm:"default:true;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ContactTicket struct {
	ID string `gorm:"type:uuid;primaryKey"`

	TicketNumber string `gorm:"type:varchar(30);uniqueIndex;not null"`

	UserID *string `gorm:"type:uuid;index"`
	User   *User   `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`

	AssignedToID *string `gorm:"type:uuid;index"`
	AssignedTo   *User   `gorm:"foreignKey:AssignedToID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`

	Name  *string `gorm:"type:varchar(150)"`
	Email string  `gorm:"type:varchar(255);index;not null"`

	Category string `gorm:"type:varchar(50);index;not null"`
	Subject  string `gorm:"type:varchar(255);not null"`
	Message  string `gorm:"type:text;not null"`

	Status   string `gorm:"type:varchar(30);default:'open';index"`
	Priority string `gorm:"type:varchar(30);default:'normal';index"`

	Source string `gorm:"type:varchar(30);default:'website'"`

	InternalNotes string `gorm:"type:text"`

	IPAddress string `gorm:"type:varchar(64)"`
	UserAgent string `gorm:"type:text"`

	ResolvedAt *time.Time
	ClosedAt   *time.Time

	LastActivityAt time.Time `gorm:"index"`

	CreatedAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
	EmailStatus string
}

func ConnectDB() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to establish target connection database: %v", err)
	}

	//log.Println("DEVELOPMENT WARNING: Dropping existing schema tables for a clean runtime run...")
	//err = database.Migrator().DropTable(
	//	&UserSetting{},
	//	&ContactTicket{},
	//	&ContactCategory{},
	//	&BillingReservation{},
	//	&Subscription{},
	//	&Transaction{},
	//	&UsageLog{},
	//	&WebhookLog{},
	//	&User{},
	//	&models.HomePageContent{},
	//	&models.SubscribePageContent{},
	//	&models.DynamicToolItem{},
	//	models.AboutPageContent{},
	//)
	//if err != nil {
	//	log.Printf("Warning: Failed to clear old tables during startup sweep: %v", err)
	//}

	err = database.AutoMigrate(
		&User{},
		&Subscription{},
		&Transaction{},
		&UsageLog{},
		&WebhookLog{},
		&BillingReservation{},
		&UserSetting{},
		&ContactCategory{},
		&ContactTicket{},
		&models.HomePageContent{},
		&models.SubscribePageContent{},
		&models.DynamicToolItem{},
		models.AboutPageContent{},
	)
	if err != nil {
		log.Fatalf("Database structural schema update failure: %v", err)
	}

	err = database.Exec("CREATE SEQUENCE IF NOT EXISTS contact_ticket_sequence START 1").Error
	if err != nil {
		log.Fatalf("Failed to create sequence contact_ticket_sequence: %v", err)
	}

	DB = database
	log.Println("Database connection pool securely initialized and synced.")

	tables, _ := database.Migrator().GetTables()

	log.Println("TABLES:", tables)

	var categoryCount int64
	if err := DB.Model(&ContactCategory{}).Count(&categoryCount).Error; err == nil && categoryCount == 0 {
		now := time.Now()
		defaultCategories := []ContactCategory{
			{ID: uuid.New().String(), Name: "Bug Report", Slug: "bug-report", Type: "technical", Description: "Technical errors and broken behavior", Color: "indigo", SortOrder: 1, IsActive: true, CreatedAt: now, UpdatedAt: now},
			{ID: uuid.New().String(), Name: "Billing", Slug: "billing", Type: "billing", Description: "Payments, invoices, subscriptions, refunds", Color: "emerald", SortOrder: 2, IsActive: true, CreatedAt: now, UpdatedAt: now},
			{ID: uuid.New().String(), Name: "Account", Slug: "account", Type: "account", Description: "Login, verification, account access", Color: "amber", SortOrder: 3, IsActive: true, CreatedAt: now, UpdatedAt: now},
			{ID: uuid.New().String(), Name: "Security", Slug: "security", Type: "security", Description: "Suspicious activity and security concerns", Color: "rose", SortOrder: 4, IsActive: true, CreatedAt: now, UpdatedAt: now},
			{ID: uuid.New().String(), Name: "Feedback", Slug: "feedback", Type: "feedback", Description: "Suggestions and product feedback", Color: "purple", SortOrder: 5, IsActive: true, CreatedAt: now, UpdatedAt: now},
			{ID: uuid.New().String(), Name: "General", Slug: "general", Type: "other", Description: "Anything else", Color: "slate", SortOrder: 6, IsActive: true, CreatedAt: now, UpdatedAt: now},
		}
		_ = DB.Create(&defaultCategories).Error
	}

	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail == "" {
		adminEmail = "admin@admin.com"
	}

	var count int64
	DB.Model(&User{}).Where("email = ?", adminEmail).Count(&count)

	if count == 0 {
		adminPassword := os.Getenv("ADMIN_PASSWORD")
		isProd := os.Getenv("APP_ENV") == "production"

		if adminPassword == "" {
			if isProd {
				log.Printf("[SEEDER WARNING] APP_ENV=production but ADMIN_PASSWORD is not configured. Skipping admin account creation.")
				return
			}
			adminPassword = "admin"
		}

		log.Printf("[SEEDER] Creating administrative core profile account for: %s", adminEmail)
		passwordHash, _ := HashPassword(adminPassword)

		adminUser := User{
			ID:            uuid.New().String(),
			Email:         adminEmail,
			PasswordHash:  passwordHash,
			Role:          "admin",
			Status:        "active",
			EmailVerified: true,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}

		if err := DB.Create(&adminUser).Error; err != nil {
			log.Printf("[SEEDER ERROR] Failed to bootstrap admin user schema: %v", err)
			return
		}

		now := time.Now()

		adminSub := Subscription{
			ID:                   uuid.New().String(),
			UserID:               adminUser.ID,
			PaddleCustomerID:     "admin_cust_" + adminUser.ID,
			PaddleSubscriptionID: "admin_sub_" + adminUser.ID,
			Status:               "active",
			Tier:                 "plus",
			CustomCredits:        9999,
			CurrentPeriodEnd:     now.AddDate(50, 0, 0),

			UsedUnits3h:          0,
			UsedUnitsDaily:       0,
			UsedUnitsMonthly:     0,
			Window3HResetAt:      now.Add(3 * time.Hour),
			WindowDailyResetAt:   time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()),
			WindowMonthlyResetAt: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0),

			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := DB.Create(&adminSub).Error; err != nil {
			log.Printf("[SEEDER ERROR] Failed to bootstrap admin user tier metadata mapping: %v", err)
		} else {
			log.Println("[SEEDER] Admin seed execution pipelines successfully provisioned.")
		}
	}
}

func LogToolUsage(userID string, toolName string, isCredit bool) {
	logEntry := UsageLog{
		ID:        uuid.New().String(),
		UserID:    userID,
		ToolName:  toolName,
		IsCredit:  isCredit,
		CreatedAt: time.Now(),
	}

	if err := DB.Create(&logEntry).Error; err != nil {
		log.Printf("Failed to log usage for user %s on tool %s: %v", userID, toolName, err)
	}
}

func NewUUID() string {
	return uuid.New().String()
}

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
	//err = database.Migrator().DropTable(&User{}, &Subscription{}, &Transaction{}, &UsageLog{}, &WebhookLog{}, &models.HomePageContent{}, &models.SubscribePageContent{}, &models.DynamicToolItem{}, models.AboutPageContent{})
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
		&models.HomePageContent{},
		&models.SubscribePageContent{},
		&models.DynamicToolItem{},
		models.AboutPageContent{},
	)

	err = database.AutoMigrate(&User{}, &Subscription{}, &Transaction{}, &UsageLog{}, &WebhookLog{}, &models.HomePageContent{}, &models.SubscribePageContent{}, &models.DynamicToolItem{}, models.AboutPageContent{})
	if err != nil {
		log.Fatalf("Database structural schema update failure: %v", err)
	}

	DB = database
	log.Println("Database connection pool securely initialized and synced.")

	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail == "" {
		adminEmail = "admin@admin.com"
	}

	var count int64
	DB.Model(&User{}).Where("email = ?", adminEmail).Count(&count)

	if count == 0 {
		log.Printf("[SEEDER] Creating administrative core profile account for: %s", adminEmail)

		adminPassword := os.Getenv("ADMIN_PASSWORD")
		if adminPassword == "" {
			adminPassword = "admin"
		}

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
			Tier:                 "pro",
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

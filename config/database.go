// file: config/config.go
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
	Tier                 string    `gorm:"type:varchar(50);default:'free'"` // 'free', 'plus', or 'pro'
	CustomCredits        int       `gorm:"default:0;not null"`              // ADDED: Lifetime remaining purchased package tokens
	UpdateURL            string    `gorm:"type:text"`
	CancelURL            string    `gorm:"type:text"`
	CurrentPeriodEnd     time.Time `gorm:"not null"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
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
	IsCredit   bool      `gorm:"default:false;not null"` // ADDED: Marks if transaction consumed a standalone token block instead of subscription quota
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

func ConnectDB() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to establish target connection database: %v", err)
	}

	log.Println("DEVELOPMENT WARNING: Dropping existing schema tables for a clean runtime run...")
	err = database.Migrator().DropTable(&User{}, &Subscription{}, &Transaction{}, &UsageLog{}, &WebhookLog{}, &models.HomePageContent{}, &models.SubscribePageContent{}, &models.DynamicToolItem{}, models.AboutPageContent{})
	if err != nil {
		log.Printf("Warning: Failed to clear old tables during startup sweep: %v", err)
	}

	err = database.AutoMigrate(&User{}, &Subscription{}, &Transaction{}, &UsageLog{}, &WebhookLog{}, &models.HomePageContent{}, &models.SubscribePageContent{}, &models.DynamicToolItem{}, models.AboutPageContent{})
	if err != nil {
		log.Fatalf("Database structural schema update failure: %v", err)
	}

	DB = database
	log.Println("Database connection pool securely initialized and synced.")

	adminEmail := "gimeshaadikari23@gmail.com"
	var count int64
	DB.Model(&User{}).Where("email = ?", adminEmail).Count(&count)

	if count == 0 {
		log.Printf("[SEEDER] Creating administrative core profile account for: %s", adminEmail)

		adminUser := User{
			ID:            uuid.New().String(),
			Email:         adminEmail,
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

		adminSub := Subscription{
			ID:                   uuid.New().String(),
			UserID:               adminUser.ID,
			PaddleCustomerID:     "admin_cust_" + adminUser.ID,
			PaddleSubscriptionID: "admin_sub_" + adminUser.ID,
			Status:               "active",
			Tier:                 "pro",
			CustomCredits:        9999,
			CurrentPeriodEnd:     time.Now().AddDate(50, 0, 0),
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
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

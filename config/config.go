package config

import (
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

type User struct {
	ID           string `gorm:"type:uuid;primaryKey"`
	Email        string `gorm:"type:varchar(255);uniqueIndex;not null"`
	PasswordHash string `gorm:"type:varchar(255);nullable"`
	GoogleID     string `gorm:"type:varchar(255);uniqueIndex;nullable"`
	Role         string `gorm:"type:varchar(50);default:'user'"`
	Status       string `gorm:"type:varchar(50);default:'active'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Subscription struct {
	ID                   string    `gorm:"type:uuid;primaryKey"`
	UserID               string    `gorm:"type:uuid;index;not null"`
	PaddleCustomerID     string    `gorm:"type:varchar(255);uniqueIndex"`
	PaddleSubscriptionID string    `gorm:"type:varchar(255);uniqueIndex"`
	Status               string    `gorm:"type:varchar(50);not null"`       // 'active', 'past_due', 'canceled'
	PlanTier             string    `gorm:"type:varchar(50);default:'free'"` // 'free', 'pro'
	CurrentPeriodEnd     time.Time `gorm:"not null"`
}

type Transaction struct {
	ID                  string  `gorm:"type:uuid;primaryKey"`
	UserID              string  `gorm:"type:uuid;index;not null"`
	Amount              float64 `gorm:"type:decimal(10,2);not null"`
	PaddleTransactionID string  `gorm:"type:varchar(255);uniqueIndex;not null"`
	Status              string  `gorm:"type:varchar(50);not null"` // 'completed', 'failed'
	CreatedAt           time.Time
}

type UsageLog struct {
	ID         string    `gorm:"type:uuid;primaryKey"`
	UserID     string    `gorm:"type:uuid;index;not null"`
	ToolName   string    `gorm:"type:varchar(100);not null"` // e.g., "ocr", "merge", "protect"
	PagesCount int       `gorm:"default:0"`                  // Useful if gating by page count
	CreatedAt  time.Time `gorm:"index"`                      // Indexed for lightning-fast date filtering
}

func ConnectDB() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=secret dbname=pdfnest port=5432 sslmode=disable"
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to establish target connection database: %v", err)
	}

	err = database.AutoMigrate(&User{}, &Subscription{}, &Transaction{}, &UsageLog{})
	if err != nil {
		log.Fatalf("Database structural schema update failure: %v", err)
	}

	DB = database
	log.Println("Database connection pool securely initialized and synced.")
}

func LogToolUsage(userID string, toolName string) {
	logEntry := UsageLog{
		ID:        uuid.New().String(),
		UserID:    userID,
		ToolName:  toolName,
		CreatedAt: time.Now(),
	}

	if err := DB.Create(&logEntry).Error; err != nil {
		log.Printf("Failed to log usage for user %s on tool %s: %v", userID, toolName, err)
	}
}

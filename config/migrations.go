package config

import (
	"fmt"
	"log"
	"os"
	"time"

	analyzerModels "pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/models"
	studioModels "pdfnest-backend/internal/studio/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const managedSchemaVersion = "20260828_01"

func schemaModels() []interface{} {
	return []interface{}{
		&User{}, &Subscription{}, &Transaction{}, &UsageLog{}, &WebhookLog{}, &BillingReservation{}, &UserSetting{},
		&ContactCategory{}, &ContactTicket{}, &models.HomePageContent{}, &models.SubscribePageContent{}, &models.DynamicToolItem{},
		models.AboutPageContent{}, &analyzerModels.AnalyzerSession{}, &studioModels.StudioDocument{}, &studioModels.StudioAsset{},
		&studioModels.StudioSnapshot{}, &studioModels.StudioVersion{}, &studioModels.StudioOperation{}, &studioModels.StudioJob{},
		&studioModels.StudioEditorState{}, &studioModels.StudioSession{}, &studioModels.StudioExport{}, &studioModels.StudioStorageCleanupTask{},
	}
}

// RunSchemaMigrations is the one-shot schema entrypoint. PostgreSQL's advisory
// lock serializes deployment owners or replicas that accidentally invoke it
// concurrently; the version row provides reviewable traceability.
func RunSchemaMigrations(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database handle is nil")
	}
	if err := db.Exec("SELECT pg_advisory_lock(hashtext('pdfnest:managed-schema'))").Error; err != nil {
		return fmt.Errorf("acquire schema migration lock: %w", err)
	}
	defer func() {
		if err := db.Exec("SELECT pg_advisory_unlock(hashtext('pdfnest:managed-schema'))").Error; err != nil {
			log.Printf("[MIGRATION] release lock failed: %v", err)
		}
	}()

	if err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version varchar(64) PRIMARY KEY,
		applied_at timestamptz NOT NULL
	)`).Error; err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	if err := db.AutoMigrate(schemaModels()...); err != nil {
		return fmt.Errorf("auto-migrate application schema: %w", err)
	}
	if err := db.Exec("CREATE SEQUENCE IF NOT EXISTS contact_ticket_sequence START 1").Error; err != nil {
		return fmt.Errorf("create contact ticket sequence: %w", err)
	}
	if err := db.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?) ON CONFLICT (version) DO NOTHING", managedSchemaVersion, time.Now().UTC()).Error; err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}

func ValidateManagedSchema(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database handle is nil")
	}
	for _, model := range schemaModels() {
		if !db.Migrator().HasTable(model) {
			return fmt.Errorf("required schema table is missing for %T; run the serialized migrate command before starting replicas", model)
		}
	}
	var count int64
	if err := db.Table("schema_migrations").Where("version = ?", managedSchemaVersion).Count(&count).Error; err != nil || count == 0 {
		return fmt.Errorf("schema migration version %s is not recorded", managedSchemaVersion)
	}
	return nil
}

func RunManagedMigrations() error {
	if !IsManagedEnvironment() {
		return fmt.Errorf("the migrate command requires APP_ENV=canary, staging, or production")
	}
	if err := ValidateRuntimeConfig(); err != nil {
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get migration database handle: %w", err)
	}
	defer sqlDB.Close()
	return RunSchemaMigrations(db)
}

package models

import (
	"time"
)

// AnalyzerSession represents a persistent repository analysis session in PostgreSQL.
type AnalyzerSession struct {
	ID                  string    `gorm:"type:uuid;primaryKey"`
	OwnerIdentity       string    `gorm:"type:varchar(255);index;not null"`
	SourceType          string    `gorm:"type:varchar(50);not null"` // git, zip, local_folder
	GitURL              *string   `gorm:"type:text"`
	StorageKey          *string   `gorm:"type:text"`
	RepositoryName      string    `gorm:"type:varchar(255);not null"`
	ScopeJSON           string    `gorm:"type:text"`
	SelectedDomainsJSON string    `gorm:"type:text"`
	CurrentTaskID       *string   `gorm:"type:varchar(100);index"`
	Status              string    `gorm:"type:varchar(50);default:'CREATED';not null"`
	CreatedAt           time.Time `gorm:"index"`
	UpdatedAt           time.Time
}

package models

import "time"

// TaskCompletion is the application authority for async artifact ownership and
// settlement. Redis task status is only a reconstructible client projection.
// Publishing rows pin their artifact and reservation until settlement succeeds.
type TaskCompletion struct {
	ID              string `gorm:"primaryKey;size:128"`
	OwnerIdentity   string `gorm:"not null"`
	DownloadToken   string `gorm:"not null"`
	ReservationID   string `gorm:"index"`
	ReservationKind string
	State           string `gorm:"not null;index"`
	ArtifactSHA256  string
	ArtifactBytes   int64
	ResultKey       string
	ResultURL       string
	Error           string
	ExpiresAt       time.Time `gorm:"index"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

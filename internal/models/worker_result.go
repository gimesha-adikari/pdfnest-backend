package models

import "time"

// WorkerResultOwnership is the application-owned lifetime of a queued Worker
// dispatch. A submitting row is persisted before network IO and remains pinned
// after an ambiguous acknowledgement. Redis is a projection, never its owner.
type WorkerResultOwnership struct {
	ID            string `gorm:"primaryKey;size:36"`
	RequestID     string `gorm:"uniqueIndex;not null"`
	RequestHash   string `gorm:"not null"`
	WorkerJobID   string `gorm:"index"`
	OwnerIdentity string
	SubmitPath    string `gorm:"not null"`
	Profile       string
	SourceKeys    string `gorm:"type:text;not null"` // JSON; never cleanup candidates
	State         string `gorm:"not null;index"`
	StatusJSON    string `gorm:"type:text"`
	ResultKey     string
	MetadataKey   string
	ExpiresAt     *time.Time `gorm:"index"`
	NextAttemptAt time.Time  `gorm:"index"`
	Attempts      int        `gorm:"not null;default:0"`
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

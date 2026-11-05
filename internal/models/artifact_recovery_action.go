package models

import "time"

// ArtifactRecoveryAction is the durable, idempotent operator audit record for
// a held task artifact. It deliberately stores the original identity alongside
// any separately verified recovery copy; it never authorizes billing or deletion.
type ArtifactRecoveryAction struct {
	ID                 string    `gorm:"type:uuid;primaryKey"`
	OperationKey       string    `gorm:"type:varchar(128);uniqueIndex;not null"`
	RequestFingerprint string    `gorm:"type:char(64);not null"`
	TaskID             string    `gorm:"type:varchar(128);index;not null"`
	AdminUserID        string    `gorm:"type:uuid;index;not null"`
	Action             string    `gorm:"type:varchar(16);not null"`
	State              string    `gorm:"type:varchar(48);not null"`
	Reason             string    `gorm:"type:varchar(500);not null"`
	OwnerIdentity      string    `gorm:"type:text;not null"`
	OriginalResultKey  string    `gorm:"type:text;not null"`
	ArtifactSHA256     string    `gorm:"type:varchar(64);not null"`
	ArtifactBytes      int64     `gorm:"not null"`
	OriginalExpiresAt  time.Time `gorm:"not null"`
	SourceKey          string    `gorm:"type:text"`
	RecoveryKey        string    `gorm:"type:text"`
	LastErrorCode      string    `gorm:"type:varchar(64)"`
	Attempts           int       `gorm:"not null;default:0"`
	LeaseExpiresAt     *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

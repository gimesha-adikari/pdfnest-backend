package ocr

import "time"

// Dormant SQL foundation. This model is deliberately absent from schemaModels,
// request handlers, startup, and Redis. Only guarded PostgreSQL tests create it.
type ocrFinalization struct {
	TaskID               string `gorm:"type:varchar(100);primaryKey;check:ocr_task_id,task_id <> ''"`
	ProtocolVersion      int    `gorm:"not null;check:ocr_protocol,protocol_version = 1"`
	OwnerID              string `gorm:"type:uuid;not null"`
	ReservationID        string `gorm:"type:uuid;not null;uniqueIndex"`
	ReservationKind      string `gorm:"not null;check:ocr_reservation_kind,reservation_kind = 'database'"`
	ToolName             string `gorm:"not null;check:ocr_tool,tool_name = 'image_to_text_pdf'"`
	Units                int    `gorm:"not null;check:ocr_allocation,units >= 0 AND plan_units >= 0 AND credit_units >= 0 AND plan_units <= units AND credit_units = units - plan_units"`
	PlanUnits            int    `gorm:"not null"`
	CreditUnits          int    `gorm:"not null"`
	State                string `gorm:"not null;check:ocr_state,state IN ('accepted','processing','ready','succeeded','cancelled','failed')"`
	Revision             int64  `gorm:"not null;check:ocr_revision,revision >= 1"`
	ProcessingClaim      string
	ProcessingLeaseUntil *time.Time
	Artifact             artifactDescriptor `gorm:"embedded"`
	SafeCode             string             `gorm:"not null;check:ocr_safe_code,safe_code = '' OR safe_code ~ '^[A-Z][A-Z0-9_]{0,63}$'"`
	CreatedAt            time.Time          `gorm:"not null"`
	UpdatedAt            time.Time          `gorm:"not null"`
	DecidedAt            *time.Time         `gorm:"check:ocr_terminal_time,state NOT IN ('succeeded','cancelled','failed') OR decided_at IS NOT NULL"`
	// Placeholders only: no production retention, recovery, or cleanup defaults.
	NextAttemptAt      *time.Time
	RecoveryClaim      string
	RecoveryLeaseUntil *time.Time
	DownloadExpiresAt  *time.Time
	CleanupNotBefore   *time.Time
}

func (ocrFinalization) TableName() string { return "ocr_finalizations" }

// This descriptor binds metadata asserted by a future validator. The dormant
// repository performs no PDF parse, upload, readback, retention, or download.
type artifactDescriptor struct {
	ArtifactID        string    `gorm:"not null"`
	ArtifactKey       string    `gorm:"not null;uniqueIndex:ocr_artifact_key,where:artifact_key <> ''"`
	SHA256            string    `gorm:"column:artifact_sha256;not null"`
	ByteSize          int64     `gorm:"column:artifact_byte_size;not null;check:ocr_ready_artifact,state NOT IN ('ready','succeeded') OR (artifact_id <> '' AND artifact_key <> '' AND artifact_sha256 ~ '^[0-9a-f]{64}$' AND artifact_byte_size > 0 AND artifact_page_count > 0 AND artifact_content_type = 'application/pdf' AND artifact_validation_version = 1 AND artifact_verified_at > '0001-01-01T00:00:00Z'::timestamptz)"`
	PageCount         int       `gorm:"column:artifact_page_count;not null"`
	ContentType       string    `gorm:"column:artifact_content_type;not null"`
	ValidationVersion int       `gorm:"column:artifact_validation_version;not null"`
	VerifiedAt        time.Time `gorm:"column:artifact_verified_at;not null"`
}

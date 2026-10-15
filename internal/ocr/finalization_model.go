package ocr

import "time"

// Dormant SQL foundation. This model is deliberately absent from schemaModels,
// request handlers, startup, and Redis. Only guarded PostgreSQL tests create it.
type ocrFinalization struct {
	TaskID                     string `gorm:"type:varchar(100);primaryKey;check:ocr_task_id,task_id <> ''"`
	ProtocolVersion            int    `gorm:"not null;check:ocr_protocol,protocol_version = 1"`
	OwnerID                    string `gorm:"type:uuid;not null"`
	ReservationID              string `gorm:"type:uuid;not null;uniqueIndex"`
	ReservationKind            string `gorm:"not null;check:ocr_reservation_kind,reservation_kind = 'database'"`
	ToolName                   string `gorm:"not null;check:ocr_tool,tool_name = 'image_to_text_pdf'"`
	Units                      int    `gorm:"not null;check:ocr_allocation,units >= 0 AND plan_units >= 0 AND credit_units >= 0 AND plan_units <= units AND credit_units = units - plan_units"`
	PlanUnits                  int    `gorm:"not null"`
	CreditUnits                int    `gorm:"not null"`
	State                      string `gorm:"not null;check:ocr_lifecycle_shape,state IN ('accepted','processing','ready','succeeded','cancelled','failed') AND (state <> 'accepted' OR (processing_started_at IS NULL AND processing_claim = '' AND processing_lease_until IS NULL AND artifact_intent_at IS NULL AND ready_at IS NULL AND artifact_id = '' AND artifact_key = '' AND artifact_sha256 = '' AND artifact_byte_size = 0 AND artifact_page_count = 0 AND artifact_content_type = '' AND artifact_validation_version = 0 AND artifact_verified_at = '0001-01-01T00:00:00Z'::timestamptz)) AND (state NOT IN ('processing','ready','succeeded') OR (processing_started_at IS NOT NULL AND processing_claim ~ '^[0-9a-f]{64}$' AND processing_lease_until IS NOT NULL AND processing_lease_until > processing_started_at)) AND (state <> 'processing' OR ready_at IS NULL) AND (state NOT IN ('ready','succeeded') OR (artifact_intent_at IS NOT NULL AND ready_at IS NOT NULL)) AND (state NOT IN ('cancelled','failed') OR (processing_claim = '' AND processing_lease_until IS NULL)) AND (state IN ('succeeded','cancelled','failed') OR decided_at IS NULL)"`
	Revision                   int64  `gorm:"not null;check:ocr_revision,revision >= 1"`
	CancellationCapabilityHash string `gorm:"not null;check:ocr_cancel_capability,cancellation_capability_hash ~ '^[0-9a-f]{64}$'"`
	ProcessingStartedAt        *time.Time
	ArtifactIntentAt           *time.Time
	ReadyAt                    *time.Time
	ProcessingClaim            string `gorm:"not null"`
	ProcessingLeaseUntil       *time.Time
	Artifact                   artifactDescriptor `gorm:"embedded"`
	SafeCode                   string             `gorm:"not null;check:ocr_safe_code,safe_code = '' OR safe_code ~ '^[A-Z][A-Z0-9_]{0,63}$'"`
	CreatedAt                  time.Time          `gorm:"not null"`
	UpdatedAt                  time.Time          `gorm:"not null;check:ocr_chronology,created_at > '0001-01-01T00:00:00Z'::timestamptz AND updated_at >= created_at AND (processing_started_at IS NULL OR (processing_started_at >= created_at AND updated_at >= processing_started_at)) AND (artifact_intent_at IS NULL OR (processing_started_at IS NOT NULL AND artifact_intent_at >= processing_started_at AND updated_at >= artifact_intent_at)) AND (ready_at IS NULL OR (artifact_intent_at IS NOT NULL AND updated_at >= ready_at AND ready_at >= artifact_verified_at AND artifact_verified_at >= artifact_intent_at AND artifact_verified_at > created_at)) AND (decided_at IS NULL OR (decided_at >= created_at AND updated_at >= decided_at AND (ready_at IS NULL OR decided_at >= ready_at)))"`
	DecidedAt                  *time.Time         `gorm:"check:ocr_terminal_time,state NOT IN ('succeeded','cancelled','failed') OR decided_at IS NOT NULL"`
	// SQL constraints supplement the locked repository transitions. Explicit IS
	// NOT NULL is necessary: PostgreSQL CHECK accepts an UNKNOWN/NULL expression.
	// Placeholders only: no production retention, recovery, or cleanup defaults.
	NextAttemptAt      *time.Time
	RecoveryClaim      string
	RecoveryLeaseUntil *time.Time
	DownloadExpiresAt  *time.Time
	CleanupNotBefore   *time.Time
}

func (ocrFinalization) TableName() string { return "ocr_finalizations" }

// This descriptor binds logical PDF metadata. The dormant artifact verifier
// independently reads/parses bytes before supplying private readiness evidence.
// The SQL repository itself performs no storage I/O, retention, or download.
type artifactDescriptor struct {
	ArtifactID        string    `gorm:"not null"`
	ArtifactKey       string    `gorm:"not null;uniqueIndex:ocr_artifact_key,where:artifact_key <> ''"`
	SHA256            string    `gorm:"column:artifact_sha256;not null;check:ocr_intent_artifact,(artifact_intent_at IS NULL AND artifact_id = '' AND artifact_key = '' AND artifact_sha256 = '' AND artifact_byte_size = 0 AND artifact_page_count = 0 AND artifact_content_type = '' AND artifact_validation_version = 0 AND artifact_verified_at = '0001-01-01T00:00:00Z'::timestamptz) OR (artifact_intent_at IS NOT NULL AND artifact_id <> '' AND artifact_key <> '' AND artifact_sha256 ~ '^[0-9a-f]{64}$' AND artifact_byte_size > 0 AND artifact_page_count > 0 AND artifact_content_type = 'application/pdf' AND artifact_validation_version = 1 AND (ready_at IS NOT NULL OR artifact_verified_at = '0001-01-01T00:00:00Z'::timestamptz))"`
	ByteSize          int64     `gorm:"column:artifact_byte_size;not null;check:ocr_ready_artifact,state NOT IN ('ready','succeeded') OR (artifact_id <> '' AND artifact_key <> '' AND artifact_sha256 ~ '^[0-9a-f]{64}$' AND artifact_byte_size > 0 AND artifact_page_count > 0 AND artifact_content_type = 'application/pdf' AND artifact_validation_version = 1 AND artifact_verified_at > '0001-01-01T00:00:00Z'::timestamptz)"`
	PageCount         int       `gorm:"column:artifact_page_count;not null"`
	ContentType       string    `gorm:"column:artifact_content_type;not null"`
	ValidationVersion int       `gorm:"column:artifact_validation_version;not null"`
	VerifiedAt        time.Time `gorm:"column:artifact_verified_at;not null"`
}

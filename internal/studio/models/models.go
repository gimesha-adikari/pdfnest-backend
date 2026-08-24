package models

import (
	"database/sql/driver"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// JSON represents raw JSONB bytes compatible with GORM and PostgreSQL.
type JSON []byte

// Value returns the driver.Value for database serialization.
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// Scan deserializes database value into JSON byte slice.
func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.New("failed to scan JSON: invalid type")
	}
	*j = append((*j)[0:0], bytes...)
	return nil
}

// MarshalJSON returns j as the JSON encoding of j.
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON sets *j to a copy of data.
func (j *JSON) UnmarshalJSON(data []byte) error {
	if j == nil {
		return errors.New("JSON: UnmarshalJSON on nil pointer")
	}
	*j = append((*j)[0:0], data...)
	return nil
}

// StudioDocument represents a workspace document entity.
type StudioDocument struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OriginalFileName string    `gorm:"type:varchar(255);not null" json:"original_filename"`
	FileSize         int64     `gorm:"not null" json:"file_size"`
	InitialPageCount int       `gorm:"not null" json:"initial_page_count"`
	CreatedAt        time.Time `json:"created_at"`
}

// StudioAsset is the singular authoritative catalog for all persistent binary files owned by a document in Cloudflare R2.
// Asset types include: source_pdf, merge_source, signature, watermark, snapshot.
type StudioAsset struct {
	ID         string    `gorm:"type:varchar(128);primaryKey" json:"id"`
	DocumentID uuid.UUID `gorm:"type:uuid;index;not null" json:"document_id"`
	AssetType  string    `gorm:"type:varchar(32);not null" json:"asset_type"`
	R2Key      string    `gorm:"type:varchar(512);not null" json:"r2_key"`
	ByteSize   int64     `gorm:"not null" json:"byte_size"`
	MimeType   string    `gorm:"type:varchar(64);not null" json:"mime_type"`
	CreatedAt  time.Time `json:"created_at"`

	Document *StudioDocument `gorm:"foreignKey:DocumentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

// StudioSnapshot stores version-checkpoint metadata referencing a compiled StudioAsset.
type StudioSnapshot struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	VersionID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null" json:"version_id"`
	AssetID   string    `gorm:"type:varchar(128);index;not null" json:"asset_id"` // FK -> StudioAsset.id
	PageCount int       `gorm:"not null" json:"page_count"`
	CreatedAt time.Time `json:"created_at"`

	Asset *StudioAsset `gorm:"foreignKey:AssetID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
}

// StudioVersion represents an immutable node in the document's version DAG.
type StudioVersion struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	DocumentID       uuid.UUID  `gorm:"type:uuid;index;not null" json:"document_id"`
	ParentVersionID  *uuid.UUID `gorm:"type:uuid;index" json:"parent_version_id,omitempty"`
	PreferredChildID *uuid.UUID `gorm:"type:uuid" json:"preferred_child_id,omitempty"`
	VersionNumber    int        `gorm:"not null" json:"version_number"`
	Status           string     `gorm:"type:varchar(20);default:'ready';not null" json:"status"` // ready, pending, running, failed, cancelled
	OperationType    string     `gorm:"type:varchar(64);not null" json:"operation_type"`
	VirtualModel     JSON       `gorm:"type:jsonb;not null" json:"virtual_model"`
	SnapshotID       *uuid.UUID `gorm:"type:uuid;index" json:"snapshot_id,omitempty"`
	IsMaterialized   bool       `gorm:"default:false;not null" json:"is_materialized"`
	CreatedAt        time.Time  `gorm:"index;not null" json:"created_at"`

	Document       *StudioDocument `gorm:"foreignKey:DocumentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ParentVersion  *StudioVersion  `gorm:"foreignKey:ParentVersionID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	PreferredChild *StudioVersion  `gorm:"foreignKey:PreferredChildID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	Snapshot       *StudioSnapshot `gorm:"foreignKey:SnapshotID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
}

// StudioOperation records the idempotent operation envelope that produced a specific StudioVersion.
type StudioOperation struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	DocumentID     uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_doc_idempotency;not null" json:"document_id"`
	VersionID      uuid.UUID `gorm:"type:uuid;uniqueIndex;not null" json:"version_id"`
	IdempotencyKey string    `gorm:"type:varchar(128);uniqueIndex:idx_doc_idempotency;not null" json:"idempotency_key"`
	OperationName  string    `gorm:"type:varchar(64);not null" json:"operation_name"`
	Parameters     JSON      `gorm:"type:jsonb;not null" json:"parameters"`
	TargetPageIDs  JSON      `gorm:"type:jsonb" json:"target_page_ids,omitempty"`
	CreatedAt      time.Time `json:"created_at"`

	Document *StudioDocument `gorm:"foreignKey:DocumentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Version  *StudioVersion  `gorm:"foreignKey:VersionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

// StudioSession tracks an active editing workspace session for a user or anonymous guest.
type StudioSession struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID          *uuid.UUID     `gorm:"type:uuid;index" json:"user_id,omitempty"`
	GuestTokenHash  string         `gorm:"type:varchar(64);index;not null" json:"-"`
	DocumentID      uuid.UUID      `gorm:"type:uuid;index;not null" json:"document_id"`
	ActiveVersionID uuid.UUID      `gorm:"type:uuid;not null" json:"active_version_id"`
	CreatedAt       time.Time      `json:"created_at"`
	LastAccessedAt  time.Time      `gorm:"index;not null" json:"last_accessed_at"`
	ExpiresAt       time.Time      `gorm:"index;not null" json:"expires_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`

	Document      *StudioDocument `gorm:"foreignKey:DocumentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ActiveVersion *StudioVersion  `gorm:"foreignKey:ActiveVersionID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
}

// StudioExport represents an ephemeral derived deliverable (PDF or Word DOCX) with independent retention.
type StudioExport struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	DocumentID   uuid.UUID `gorm:"type:uuid;index;not null" json:"document_id"`
	VersionID    uuid.UUID `gorm:"type:uuid;not null" json:"version_id"`
	ExportFormat string    `gorm:"type:varchar(16);not null" json:"export_format"` // pdf, docx
	R2Key        string    `gorm:"type:varchar(512);not null" json:"r2_key"`
	ByteSize     int64     `gorm:"not null" json:"byte_size"`
	ExpiresAt    time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`

	Document *StudioDocument `gorm:"foreignKey:DocumentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Version  *StudioVersion  `gorm:"foreignKey:VersionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

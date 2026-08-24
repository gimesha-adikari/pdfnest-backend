package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelFieldInvariants(t *testing.T) {
	// 1. StudioAsset MUST have R2Key, ByteSize, MimeType
	assetType := reflect.TypeOf(StudioAsset{})
	_, hasR2Key := assetType.FieldByName("R2Key")
	_, hasByteSize := assetType.FieldByName("ByteSize")
	_, hasMimeType := assetType.FieldByName("MimeType")
	assert.True(t, hasR2Key, "StudioAsset must have R2Key")
	assert.True(t, hasByteSize, "StudioAsset must have ByteSize")
	assert.True(t, hasMimeType, "StudioAsset must have MimeType")

	// 2. StudioSnapshot MUST have AssetID and MUST NOT have R2Key or ByteSize
	snapshotType := reflect.TypeOf(StudioSnapshot{})
	_, hasAssetID := snapshotType.FieldByName("AssetID")
	_, snapHasR2Key := snapshotType.FieldByName("R2Key")
	_, snapHasByteSize := snapshotType.FieldByName("ByteSize")
	assert.True(t, hasAssetID, "StudioSnapshot must have AssetID")
	assert.False(t, snapHasR2Key, "StudioSnapshot MUST NOT have R2Key (violates single source of truth)")
	assert.False(t, snapHasByteSize, "StudioSnapshot MUST NOT have ByteSize")

	// 3. StudioVersion MUST have SnapshotID and MUST NOT have SnapshotR2Key
	versionType := reflect.TypeOf(StudioVersion{})
	_, hasSnapshotID := versionType.FieldByName("SnapshotID")
	_, hasSnapshotR2Key := versionType.FieldByName("SnapshotR2Key")
	assert.True(t, hasSnapshotID, "StudioVersion must have SnapshotID")
	assert.False(t, hasSnapshotR2Key, "StudioVersion MUST NOT have SnapshotR2Key (violates single source of truth)")

	// 4. StudioOperation MUST have IdempotencyKey with unique index tag
	opType := reflect.TypeOf(StudioOperation{})
	idempotencyField, hasIdempotencyKey := opType.FieldByName("IdempotencyKey")
	require.True(t, hasIdempotencyKey, "StudioOperation must have IdempotencyKey")
	gormTag := idempotencyField.Tag.Get("gorm")
	assert.Contains(t, gormTag, "uniqueIndex:idx_doc_idempotency", "IdempotencyKey must have composite unique index tag")

	docIDField, hasDocID := opType.FieldByName("DocumentID")
	require.True(t, hasDocID, "StudioOperation must have DocumentID")
	docGormTag := docIDField.Tag.Get("gorm")
	assert.Contains(t, docGormTag, "uniqueIndex:idx_doc_idempotency", "DocumentID must participate in idx_doc_idempotency composite unique index")

	// 5. StudioOperation VersionID MUST be unique
	verIDField, hasOpVerID := opType.FieldByName("VersionID")
	require.True(t, hasOpVerID, "StudioOperation must have VersionID")
	assert.Contains(t, verIDField.Tag.Get("gorm"), "uniqueIndex", "StudioOperation VersionID must be unique")

	// 6. StudioSnapshot VersionID MUST be unique
	snapVerField, hasSnapVerID := snapshotType.FieldByName("VersionID")
	require.True(t, hasSnapVerID, "StudioSnapshot must have VersionID")
	assert.Contains(t, snapVerField.Tag.Get("gorm"), "uniqueIndex", "StudioSnapshot VersionID must be unique")
}

func TestJSONTypeSerialization(t *testing.T) {
	rawJSON := `{"document_id":"doc_123","pages":[{"page_id":"p_1","rotation":90,"crop_box":[0,0,500,700]}]}`
	var j JSON = []byte(rawJSON)

	// Test Value()
	val, err := j.Value()
	require.NoError(t, err)
	assert.Equal(t, rawJSON, val)

	// Test Scan()
	var scanned JSON
	err = scanned.Scan([]byte(rawJSON))
	require.NoError(t, err)
	assert.Equal(t, j, scanned)

	err = scanned.Scan(rawJSON)
	require.NoError(t, err)
	assert.Equal(t, j, scanned)

	// Test Scan(nil)
	var nilJSON JSON
	err = nilJSON.Scan(nil)
	require.NoError(t, err)
	assert.Nil(t, nilJSON)

	// Test MarshalJSON and UnmarshalJSON
	type wrapper struct {
		Model JSON `json:"model"`
	}
	w := wrapper{Model: j}
	bytes, err := json.Marshal(w)
	require.NoError(t, err)
	assert.Contains(t, string(bytes), `"rotation":90`)

	var unmarshaled wrapper
	err = json.Unmarshal(bytes, &unmarshaled)
	require.NoError(t, err)
	assert.Equal(t, j, unmarshaled.Model)
}

func TestModelInstantiationAndSerialization(t *testing.T) {
	docID := uuid.New()
	verID := uuid.New()
	snapID := uuid.New()
	assetID := "ast_sha256_e3b0c44298fc1c149afbf4c8996fb924"
	now := time.Now().UTC()

	doc := StudioDocument{
		ID:               docID,
		OriginalFileName: "contract.pdf",
		FileSize:         1048576,
		InitialPageCount: 10,
		CreatedAt:        now,
	}

	asset := StudioAsset{
		ID:         assetID,
		DocumentID: docID,
		AssetType:  "snapshot",
		R2Key:      "studio/snapshots/" + docID.String() + "/v1.pdf",
		ByteSize:   1048576,
		MimeType:   "application/pdf",
		CreatedAt:  now,
	}

	snap := StudioSnapshot{
		ID:        snapID,
		VersionID: verID,
		AssetID:   assetID,
		PageCount: 10,
		CreatedAt: now,
	}

	vdmRaw := `{"page_count":10,"pages":[]}`
	ver := StudioVersion{
		ID:             verID,
		DocumentID:     docID,
		VersionNumber:  1,
		Status:         "ready",
		OperationType:  "initial_upload",
		VirtualModel:   JSON([]byte(vdmRaw)),
		SnapshotID:     &snapID,
		IsMaterialized: true,
		CreatedAt:      now,
	}

	op := StudioOperation{
		ID:             uuid.New(),
		DocumentID:     docID,
		VersionID:      verID,
		IdempotencyKey: "idem_init_123",
		OperationName:  "upload",
		Parameters:     JSON([]byte(`{}`)),
		CreatedAt:      now,
	}

	sess := StudioSession{
		ID:              uuid.New(),
		GuestTokenHash:  "guest_hash_abc",
		DocumentID:      docID,
		ActiveVersionID: verID,
		CreatedAt:       now,
		LastAccessedAt:  now,
		ExpiresAt:       now.Add(24 * time.Hour),
	}

	export := StudioExport{
		ID:           uuid.New(),
		DocumentID:   docID,
		VersionID:    verID,
		ExportFormat: "pdf",
		R2Key:        "studio/exports/" + docID.String() + "/exp_1.pdf",
		ByteSize:     1048576,
		ExpiresAt:    now.Add(2 * time.Hour),
		CreatedAt:    now,
	}

	// Verify all JSON tags and field conversions
	docBytes, err := json.Marshal(doc)
	require.NoError(t, err)
	assert.Contains(t, string(docBytes), "contract.pdf")

	assetBytes, err := json.Marshal(asset)
	require.NoError(t, err)
	assert.Contains(t, string(assetBytes), assetID)

	snapBytes, err := json.Marshal(snap)
	require.NoError(t, err)
	assert.Contains(t, string(snapBytes), assetID)

	verBytes, err := json.Marshal(ver)
	require.NoError(t, err)
	assert.Contains(t, string(verBytes), "initial_upload")

	opBytes, err := json.Marshal(op)
	require.NoError(t, err)
	assert.Contains(t, string(opBytes), "idem_init_123")

	sessBytes, err := json.Marshal(sess)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(sessBytes), "guest_hash_abc"), "GuestTokenHash must be hidden by json:- tag")

	exportBytes, err := json.Marshal(export)
	require.NoError(t, err)
	assert.Contains(t, string(exportBytes), "export_format")
}

package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"gorm.io/gorm"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

// MaterializationName is the closed set of synchronous replacement-PDF tools.
// Job-backed tools deliberately belong to a later coordinator.
type MaterializationName string

const (
	MaterializeMerge     MaterializationName = "merge"
	MaterializeSplit     MaterializationName = "split"
	MaterializeCompress  MaterializationName = "compress"
	MaterializeGrayscale MaterializationName = "grayscale"
	MaterializeRepair    MaterializationName = "repair"
	MaterializeRedact    MaterializationName = "redact"
)

// MaterializationRequest contains typed-operation metadata and strict JSON
// parameters. The request never contains a caller-supplied VDM or storage key.
type MaterializationRequest struct {
	BaseVersionID  uuid.UUID           `json:"base_version_id"`
	IdempotencyKey string              `json:"idempotency_key"`
	Operation      MaterializationName `json:"operation"`
	Parameters     json.RawMessage     `json:"parameters"`
}

type MaterializationResult struct {
	Version            *models.StudioVersion   `json:"version"`
	Operation          *models.StudioOperation `json:"operation"`
	Asset              *models.StudioAsset     `json:"asset"`
	VDM                *vdm.DocumentModel      `json:"vdm"`
	Metrics            *CompressionMetrics     `json:"metrics,omitempty"`
	IsIdempotentReplay bool                    `json:"is_idempotent_replay"`
}

// CompressionMetrics are measured from the exact active materialized input
// and the validated output file. They deliberately do not include an
// optimistic percentage or processor-reported estimate.
type CompressionMetrics struct {
	InputBytes       int64   `json:"input_bytes"`
	OutputBytes      int64   `json:"output_bytes"`
	SavedBytes       int64   `json:"saved_bytes"`
	ReductionPercent float64 `json:"reduction_percent"`
}

type CompressParameters struct {
	Level string `json:"level"`
}

type MergeParameters struct {
	SourceAssetIDs          []string `json:"source_asset_ids"`
	CurrentDocumentPosition *int     `json:"current_document_position,omitempty"`
}

const maxStudioMergeAssets = 32

func validateMergeParameters(params MergeParameters) (int, error) {
	if len(params.SourceAssetIDs) == 0 || len(params.SourceAssetIDs) > maxStudioMergeAssets {
		return 0, ErrInvalidMaterialization
	}
	position := len(params.SourceAssetIDs)
	if params.CurrentDocumentPosition != nil {
		position = *params.CurrentDocumentPosition
	}
	if position < 0 || position > len(params.SourceAssetIDs) {
		return 0, ErrInvalidMaterialization
	}
	seen := make(map[string]struct{}, len(params.SourceAssetIDs))
	for _, assetID := range params.SourceAssetIDs {
		if assetID == "" {
			return 0, ErrInvalidMaterialization
		}
		if _, exists := seen[assetID]; exists {
			return 0, ErrInvalidMaterialization
		}
		seen[assetID] = struct{}{}
	}
	return position, nil
}

type SplitParameters struct {
	PageIDs []string `json:"page_ids"`
}

type RedactParameters struct {
	Keywords []string    `json:"keywords"`
	Boxes    []RedactBox `json:"boxes"`
}

// RedactBox is the trusted Studio-to-worker contract. Coordinates are
// normalized top-left coordinates in the visible, crop-relative page space;
// the worker converts them into its unrotated PDF context before applying
// permanent redactions.
type RedactBox struct {
	ID     string  `json:"id"`
	PageID string  `json:"page_id"`
	Page   int     `json:"page"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

const maxStudioRedactBoxes = 256

func validateRedactParameters(params RedactParameters, model *vdm.DocumentModel) error {
	if len(params.Boxes) > maxStudioRedactBoxes {
		return fmt.Errorf("too many redaction boxes")
	}
	seen := make(map[string]struct{}, len(params.Boxes))
	for _, box := range params.Boxes {
		if box.ID == "" || box.PageID == "" || box.Page < 1 || box.Page > len(model.Pages) {
			return fmt.Errorf("redaction box has an invalid page identity")
		}
		if _, exists := seen[box.ID]; exists {
			return fmt.Errorf("redaction box ID is duplicated")
		}
		seen[box.ID] = struct{}{}
		page := model.Pages[box.Page-1]
		if page.PageID != box.PageID {
			return fmt.Errorf("redaction box page does not belong to the current Studio version")
		}
		values := []float64{box.X, box.Y, box.Width, box.Height}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("redaction box geometry must be finite")
			}
		}
		if box.X < 0 || box.Y < 0 || box.Width <= 0 || box.Height <= 0 || box.X+box.Width > 1 || box.Y+box.Height > 1 {
			return fmt.Errorf("redaction box must stay within the page")
		}
	}
	return nil
}

// MaterializationProcessors are narrow adapters around the existing internal
// processing leaves. The coordinator owns lifecycle and persistence; these
// functions own only PDF transformation.
type MaterializationProcessors struct {
	Merge     func(inputPaths []string) (string, error)
	Split     func(inputPath string, pageSelection []string) (string, error)
	Compress  func(ctx context.Context, inputPath string, level ...string) (string, error)
	Grayscale func(ctx context.Context, inputPath, outputPath string) error
	Repair    func(inputPath, outputPath string) error
	Redact    func(inputPath, outputDir string, keywords []string, boxes string) (string, error)
}

type StudioMaterializationCoordinator interface {
	Execute(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, req MaterializationRequest) (*MaterializationResult, error)
}

type studioMaterializationCoordinator struct {
	repo         Repository
	materializer StudioVersionMaterializer
	processors   MaterializationProcessors
}

func NewMaterializationCoordinator(repo Repository, materializer StudioVersionMaterializer, processors MaterializationProcessors) StudioMaterializationCoordinator {
	return &studioMaterializationCoordinator{repo: repo, materializer: materializer, processors: processors}
}

func (c *studioMaterializationCoordinator) Execute(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, req MaterializationRequest) (*MaterializationResult, error) {
	if req.BaseVersionID == uuid.Nil || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 || !validMaterializationName(req.Operation) {
		return nil, ErrInvalidMaterialization
	}

	sess, err := c.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}

	// Match the existing Studio idempotency contract before doing expensive
	// processor work. A retry returns the durable result, not a new version.
	if replay, err := c.findReplay(ctx, sess.DocumentID, req); err != nil {
		return nil, err
	} else if replay != nil {
		return replay, nil
	}
	if sess.ActiveVersionID != req.BaseVersionID {
		return nil, ErrInvalidBaseVersion
	}

	current, err := c.materializer.MaterializeVersion(ctx, sessionID, ident)
	if err != nil {
		return nil, err
	}
	defer current.Cleanup()
	if current.Version.ID != req.BaseVersionID {
		return nil, ErrInvalidBaseVersion
	}
	inputInfo, err := os.Stat(current.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: stat active materialized input: %v", ErrMaterializationFailed, err)
	}
	if inputInfo.IsDir() || inputInfo.Size() < 0 {
		return nil, fmt.Errorf("%w: active materialized input is not a valid file", ErrMaterializationFailed)
	}

	workDir, err := os.MkdirTemp("", "pdfnest-studio-materialize-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create materialization workspace: %v", ErrMaterializationFailed, err)
	}
	defer os.RemoveAll(workDir)

	outputPath, err := c.runProcessor(ctx, current, req.Operation, req.Parameters, workDir)
	if err != nil {
		return nil, err
	}
	if outputPath == "" || filepath.Clean(outputPath) == filepath.Clean(current.Path) {
		return nil, fmt.Errorf("%w: processor returned the current input path", ErrMaterializationFailed)
	}
	defer os.Remove(outputPath)

	pageCount, err := validateMaterializedOutput(outputPath)
	if err != nil {
		return nil, err
	}
	derivedModel, err := deriveMaterializedVDM(current.Model, outputPath, pageCount)
	if err != nil {
		return nil, err
	}

	key := storage.BuildKey(filepath.ToSlash(filepath.Join("studio", "materialized", current.Document.ID.String())), ".pdf")
	if err := persistStudioPDF(ctx, outputPath, key); err != nil {
		return nil, fmt.Errorf("%w: persist materialized output: %v", ErrMaterializationFailed, err)
	}
	registered := false
	defer func() {
		if !registered {
			cleanupStudioObject(ctx, key)
		}
	}()

	info, err := os.Stat(outputPath)
	if err != nil || info.Size() <= 0 {
		return nil, fmt.Errorf("%w: stat validated output: %v", ErrMaterializationFailed, err)
	}
	result, err := c.persistResult(ctx, sessionID, ident, req, current, derivedModel, key, info.Size(), pageCount)
	if err != nil {
		return nil, err
	}
	if req.Operation == MaterializeCompress && result.Metrics == nil {
		result.Metrics = calculateCompressionMetrics(inputInfo.Size(), info.Size())
	}
	registered = !result.IsIdempotentReplay
	return result, nil
}

func (c *studioMaterializationCoordinator) findReplay(ctx context.Context, documentID uuid.UUID, req MaterializationRequest) (*MaterializationResult, error) {
	op, version, err := c.repo.FindOperationByIdempotencyKey(ctx, documentID, req.IdempotencyKey)
	if err != nil || op == nil || version == nil {
		return nil, err
	}
	canonicalParameters, err := canonicalJSON(req.Parameters)
	if err != nil {
		return nil, ErrInvalidMaterialization
	}
	mutation := operationMutation{
		BaseVersionID: req.BaseVersionID, IdempotencyKey: req.IdempotencyKey,
		OperationName: string(req.Operation), Parameters: req.Parameters, TargetPageIDs: materializationTargetPageIDsSlice(req), IsMaterialized: true,
	}
	if !sameOperationEnvelope(op, version, mutation, canonicalParameters) {
		return nil, ErrIdempotencyConflict
	}
	asset, modelState, err := c.versionAssetAndVDM(ctx, version)
	if err != nil {
		return nil, err
	}
	result := &MaterializationResult{Version: version, Operation: op, Asset: asset, VDM: modelState, IsIdempotentReplay: true}
	if req.Operation == MaterializeCompress {
		result.Metrics = c.replayCompressionMetrics(ctx, req, asset)
	}
	return result, nil
}

func (c *studioMaterializationCoordinator) replayCompressionMetrics(ctx context.Context, req MaterializationRequest, outputAsset *models.StudioAsset) *CompressionMetrics {
	if outputAsset == nil || outputAsset.ByteSize < 0 {
		return nil
	}
	base, err := c.repo.GetVersion(ctx, req.BaseVersionID)
	if err != nil || base.SnapshotID == nil || *base.SnapshotID == uuid.Nil {
		return nil
	}
	inputSnapshot, err := c.repo.GetSnapshot(ctx, *base.SnapshotID)
	if err != nil {
		return nil
	}
	inputAsset, err := c.repo.GetAsset(ctx, inputSnapshot.AssetID)
	if err != nil {
		return nil
	}
	return calculateCompressionMetrics(inputAsset.ByteSize, outputAsset.ByteSize)
}

func (c *studioMaterializationCoordinator) persistResult(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, req MaterializationRequest, current *MaterializedVersion, derivedModel *vdm.DocumentModel, key string, byteSize int64, pageCount int) (*MaterializationResult, error) {
	canonicalParameters, err := canonicalJSON(req.Parameters)
	if err != nil {
		return nil, ErrInvalidMaterialization
	}

	assetID := "studio-materialized-" + uuid.NewString()
	asset := &models.StudioAsset{ID: assetID, DocumentID: current.Document.ID, AssetType: "materialized", R2Key: key, ByteSize: byteSize, MimeType: "application/pdf"}

	for index := range derivedModel.Pages {
		if !derivedModel.Pages[index].IsBlank {
			derivedModel.Pages[index].SourceAssetID = &assetID
			derivedModel.Pages[index].SourcePageNumber = index + 1
		}
	}
	newVersionID := uuid.New()
	derivedModel.VersionID = newVersionID.String()
	vdmBytes, err := derivedModel.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("%w: derive output VDM: %v", ErrMaterializationFailed, err)
	}
	now := timeNowUTC()
	snapshot := &models.StudioSnapshot{ID: uuid.New(), VersionID: newVersionID, AssetID: assetID, PageCount: pageCount, CreatedAt: now}
	version := &models.StudioVersion{
		ID: newVersionID, DocumentID: current.Document.ID, ParentVersionID: &current.Version.ID,
		VersionNumber: current.Version.VersionNumber + 1, Status: "ready", OperationType: string(req.Operation),
		VirtualModel: models.JSON(vdmBytes), SnapshotID: &snapshot.ID, IsMaterialized: true, CreatedAt: now,
	}
	targetPages := materializationTargetPageIDs(req)
	op := &models.StudioOperation{
		ID: uuid.New(), DocumentID: current.Document.ID, VersionID: newVersionID,
		IdempotencyKey: req.IdempotencyKey, OperationName: string(req.Operation),
		Parameters: models.JSON(canonicalParameters), TargetPageIDs: targetPages, CreatedAt: now,
	}
	var result *MaterializationResult
	err = c.repo.WithTransaction(ctx, func(txRepo Repository, _ *gorm.DB) error {
		locked, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := validateSessionAccess(locked, ident); err != nil {
			return err
		}
		if existingOp, existingVer, findErr := txRepo.FindOperationByIdempotencyKey(ctx, locked.DocumentID, req.IdempotencyKey); findErr != nil {
			return findErr
		} else if existingOp != nil || existingVer != nil {
			if existingOp == nil || existingVer == nil || !sameOperationEnvelope(existingOp, existingVer, operationMutation{BaseVersionID: req.BaseVersionID, IdempotencyKey: req.IdempotencyKey, OperationName: string(req.Operation), Parameters: req.Parameters, TargetPageIDs: materializationTargetPageIDsSlice(req), IsMaterialized: true}, canonicalParameters) {
				return ErrIdempotencyConflict
			}
			existingAsset, existingModel, assetErr := c.versionAssetAndVDMWithRepo(ctx, txRepo, existingVer)
			if assetErr != nil {
				return assetErr
			}
			result = &MaterializationResult{Version: existingVer, Operation: existingOp, Asset: existingAsset, VDM: existingModel, IsIdempotentReplay: true}
			return nil
		}
		if locked.DocumentID != current.Document.ID || locked.ActiveVersionID != req.BaseVersionID {
			return ErrConflict
		}
		if err := txRepo.CreateAsset(ctx, asset); err != nil {
			return err
		}
		if err := txRepo.CreateSnapshot(ctx, snapshot); err != nil {
			return err
		}
		if err := txRepo.CreateVersionAndOperation(ctx, version, op, sessionID, &current.Version.ID); err != nil {
			return err
		}
		result = &MaterializationResult{Version: version, Operation: op, Asset: asset, VDM: derivedModel}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *studioMaterializationCoordinator) versionAssetAndVDM(ctx context.Context, version *models.StudioVersion) (*models.StudioAsset, *vdm.DocumentModel, error) {
	return c.versionAssetAndVDMWithRepo(ctx, c.repo, version)
}

func (c *studioMaterializationCoordinator) versionAssetAndVDMWithRepo(ctx context.Context, repo Repository, version *models.StudioVersion) (*models.StudioAsset, *vdm.DocumentModel, error) {
	if version.SnapshotID == nil || *version.SnapshotID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: materialized version has no snapshot", ErrMaterializationFailed)
	}
	snapshot, err := repo.GetSnapshot(ctx, *version.SnapshotID)
	if err != nil {
		return nil, nil, err
	}
	asset, err := repo.GetAsset(ctx, snapshot.AssetID)
	if err != nil {
		return nil, nil, err
	}
	modelState, err := vdm.FromJSON(version.VirtualModel)
	if err != nil {
		return nil, nil, err
	}
	return asset, modelState, nil
}

func (c *studioMaterializationCoordinator) runProcessor(ctx context.Context, current *MaterializedVersion, operation MaterializationName, raw json.RawMessage, workDir string) (string, error) {
	switch operation {
	case MaterializeCompress:
		var params CompressParameters
		if err := decodeStrictParameters(raw, &params); err != nil {
			return "", ErrInvalidMaterialization
		}
		level := strings.TrimSpace(params.Level)
		if level == "" {
			level = "medium"
		}
		if !validCompressionLevel(level) {
			return "", ErrInvalidMaterialization
		}
		if c.processors.Compress == nil {
			return "", ErrMaterializationProcessorUnavailable
		}
		output, err := c.processors.Compress(ctx, current.Path, level)
		if err != nil {
			return "", fmt.Errorf("%w: compress: %v", ErrMaterializationFailed, err)
		}
		return output, nil
	case MaterializeGrayscale:
		var params struct{}
		if err := decodeStrictParameters(raw, &params); err != nil {
			return "", ErrInvalidMaterialization
		}
		if c.processors.Grayscale == nil {
			return "", ErrMaterializationProcessorUnavailable
		}
		output := filepath.Join(workDir, "grayscale.pdf")
		if err := c.processors.Grayscale(ctx, current.Path, output); err != nil {
			return "", fmt.Errorf("%w: grayscale: %v", ErrMaterializationFailed, err)
		}
		return output, nil
	case MaterializeRepair:
		var params struct{}
		if err := decodeStrictParameters(raw, &params); err != nil {
			return "", ErrInvalidMaterialization
		}
		if c.processors.Repair == nil {
			return "", ErrMaterializationProcessorUnavailable
		}
		output := filepath.Join(workDir, "repaired.pdf")
		if err := c.processors.Repair(current.Path, output); err != nil {
			return "", fmt.Errorf("%w: repair: %v", ErrMaterializationFailed, err)
		}
		return output, nil
	case MaterializeRedact:
		var params RedactParameters
		if len(raw) > 64*1024 || decodeStrictParameters(raw, &params) != nil || (len(params.Keywords) == 0 && len(params.Boxes) == 0) || validateRedactParameters(params, current.Model) != nil {
			return "", ErrInvalidMaterialization
		}
		if c.processors.Redact == nil {
			return "", ErrMaterializationProcessorUnavailable
		}
		workerBoxes, err := json.Marshal(params.Boxes)
		if err != nil {
			return "", ErrInvalidMaterialization
		}
		fileName, err := c.processors.Redact(current.Path, workDir, params.Keywords, string(workerBoxes))
		if err != nil {
			return "", fmt.Errorf("%w: redact: %v", ErrMaterializationFailed, err)
		}
		output := filepath.Join(workDir, filepath.Base(fileName))
		if filepath.Dir(output) != filepath.Clean(workDir) {
			return "", ErrInvalidMaterialization
		}
		return output, nil
	case MaterializeSplit:
		var params SplitParameters
		if err := decodeStrictParameters(raw, &params); err != nil || len(params.PageIDs) == 0 {
			return "", ErrInvalidMaterialization
		}
		if c.processors.Split == nil {
			return "", ErrMaterializationProcessorUnavailable
		}
		indices := make([]string, 0, len(params.PageIDs))
		seen := make(map[string]struct{}, len(params.PageIDs))
		for _, pageID := range params.PageIDs {
			if _, ok := seen[pageID]; ok {
				return "", ErrInvalidMaterialization
			}
			seen[pageID] = struct{}{}
			index := -1
			for i := range current.Model.Pages {
				if current.Model.Pages[i].PageID == pageID {
					index = i + 1
					break
				}
			}
			if index < 1 {
				return "", ErrCommandPageNotFound
			}
			indices = append(indices, strconv.Itoa(index))
		}
		output, err := c.processors.Split(current.Path, indices)
		if err != nil {
			return "", fmt.Errorf("%w: split: %v", ErrMaterializationFailed, err)
		}
		return output, nil
	case MaterializeMerge:
		var params MergeParameters
		if err := decodeStrictParameters(raw, &params); err != nil {
			return "", ErrInvalidMaterialization
		}
		currentPosition, err := validateMergeParameters(params)
		if err != nil {
			return "", ErrInvalidMaterialization
		}
		if c.processors.Merge == nil {
			return "", ErrMaterializationProcessorUnavailable
		}
		inputs := make([]string, 0, len(params.SourceAssetIDs)+1)
		assetPaths := make([]string, 0, len(params.SourceAssetIDs))
		cleanups := make([]func(), 0, len(params.SourceAssetIDs))
		defer func() {
			for _, cleanup := range cleanups {
				cleanup()
			}
		}()
		for _, assetID := range params.SourceAssetIDs {
			asset, err := c.repo.GetAsset(ctx, assetID)
			if err != nil {
				return "", err
			}
			if asset.DocumentID != current.Document.ID {
				return "", ErrUnauthorized
			}
			if asset.AssetType != "merge_source" {
				return "", ErrInvalidMaterialization
			}
			path, cleanup, err := storage.ResolveObject(ctx, asset.R2Key, "pdfnest-studio-merge", ".pdf")
			if err != nil {
				return "", err
			}
			cleanups = append(cleanups, cleanup)
			assetPaths = append(assetPaths, path)
		}
		inputs = orderedMergeInputs(assetPaths, current.Path, currentPosition)
		if len(inputs) != len(params.SourceAssetIDs)+1 {
			return "", ErrInvalidMaterialization
		}
		output, err := c.processors.Merge(inputs)
		if err != nil {
			return "", fmt.Errorf("%w: merge: %v", ErrMaterializationFailed, err)
		}
		return output, nil
	default:
		return "", ErrInvalidMaterialization
	}
}

func orderedMergeInputs(assets []string, current string, currentPosition int) []string {
	result := make([]string, 0, len(assets)+1)
	for index, asset := range assets {
		if index == currentPosition {
			result = append(result, current)
		}
		result = append(result, asset)
	}
	if currentPosition == len(assets) {
		result = append(result, current)
	}
	return result
}

func deriveMaterializedVDM(base *vdm.DocumentModel, outputPath string, pageCount int) (*vdm.DocumentModel, error) {
	dimensions, err := api.PageDimsFile(outputPath)
	if err != nil || len(dimensions) != pageCount {
		return nil, fmt.Errorf("%w: inspect materialized page dimensions: %v", ErrMaterializationFailed, err)
	}
	assetID := "pending-materialized-asset"
	pages := make([]vdm.PageDescriptor, 0, pageCount)
	for i, dimension := range dimensions {
		pages = append(pages, vdm.PageDescriptor{
			PageID: uuid.NewString(), SourceAssetID: &assetID, SourcePageNumber: i + 1,
			Dimensions: &vdm.Dimensions{Width: dimension.Width, Height: dimension.Height}, Rotation: 0, Overlays: []vdm.Overlay{},
		})
	}
	metadata := make(map[string]string, len(base.Metadata))
	for key, value := range base.Metadata {
		metadata[key] = value
	}
	return &vdm.DocumentModel{DocumentID: base.DocumentID, PageCount: pageCount, Pages: pages, Metadata: metadata}, nil
}

func validateMaterializedOutput(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return 0, fmt.Errorf("%w: output is missing or empty", ErrInvalidMaterializedOutput)
	}
	if err := api.ValidateFile(path, model.NewDefaultConfiguration()); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidMaterializedOutput, err)
	}
	pageCount, err := api.PageCountFile(path)
	if err != nil || pageCount < 1 {
		return 0, fmt.Errorf("%w: output page count is invalid", ErrInvalidMaterializedOutput)
	}
	return pageCount, nil
}

func materializationTargetPageIDs(req MaterializationRequest) models.JSON {
	ids := materializationTargetPageIDsSlice(req)
	if len(ids) == 0 {
		return nil
	}
	data, _ := json.Marshal(ids)
	return models.JSON(data)
}

func materializationTargetPageIDsSlice(req MaterializationRequest) []string {
	if req.Operation != MaterializeSplit {
		return nil
	}
	var params SplitParameters
	if err := decodeStrictParameters(req.Parameters, &params); err != nil {
		return nil
	}
	return params.PageIDs
}

func validMaterializationName(name MaterializationName) bool {
	switch name {
	case MaterializeMerge, MaterializeSplit, MaterializeCompress, MaterializeGrayscale, MaterializeRepair, MaterializeRedact:
		return true
	default:
		return false
	}
}

func validCompressionLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func calculateCompressionMetrics(inputBytes, outputBytes int64) *CompressionMetrics {
	if inputBytes < 0 {
		inputBytes = 0
	}
	if outputBytes < 0 {
		outputBytes = 0
	}
	savedBytes := inputBytes - outputBytes
	if savedBytes < 0 {
		savedBytes = 0
	}
	reductionPercent := 0.0
	if inputBytes > 0 {
		reductionPercent = float64(savedBytes) / float64(inputBytes) * 100
	}
	return &CompressionMetrics{
		InputBytes: inputBytes, OutputBytes: outputBytes,
		SavedBytes: savedBytes, ReductionPercent: reductionPercent,
	}
}

func timeNowUTC() (now time.Time) {
	return time.Now().UTC()
}

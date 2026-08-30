package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/markup"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

// StudioJobName is a closed, typed adapter set. It is intentionally not a
// generic worker-command passthrough.
type StudioJobName string

const (
	StudioJobMarkupHighlight StudioJobName = "markup_highlight"
	StudioJobMarkupUnderline StudioJobName = "markup_underline"
	StudioJobMarkupStrikeout StudioJobName = "markup_strikeout"
	StudioJobEditExtract     StudioJobName = "editor_extract"
	StudioJobEditCompile     StudioJobName = "editor_compile"
)

type MarkupJobParameters struct {
	Boxes []markup.Box     `json:"boxes"`
	Mode  StudioMarkupMode `json:"mode"`
}

// StudioMarkupMode is the public closed set. The worker's internal "text"
// mode is deliberately not accepted at this boundary.
type StudioMarkupMode string

const (
	StudioMarkupModeManual StudioMarkupMode = "manual"
	StudioMarkupModeSmart  StudioMarkupMode = "smart"
	StudioMarkupModeOCR    StudioMarkupMode = "ocr"
)

func validStudioMarkupMode(mode StudioMarkupMode) bool {
	switch mode {
	case StudioMarkupModeManual, StudioMarkupModeSmart, StudioMarkupModeOCR:
		return true
	default:
		return false
	}
}

type EditExtractJobParameters struct{}
type EditCompileJobParameters struct {
	EditorStateID uuid.UUID       `json:"editor_state_id"`
	Layout        json.RawMessage `json:"layout"`
}

type StudioJobRequest struct {
	BaseVersionID  uuid.UUID       `json:"base_version_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Operation      StudioJobName   `json:"operation"`
	Parameters     json.RawMessage `json:"parameters"`
}
type StudioJobResult struct {
	Job                *models.StudioJob `json:"job"`
	IsIdempotentReplay bool              `json:"is_idempotent_replay"`
}
type workerJobStatus struct {
	ID, Status, Message, Error string
	Progress                   int
	Result                     map[string]any
}

// StudioWorkerGateway is the only Studio-to-worker seam. Its production
// implementation uses existing signed Go service clients; Studio never calls
// its own public /api/edit or /api/markup HTTP routes.
type StudioWorkerGateway interface {
	SubmitMarkup(context.Context, StudioJobName, string, string, string) (workerJobStatus, error)
	SubmitEdit(context.Context, StudioJobName, string, string, string) (workerJobStatus, error)
	Status(context.Context, StudioJobName, string) (workerJobStatus, error)
	Cancel(context.Context, StudioJobName, string) (workerJobStatus, error)
	Download(context.Context, StudioJobName, string) (*http.Response, error)
}

type studioWorkerGateway struct {
	edit   edit.Service
	markup markup.Service
}

func NewStudioWorkerGateway(editService edit.Service, markupService markup.Service) StudioWorkerGateway {
	return &studioWorkerGateway{edit: editService, markup: markupService}
}
func workerStatusFrom(id, status string, progress int, message, failure string, result map[string]any) workerJobStatus {
	return workerJobStatus{ID: id, Status: status, Progress: progress, Message: message, Error: failure, Result: result}
}
func (g *studioWorkerGateway) SubmitMarkup(_ context.Context, op StudioJobName, source, payload, name string) (workerJobStatus, error) {
	var s *markup.WorkerJobSubmission
	var e error
	switch op {
	case StudioJobMarkupHighlight:
		s, e = g.markup.HighlightPDF(source, payload, name)
	case StudioJobMarkupUnderline:
		s, e = g.markup.UnderlinePDF(source, payload, name)
	case StudioJobMarkupStrikeout:
		s, e = g.markup.StrikeoutPDF(source, payload, name)
	default:
		return workerJobStatus{}, ErrInvalidJob
	}
	if e != nil {
		return workerJobStatus{}, e
	}
	return workerStatusFrom(s.JobID, s.Status, 0, "", "", nil), nil
}
func (g *studioWorkerGateway) SubmitEdit(_ context.Context, op StudioJobName, source, payload, name string) (workerJobStatus, error) {
	var s *edit.WorkerJobSubmission
	var e error
	switch op {
	case StudioJobEditExtract:
		if v2, ok := g.edit.(edit.OCRV2Service); ok {
			s, e = v2.ExtractLayoutV2(source, "", name)
		} else {
			s, e = g.edit.ExtractLayout(source, "", name)
		}
	case StudioJobEditCompile:
		s, e = g.edit.CompileLayout(source, payload, name)
	default:
		return workerJobStatus{}, ErrInvalidJob
	}
	if e != nil {
		return workerJobStatus{}, e
	}
	return workerStatusFrom(s.JobID, s.Status, 0, "", "", nil), nil
}
func (g *studioWorkerGateway) Status(_ context.Context, op StudioJobName, id string) (workerJobStatus, error) {
	if strings.HasPrefix(string(op), "markup_") {
		r, e := g.markup.GetJobStatus(id)
		if e != nil {
			return workerJobStatus{}, e
		}
		return workerStatusFrom(r.ID, r.Status, r.Progress, r.Message, r.Error, r.Result), nil
	}
	r, e := g.edit.GetJobStatus(id)
	if e != nil {
		return workerJobStatus{}, e
	}
	return workerStatusFrom(r.ID, r.Status, r.Progress, r.Message, r.Error, r.Result), nil
}
func (g *studioWorkerGateway) Cancel(_ context.Context, op StudioJobName, id string) (workerJobStatus, error) {
	if strings.HasPrefix(string(op), "markup_") {
		r, e := g.markup.CancelJob(id)
		if e != nil {
			return workerJobStatus{}, e
		}
		return workerStatusFrom(r.ID, r.Status, r.Progress, r.Message, r.Error, r.Result), nil
	}
	r, e := g.edit.CancelJob(id)
	if e != nil {
		return workerJobStatus{}, e
	}
	return workerStatusFrom(r.ID, r.Status, r.Progress, r.Message, r.Error, r.Result), nil
}
func (g *studioWorkerGateway) Download(_ context.Context, op StudioJobName, id string) (*http.Response, error) {
	if strings.HasPrefix(string(op), "markup_") {
		return g.markup.GetJobDownload(id)
	}
	return g.edit.GetJobDownload(id)
}

type StudioJobCoordinator interface {
	Submit(context.Context, uuid.UUID, identity.Identity, StudioJobRequest) (*StudioJobResult, error)
	Get(context.Context, uuid.UUID, uuid.UUID, identity.Identity) (*models.StudioJob, error)
	Cancel(context.Context, uuid.UUID, uuid.UUID, identity.Identity) (*models.StudioJob, error)
	GetEditorState(context.Context, uuid.UUID, uuid.UUID, identity.Identity) (*models.StudioEditorState, error)
}
type studioJobCoordinator struct {
	repo         Repository
	materializer StudioVersionMaterializer
	gateway      StudioWorkerGateway
}

func NewJobCoordinator(repo Repository, materializer StudioVersionMaterializer, editService edit.Service, markupService markup.Service) StudioJobCoordinator {
	return &studioJobCoordinator{repo: repo, materializer: materializer, gateway: NewStudioWorkerGateway(editService, markupService)}
}
func newJobCoordinatorForGateway(repo Repository, materializer StudioVersionMaterializer, gateway StudioWorkerGateway) StudioJobCoordinator {
	return &studioJobCoordinator{repo: repo, materializer: materializer, gateway: gateway}
}

func (c *studioJobCoordinator) Submit(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, req StudioJobRequest) (*StudioJobResult, error) {
	if req.BaseVersionID == uuid.Nil || strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 || !validStudioJob(req.Operation) {
		return nil, ErrInvalidJob
	}
	sess, err := c.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err = validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}
	canonical, err := canonicalJSON(req.Parameters)
	if err != nil {
		return nil, ErrInvalidJob
	}
	if old, err := c.repo.FindJobByIdempotencyKey(ctx, sess.DocumentID, req.IdempotencyKey); err != nil {
		return nil, err
	} else if old != nil {
		persistedCanonical, canonicalErr := canonicalJSON(json.RawMessage(old.Parameters))
		if canonicalErr != nil || old.BaseVersionID != req.BaseVersionID || old.JobType != string(req.Operation) || !bytes.Equal(persistedCanonical, canonical) {
			return nil, ErrIdempotencyConflict
		}
		return &StudioJobResult{Job: old, IsIdempotentReplay: true}, nil
	}
	if req.Operation != StudioJobEditCompile && sess.ActiveVersionID != req.BaseVersionID {
		return nil, ErrInvalidBaseVersion
	}
	var compileState *models.StudioEditorState
	if req.Operation == StudioJobEditCompile {
		var p EditCompileJobParameters
		if decodeStrictParameters(canonical, &p) != nil || p.EditorStateID == uuid.Nil {
			return nil, ErrInvalidJob
		}
		compileState, err = c.repo.GetEditorState(ctx, p.EditorStateID)
		if err != nil {
			return nil, err
		}
		if compileState.DocumentID != sess.DocumentID || compileState.SessionID != sessionID || compileState.BaseVersionID != req.BaseVersionID {
			return nil, ErrInvalidBaseVersion
		}
		baseLayout, _, err := decodeEditorLayout(compileState.Layout)
		if err != nil {
			return nil, ErrInvalidJob
		}
		editedLayout, _, err := decodeEditorLayout(p.Layout)
		if err != nil || validateEditedEditorLayout(baseLayout, editedLayout) != nil {
			return nil, ErrInvalidJob
		}
	}
	var current *MaterializedVersion
	if req.Operation == StudioJobEditCompile {
		if byID, ok := c.materializer.(StudioVersionMaterializerByID); ok {
			current, err = byID.MaterializeVersionByID(ctx, sessionID, req.BaseVersionID, ident)
		} else {
			return nil, ErrInvalidBaseVersion
		}
	} else {
		current, err = c.materializer.MaterializeVersion(ctx, sessionID, ident)
	}
	if err != nil {
		return nil, err
	}
	defer current.Cleanup()
	if current.Version.ID != req.BaseVersionID {
		return nil, ErrInvalidBaseVersion
	}
	sourceKey, err := stageStudioJobSource(ctx, current.Path, current.Document.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: stage source: %v", ErrJobReconciliationFailed, err)
	}
	payloadKey, err := c.stagePayload(ctx, req.Operation, canonical, current.Document.ID)
	if err != nil {
		cleanupStudioObject(ctx, sourceKey)
		return nil, fmt.Errorf("%w: stage payload: %v", ErrJobReconciliationFailed, err)
	}
	var submitted workerJobStatus
	if strings.HasPrefix(string(req.Operation), "markup_") {
		submitted, err = c.gateway.SubmitMarkup(ctx, req.Operation, sourceKey, payloadKey, current.Document.OriginalFileName)
	} else {
		submitted, err = c.gateway.SubmitEdit(ctx, req.Operation, sourceKey, payloadKey, current.Document.OriginalFileName)
	}
	if err != nil {
		cleanupStudioObject(ctx, sourceKey)
		cleanupStudioObject(ctx, payloadKey)
		return nil, fmt.Errorf("%w: submit worker: %v", ErrJobReconciliationFailed, err)
	}
	now := time.Now().UTC()
	job := &models.StudioJob{ID: uuid.New(), DocumentID: current.Document.ID, SessionID: sessionID, BaseVersionID: req.BaseVersionID, WorkerJobID: submitted.ID, JobType: string(req.Operation), Status: submitted.Status, Progress: submitted.Progress, Message: submitted.Message, IdempotencyKey: req.IdempotencyKey, Parameters: models.JSON(canonical), SourceKey: sourceKey, PayloadKey: payloadKey, CreatedAt: now, UpdatedAt: now}
	if compileState != nil {
		job.EditorStateID = &compileState.ID
	}
	if err := c.repo.CreateJob(ctx, job); err != nil {
		cleanupStudioObject(ctx, sourceKey)
		cleanupStudioObject(ctx, payloadKey)
		return nil, err
	}
	return &StudioJobResult{Job: job}, nil
}

func (c *studioJobCoordinator) stagePayload(ctx context.Context, op StudioJobName, canonical []byte, documentID uuid.UUID) (string, error) {
	var payload []byte
	var err error
	switch op {
	case StudioJobMarkupHighlight, StudioJobMarkupUnderline, StudioJobMarkupStrikeout:
		var p MarkupJobParameters
		if err = decodeStrictParameters(canonical, &p); err != nil || len(p.Boxes) == 0 {
			return "", ErrInvalidJob
		}
		if p.Mode == "" {
			p.Mode = StudioMarkupModeSmart
		}
		if !validStudioMarkupMode(p.Mode) {
			return "", ErrInvalidJob
		}
		payload, err = json.Marshal(struct {
			MarkupJobParameters
			OCRV2 bool `json:"ocr_v2"`
		}{MarkupJobParameters: p, OCRV2: true})
	case StudioJobEditExtract:
		var p EditExtractJobParameters
		if err = decodeStrictParameters(canonical, &p); err != nil {
			return "", ErrInvalidJob
		}
		return "", nil
	case StudioJobEditCompile:
		var p EditCompileJobParameters
		if err = decodeStrictParameters(canonical, &p); err != nil || p.EditorStateID == uuid.Nil || len(p.Layout) == 0 {
			return "", ErrInvalidJob
		}
		_, payload, err = decodeEditorLayout(p.Layout)
	default:
		return "", ErrInvalidJob
	}
	if err != nil {
		return "", err
	}
	return stageStudioJobBytes(ctx, payload, documentID, "payload")
}

func (c *studioJobCoordinator) GetEditorState(ctx context.Context, sessionID, stateID uuid.UUID, ident identity.Identity) (*models.StudioEditorState, error) {
	sess, err := c.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err = validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}
	state, err := c.repo.GetEditorState(ctx, stateID)
	if err != nil {
		return nil, err
	}
	if state.DocumentID != sess.DocumentID || state.SessionID != sessionID {
		return nil, ErrEditorStateNotFound
	}
	return state, nil
}

func stageStudioJobSource(ctx context.Context, path string, documentID uuid.UUID) (string, error) {
	key := storage.BuildKey(filepath.ToSlash(filepath.Join("studio", "jobs", "staging", documentID.String(), "source")), ".pdf")
	return key, persistStudioPDF(ctx, path, key)
}
func stageStudioJobBytes(ctx context.Context, data []byte, documentID uuid.UUID, kind string) (string, error) {
	key := storage.BuildKey(filepath.ToSlash(filepath.Join("studio", "jobs", "staging", documentID.String(), kind)), ".json")
	if store, err := storage.Default(); err == nil && store != nil {
		return key, store.UploadBytes(data, key, "application/json")
	}
	_, _, err := storage.SaveLocalStream(ctx, key, bytes.NewReader(data))
	return key, err
}

func (c *studioJobCoordinator) Get(ctx context.Context, sessionID, jobID uuid.UUID, ident identity.Identity) (*models.StudioJob, error) {
	job, err := c.repo.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.SessionID != sessionID {
		return nil, ErrJobNotFound
	}
	sess, err := c.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err = validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}
	return c.reconcile(ctx, job)
}
func (c *studioJobCoordinator) Cancel(ctx context.Context, sessionID, jobID uuid.UUID, ident identity.Identity) (*models.StudioJob, error) {
	job, err := c.Get(ctx, sessionID, jobID, ident)
	if err != nil {
		return nil, err
	}
	if terminalStudioJob(job.Status) {
		return job, nil
	}
	status, err := c.gateway.Cancel(ctx, StudioJobName(job.JobType), job.WorkerJobID)
	if err != nil {
		return nil, err
	}
	applyWorkerStatus(job, status)
	if err = c.repo.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	if terminalStudioJob(job.Status) {
		c.cleanupStaging(ctx, job)
	}
	return job, nil
}
func (c *studioJobCoordinator) reconcile(ctx context.Context, job *models.StudioJob) (*models.StudioJob, error) {
	if terminalStudioJob(job.Status) {
		return job, nil
	}
	status, err := c.gateway.Status(ctx, StudioJobName(job.JobType), job.WorkerJobID)
	if err != nil {
		return nil, err
	}
	applyWorkerStatus(job, status)
	if job.Status == "succeeded" {
		if err := c.reconcileSuccess(ctx, job); err != nil {
			return nil, err
		}
	} else if err := c.repo.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	if terminalStudioJob(job.Status) {
		c.cleanupStaging(ctx, job)
	}
	return job, nil
}
func applyWorkerStatus(job *models.StudioJob, s workerJobStatus) {
	job.Status = s.Status
	job.Progress = s.Progress
	job.Message = s.Message
	job.Error = s.Error
	if s.Result != nil {
		b, _ := json.Marshal(s.Result)
		job.Result = models.JSON(b)
	}
	job.UpdatedAt = time.Now().UTC()
}
func terminalStudioJob(s string) bool { return s == "succeeded" || s == "failed" || s == "cancelled" }
func validStudioJob(op StudioJobName) bool {
	switch op {
	case StudioJobMarkupHighlight, StudioJobMarkupUnderline, StudioJobMarkupStrikeout, StudioJobEditExtract, StudioJobEditCompile:
		return true
	}
	return false
}

func (c *studioJobCoordinator) reconcileSuccess(ctx context.Context, job *models.StudioJob) error {
	if job.ReconciledAt != nil {
		return nil
	}
	if StudioJobName(job.JobType) == StudioJobEditExtract {
		return c.reconcileEditorExtract(ctx, job)
	}
	resp, err := c.gateway.Download(ctx, StudioJobName(job.JobType), job.WorkerJobID)
	if err != nil {
		return fmt.Errorf("%w: download worker artifact: %v", ErrJobReconciliationFailed, err)
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp("", "pdfnest-studio-job-result-*.pdf")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err = io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	pages, err := validateMaterializedOutput(path)
	if err != nil {
		return err
	}
	base, err := c.repo.GetVersion(ctx, job.BaseVersionID)
	if err != nil {
		return err
	}
	baseModel, err := vdm.FromJSON(base.VirtualModel)
	if err != nil {
		return err
	}
	modelState, err := deriveMaterializedVDM(baseModel, path, pages)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	key := storage.BuildKey(filepath.ToSlash(filepath.Join("studio", "materialized", job.DocumentID.String())), ".pdf")
	if err = persistStudioPDF(ctx, path, key); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			cleanupStudioObject(ctx, key)
		}
	}()
	assetID := "studio-job-" + uuid.NewString()
	for i := range modelState.Pages {
		modelState.Pages[i].SourceAssetID = &assetID
		modelState.Pages[i].SourcePageNumber = i + 1
	}
	versionID := uuid.New()
	modelState.VersionID = versionID.String()
	vdmBytes, err := modelState.ToJSON()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	snap := &models.StudioSnapshot{ID: uuid.New(), VersionID: versionID, AssetID: assetID, PageCount: pages, CreatedAt: now}
	asset := &models.StudioAsset{ID: assetID, DocumentID: job.DocumentID, AssetType: "job_result", R2Key: key, ByteSize: info.Size(), MimeType: "application/pdf"}
	ver := &models.StudioVersion{ID: versionID, DocumentID: job.DocumentID, ParentVersionID: &job.BaseVersionID, VersionNumber: base.VersionNumber + 1, Status: "ready", OperationType: job.JobType, VirtualModel: models.JSON(vdmBytes), SnapshotID: &snap.ID, IsMaterialized: true, CreatedAt: now}
	op := &models.StudioOperation{ID: uuid.New(), DocumentID: job.DocumentID, VersionID: versionID, IdempotencyKey: job.IdempotencyKey, OperationName: job.JobType, Parameters: job.Parameters, CreatedAt: now}
	err = c.repo.WithTransaction(ctx, func(tx Repository, _ *gorm.DB) error {
		locked, err := tx.LockSession(ctx, job.SessionID)
		if err != nil {
			return err
		}
		if err = tx.CreateAsset(ctx, asset); err != nil {
			return err
		}
		if err = tx.CreateSnapshot(ctx, snap); err != nil {
			return err
		}
		if locked.ActiveVersionID == job.BaseVersionID {
			if err = tx.CreateVersionAndOperation(ctx, ver, op, job.SessionID, &job.BaseVersionID); err != nil {
				return err
			}
		} else {
			if err = tx.CreateDetachedVersionAndOperation(ctx, ver, op); err != nil {
				return err
			}
		}
		job.ResultVersionID = &versionID
		job.ReconciledAt = &now
		return tx.SaveJob(ctx, job)
	})
	if err != nil {
		return err
	}
	registered = true
	c.cleanupStaging(ctx, job)
	return nil
}

func (c *studioJobCoordinator) reconcileEditorExtract(ctx context.Context, job *models.StudioJob) error {
	if state, err := c.repo.GetEditorStateByExtractJob(ctx, job.ID); err != nil {
		return err
	} else if state != nil {
		job.EditorStateID = &state.ID
		now := time.Now().UTC()
		job.ReconciledAt = &now
		_ = c.repo.SaveJob(ctx, job)
		c.cleanupStaging(ctx, job)
		return nil
	}
	layout, canonical, err := decodeEditorLayout(job.Result)
	if err != nil {
		job.Status = "failed"
		job.Error = "worker returned invalid editor layout"
		return c.repo.SaveJob(ctx, job)
	}
	_ = layout
	now := time.Now().UTC()
	state := &models.StudioEditorState{ID: uuid.New(), DocumentID: job.DocumentID, SessionID: job.SessionID, BaseVersionID: job.BaseVersionID, ExtractJobID: job.ID, Layout: models.JSON(canonical), CreatedAt: now}
	if err = c.repo.WithTransaction(ctx, func(tx Repository, _ *gorm.DB) error {
		if err := tx.CreateEditorState(ctx, state); err != nil {
			return err
		}
		job.EditorStateID = &state.ID
		job.ReconciledAt = &now
		return tx.SaveJob(ctx, job)
	}); err != nil {
		return err
	}
	c.cleanupStaging(ctx, job)
	return nil
}
func (c *studioJobCoordinator) cleanupStaging(ctx context.Context, job *models.StudioJob) {
	cleanupStudioObject(ctx, job.SourceKey)
	cleanupStudioObject(ctx, job.PayloadKey)
	job.SourceKey = ""
	job.PayloadKey = ""
	_ = c.repo.SaveJob(ctx, job)
}

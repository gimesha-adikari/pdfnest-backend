package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

type EditExtractJobParameters struct {
	LanguageMode string   `json:"language_mode"`
	Languages    []string `json:"languages"`
}
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
	ErrorCode                  string
	Progress                   int
	Result                     map[string]any
}

// preparedStudioResult contains only the deterministic, server-derived state
// required to register a completed worker artifact. It is intentionally
// process-local: the artifact bytes stay in controlled temporary storage and
// the durable object is owned by a unique key until finalization wins.
type preparedStudioResult struct {
	jobID             uuid.UUID
	sessionID         uuid.UUID
	documentID        uuid.UUID
	baseVersionID     uuid.UUID
	workerJobID       string
	jobType           string
	baseVersionNumber int
	materializedKey   string
	pageCount         int
	byteSize          int64
	assetID           string
	versionID         uuid.UUID
	snapshotID        uuid.UUID
	operationID       uuid.UUID
	virtualModel      models.JSON
	targetPageIDs     models.JSON
	workerStatus      workerJobStatus
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
type StudioLanguageWorkerGateway interface {
	SubmitEditWithLanguage(context.Context, StudioJobName, string, string, string, edit.EditorLanguageRequest) (workerJobStatus, error)
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
func (g *studioWorkerGateway) SubmitEdit(ctx context.Context, op StudioJobName, source, payload, name string) (workerJobStatus, error) {
	return g.SubmitEditWithLanguage(ctx, op, source, payload, name, edit.EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"eng"}})
}
func (g *studioWorkerGateway) SubmitEditWithLanguage(_ context.Context, op StudioJobName, source, payload, name string, language edit.EditorLanguageRequest) (workerJobStatus, error) {
	var s *edit.WorkerJobSubmission
	var e error
	switch op {
	case StudioJobEditExtract:
		if v2, ok := g.edit.(edit.StudioEditorLanguageService); ok {
			s, e = v2.ExtractLayoutV2WithLanguage(source, "", name, language)
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
	status := workerStatusFrom(r.ID, r.Status, r.Progress, r.Message, r.Error, r.Result)
	status.ErrorCode = r.ErrorCode
	return status, nil
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
	ReconcilePending(context.Context, int) (int, error)
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
	} else if req.Operation == StudioJobEditExtract {
		var parameters EditExtractJobParameters
		_ = decodeStrictParameters(canonical, &parameters)
		language, languageErr := validateStudioEditorLanguage(parameters)
		if languageErr != nil {
			cleanupStudioObject(ctx, sourceKey)
			cleanupStudioObject(ctx, payloadKey)
			return nil, languageErr
		}
		if gateway, ok := c.gateway.(StudioLanguageWorkerGateway); ok {
			submitted, err = gateway.SubmitEditWithLanguage(ctx, req.Operation, sourceKey, payloadKey, current.Document.OriginalFileName, language)
		} else {
			submitted, err = c.gateway.SubmitEdit(ctx, req.Operation, sourceKey, payloadKey, current.Document.OriginalFileName)
		}
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

func validateStudioEditorLanguage(parameters EditExtractJobParameters) (edit.EditorLanguageRequest, error) {
	mode := strings.ToUpper(strings.TrimSpace(parameters.LanguageMode))
	if mode != "AUTO" && mode != "EXPLICIT" {
		return edit.EditorLanguageRequest{}, ErrInvalidJob
	}
	if len(parameters.Languages) == 0 || len(parameters.Languages) > 3 {
		return edit.EditorLanguageRequest{}, ErrInvalidJob
	}
	seen := map[string]bool{}
	languages := make([]string, 0, len(parameters.Languages))
	for _, raw := range parameters.Languages {
		code := strings.ToLower(strings.TrimSpace(raw))
		if code != "eng" && code != "sin" && code != "tam" || seen[code] {
			return edit.EditorLanguageRequest{}, ErrInvalidJob
		}
		seen[code] = true
		languages = append(languages, code)
	}
	return edit.EditorLanguageRequest{Mode: mode, Languages: languages}, nil
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
	return key, storage.SaveObjectBytes(ctx, key, data, "application/json")
}

func markupTargetPageIDs(base *vdm.DocumentModel, raw models.JSON) models.JSON {
	var parameters MarkupJobParameters
	if err := json.Unmarshal(raw, &parameters); err != nil || len(parameters.Boxes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(parameters.Boxes))
	seen := make(map[string]struct{}, len(parameters.Boxes))
	for _, box := range parameters.Boxes {
		if box.Page < 1 || box.Page > len(base.Pages) {
			return nil
		}
		pageID := base.Pages[box.Page-1].PageID
		if pageID == "" {
			return nil
		}
		if _, exists := seen[pageID]; exists {
			continue
		}
		seen[pageID] = struct{}{}
		ids = append(ids, pageID)
	}
	if len(ids) == 0 {
		return nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return nil
	}
	return models.JSON(data)
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

// ReconcilePending advances a bounded batch of durable Studio jobs without
// requiring a browser to remain connected and polling. It deliberately calls
// the same reconcile path as GET; the row lock in the success path makes this
// safe when a client poll and this loop observe the same worker completion.
func (c *studioJobCoordinator) ReconcilePending(ctx context.Context, limit int) (int, error) {
	jobs, err := c.repo.ListReconciliationJobs(ctx, limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	var firstErr error
	for index := range jobs {
		jobCtx, cancel := context.WithTimeout(ctx, studioJobReconciliationTimeout)
		_, reconcileErr := c.reconcile(jobCtx, &jobs[index])
		cancel()
		if reconcileErr != nil {
			if firstErr == nil {
				firstErr = reconcileErr
			}
			continue
		}
		completed++
	}
	return completed, firstErr
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
	if err = c.persistWorkerStatus(ctx, job, status); err != nil {
		return nil, err
	}
	if terminalStudioJob(job.Status) && (job.SourceKey != "" || job.PayloadKey != "") {
		c.cleanupStaging(ctx, job)
	}
	return job, nil
}
func (c *studioJobCoordinator) reconcile(ctx context.Context, job *models.StudioJob) (result *models.StudioJob, reconcileErr error) {
	reconcileStarted := time.Now()
	defer func() {
		c.logReconciliationStage(job, "reconcile_total", time.Since(reconcileStarted), reconciliationOutcome(reconcileErr))
	}()
	if terminalStudioJob(job.Status) {
		return job, nil
	}
	workerStatusStarted := time.Now()
	status, err := c.gateway.Status(ctx, StudioJobName(job.JobType), job.WorkerJobID)
	c.logReconciliationStage(job, "worker_status", time.Since(workerStatusStarted), reconciliationOutcome(err))
	if err != nil {
		return nil, err
	}
	if status.Status == "succeeded" {
		applyWorkerStatus(job, status)
		if err := c.reconcileSuccess(ctx, job); err != nil {
			return nil, err
		}
	} else {
		if err := c.persistWorkerStatus(ctx, job, status); err != nil {
			return nil, err
		}
	}
	if terminalStudioJob(job.Status) && (job.SourceKey != "" || job.PayloadKey != "") {
		c.cleanupStaging(ctx, job)
	}
	return job, nil
}

func (c *studioJobCoordinator) persistWorkerStatus(ctx context.Context, job *models.StudioJob, status workerJobStatus) error {
	return c.repo.WithTransaction(ctx, func(tx Repository, _ *gorm.DB) error {
		locked, err := tx.LockJob(ctx, job.ID)
		if err != nil {
			return err
		}
		// A concurrent success reconciliation is authoritative. Never let a
		// stale status response regress a terminal durable result.
		if locked.ReconciledAt != nil || locked.Status == "failed" || locked.Status == "cancelled" {
			*job = *locked
			return nil
		}
		applyWorkerStatus(locked, status)
		if err := tx.SaveJob(ctx, locked); err != nil {
			return err
		}
		*job = *locked
		return nil
	})
}
func applyWorkerStatus(job *models.StudioJob, s workerJobStatus) {
	job.Status = s.Status
	job.Progress = s.Progress
	job.Message = s.Message
	job.Error = s.Error
	job.ErrorCode = s.ErrorCode
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
		return c.reconcileEditorExtractSuccess(ctx, job)
	}
	prepared, err := c.prepareStudioJobResult(ctx, job)
	if err != nil {
		return err
	}
	registered, err := c.finalizePreparedStudioJobResult(ctx, job, prepared)
	if !registered {
		c.cleanupPreparedStudioObject(ctx, prepared.materializedKey)
	}
	if err != nil {
		return err
	}
	if terminalStudioJob(job.Status) && (job.SourceKey != "" || job.PayloadKey != "") {
		c.cleanupStaging(ctx, job)
	}
	return nil
}

func (c *studioJobCoordinator) reconcileEditorExtractSuccess(ctx context.Context, job *models.StudioJob) error {
	return c.repo.WithTransaction(ctx, func(tx Repository, _ *gorm.DB) error {
		locked, err := tx.LockJob(ctx, job.ID)
		if err != nil {
			return err
		}
		if locked.ReconciledAt != nil || locked.Status == "failed" || locked.Status == "cancelled" {
			*job = *locked
			return nil
		}
		applyWorkerStatus(locked, workerJobStatus{ID: locked.WorkerJobID, Status: "succeeded", Progress: job.Progress, Message: job.Message, Error: job.Error, ErrorCode: job.ErrorCode, Result: mapFromJSON(job.Result)})
		if err := c.reconcileEditorExtractLocked(ctx, tx, locked); err != nil {
			return err
		}
		*job = *locked
		return nil
	})
}

func (c *studioJobCoordinator) prepareStudioJobResult(ctx context.Context, job *models.StudioJob) (*preparedStudioResult, error) {
	if job.WorkerJobID == "" || job.DocumentID == uuid.Nil || job.SessionID == uuid.Nil || job.BaseVersionID == uuid.Nil {
		return nil, fmt.Errorf("%w: incomplete durable job identity", ErrJobReconciliationFailed)
	}
	base, err := c.repo.GetVersion(ctx, job.BaseVersionID)
	if err != nil {
		return nil, err
	}
	if base.DocumentID != job.DocumentID {
		return nil, ErrInvalidBaseVersion
	}
	baseModel, err := vdm.FromJSON(base.VirtualModel)
	if err != nil {
		return nil, err
	}

	downloadStarted := time.Now()
	resp, err := c.gateway.Download(ctx, StudioJobName(job.JobType), job.WorkerJobID)
	c.logReconciliationStage(job, "artifact_download", time.Since(downloadStarted), reconciliationOutcome(err))
	if err != nil {
		return nil, fmt.Errorf("%w: download worker artifact: %w", ErrJobReconciliationFailed, err)
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp("", "pdfnest-studio-job-result-*.pdf")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	defer tmp.Close()
	if _, err = io.Copy(tmp, resp.Body); err != nil {
		return nil, err
	}
	if err = tmp.Close(); err != nil {
		return nil, err
	}

	validateStarted := time.Now()
	pages, err := validateMaterializedOutput(path)
	if err != nil {
		c.logReconciliationStage(job, "artifact_validate", time.Since(validateStarted), reconciliationOutcome(err))
		return nil, err
	}
	modelState, err := deriveMaterializedVDMForJob(baseModel, path, pages, StudioJobName(job.JobType))
	if err != nil {
		c.logReconciliationStage(job, "artifact_validate", time.Since(validateStarted), reconciliationOutcome(err))
		return nil, err
	}
	assetID := "studio-job-" + uuid.NewString()
	versionID := uuid.New()
	for index := range modelState.Pages {
		modelState.Pages[index].SourceAssetID = &assetID
		modelState.Pages[index].SourcePageNumber = index + 1
	}
	modelState.VersionID = versionID.String()
	vdmBytes, err := modelState.ToJSON()
	if err != nil {
		c.logReconciliationStage(job, "artifact_validate", time.Since(validateStarted), reconciliationOutcome(err))
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		c.logReconciliationStage(job, "artifact_validate", time.Since(validateStarted), reconciliationOutcome(err))
		return nil, err
	}
	c.logReconciliationStage(job, "artifact_validate", time.Since(validateStarted), "ok")

	targetPageIDs := models.JSON(nil)
	if strings.HasPrefix(job.JobType, "markup_") {
		targetPageIDs = markupTargetPageIDs(baseModel, job.Parameters)
	}
	prepared := &preparedStudioResult{
		jobID: job.ID, sessionID: job.SessionID, documentID: job.DocumentID, baseVersionID: job.BaseVersionID,
		workerJobID: job.WorkerJobID, jobType: job.JobType, baseVersionNumber: base.VersionNumber,
		pageCount: pages, byteSize: info.Size(), assetID: assetID, versionID: versionID,
		snapshotID: uuid.New(), operationID: uuid.New(), virtualModel: models.JSON(vdmBytes),
		targetPageIDs: targetPageIDs,
		workerStatus:  workerJobStatus{ID: job.WorkerJobID, Status: "succeeded", Progress: job.Progress, Message: job.Message, Error: job.Error, ErrorCode: job.ErrorCode, Result: mapFromJSON(job.Result)},
	}
	storeStarted := time.Now()
	prepared.materializedKey = storage.BuildKey(filepath.ToSlash(filepath.Join("studio", "materialized", job.DocumentID.String())), ".pdf")
	storeErr := persistStudioPDF(ctx, path, prepared.materializedKey)
	c.logReconciliationStage(job, "artifact_store", time.Since(storeStarted), reconciliationOutcome(storeErr))
	if storeErr != nil {
		c.cleanupPreparedStudioObject(ctx, prepared.materializedKey)
		return nil, storeErr
	}
	return prepared, nil
}

func (c *studioJobCoordinator) finalizePreparedStudioJobResult(ctx context.Context, job *models.StudioJob, prepared *preparedStudioResult) (registered bool, finalizeErr error) {
	txStarted := time.Now()
	defer func() {
		c.logReconciliationStage(job, "finalize_tx", time.Since(txStarted), reconciliationOutcome(finalizeErr))
	}()

	finalizeErr = c.repo.WithTransaction(ctx, func(tx Repository, _ *gorm.DB) error {
		lockStarted := time.Now()
		locked, err := tx.LockJob(ctx, job.ID)
		c.logReconciliationStage(job, "finalize_lock_wait", time.Since(lockStarted), reconciliationOutcome(err))
		if err != nil {
			return err
		}
		if locked.ReconciledAt != nil || locked.Status == "failed" || locked.Status == "cancelled" {
			*job = *locked
			return nil
		}
		if locked.ID != prepared.jobID || locked.SessionID != prepared.sessionID || locked.DocumentID != prepared.documentID ||
			locked.BaseVersionID != prepared.baseVersionID || locked.WorkerJobID != prepared.workerJobID || locked.JobType != prepared.jobType {
			return fmt.Errorf("%w: prepared result no longer matches durable job", ErrJobReconciliationFailed)
		}
		base, err := tx.GetVersion(ctx, locked.BaseVersionID)
		if err != nil {
			return err
		}
		if base.DocumentID != locked.DocumentID || base.VersionNumber != prepared.baseVersionNumber {
			return ErrInvalidBaseVersion
		}
		lockedSession, err := tx.LockSession(ctx, locked.SessionID)
		if err != nil {
			return err
		}
		if lockedSession.DocumentID != locked.DocumentID {
			return ErrUnauthorized
		}

		applyWorkerStatus(locked, prepared.workerStatus)
		now := time.Now().UTC()
		snapshot := &models.StudioSnapshot{ID: prepared.snapshotID, VersionID: prepared.versionID, AssetID: prepared.assetID, PageCount: prepared.pageCount, CreatedAt: now}
		asset := &models.StudioAsset{ID: prepared.assetID, DocumentID: locked.DocumentID, AssetType: "job_result", R2Key: prepared.materializedKey, ByteSize: prepared.byteSize, MimeType: "application/pdf"}
		version := &models.StudioVersion{ID: prepared.versionID, DocumentID: locked.DocumentID, ParentVersionID: &locked.BaseVersionID, VersionNumber: base.VersionNumber + 1, Status: "ready", OperationType: locked.JobType, VirtualModel: prepared.virtualModel, SnapshotID: &snapshot.ID, IsMaterialized: true, CreatedAt: now}
		operation := &models.StudioOperation{ID: prepared.operationID, DocumentID: locked.DocumentID, VersionID: prepared.versionID, IdempotencyKey: locked.IdempotencyKey, OperationName: locked.JobType, Parameters: locked.Parameters, TargetPageIDs: prepared.targetPageIDs, CreatedAt: now}
		if err = tx.CreateAsset(ctx, asset); err != nil {
			return err
		}
		if err = tx.CreateSnapshot(ctx, snapshot); err != nil {
			return err
		}
		if lockedSession.ActiveVersionID == locked.BaseVersionID {
			if err = tx.CreateVersionAndOperation(ctx, version, operation, locked.SessionID, &locked.BaseVersionID); err != nil {
				return err
			}
		} else if err = tx.CreateDetachedVersionAndOperation(ctx, version, operation); err != nil {
			return err
		}
		locked.ResultVersionID = &prepared.versionID
		locked.ReconciledAt = &now
		if err = tx.SaveJob(ctx, locked); err != nil {
			return err
		}
		*job = *locked
		registered = true
		return nil
	})
	if finalizeErr != nil {
		registered = false
	}
	return registered, finalizeErr
}

func (c *studioJobCoordinator) cleanupPreparedStudioObject(ctx context.Context, key string) {
	if key == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	cleanupStudioObject(cleanupCtx, key)
}

func (c *studioJobCoordinator) logReconciliationStage(job *models.StudioJob, stage string, elapsed time.Duration, outcome string) {
	if job == nil {
		return
	}
	log.Printf("[STUDIO JOB RECONCILIATION] job_id=%s session_id=%s stage=%s duration_ms=%d outcome=%s", job.ID, job.SessionID, stage, elapsed.Milliseconds(), outcome)
}

func reconciliationOutcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

func (c *studioJobCoordinator) reconcileEditorExtractLocked(ctx context.Context, tx Repository, job *models.StudioJob) error {
	if state, err := tx.GetEditorStateByExtractJob(ctx, job.ID); err != nil {
		return err
	} else if state != nil {
		job.EditorStateID = &state.ID
		now := time.Now().UTC()
		job.ReconciledAt = &now
		return tx.SaveJob(ctx, job)
	}
	layout, canonical, err := decodeEditorLayout(job.Result)
	if err != nil {
		job.Status = "failed"
		job.Error = "worker returned invalid editor layout"
		return tx.SaveJob(ctx, job)
	}
	_ = layout
	now := time.Now().UTC()
	state := &models.StudioEditorState{ID: uuid.New(), DocumentID: job.DocumentID, SessionID: job.SessionID, BaseVersionID: job.BaseVersionID, ExtractJobID: job.ID, Layout: models.JSON(canonical), CreatedAt: now}
	if err = tx.CreateEditorState(ctx, state); err != nil {
		return err
	}
	job.EditorStateID = &state.ID
	job.ReconciledAt = &now
	return tx.SaveJob(ctx, job)
}
func (c *studioJobCoordinator) cleanupStaging(ctx context.Context, job *models.StudioJob) {
	cleanupStudioObject(ctx, job.SourceKey)
	cleanupStudioObject(ctx, job.PayloadKey)
	job.SourceKey = ""
	job.PayloadKey = ""
	_ = c.repo.SaveJob(ctx, job)
}

func mapFromJSON(raw models.JSON) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return result
}

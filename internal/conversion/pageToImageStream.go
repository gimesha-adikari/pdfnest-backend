package conversion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"pdfnest-backend/internal/worker"
)

type previewWorkerSession struct {
	ID           string
	OwnerID      string
	SourceHash   string
	PageCount    int
	CreatedAt    time.Time
	LastAccessed time.Time
}

var (
	ErrPreviewSessionNotFound  = errors.New("preview session not found")
	ErrPreviewSessionForbidden = errors.New("preview session belongs to another identity")
)

type previewSessionCache struct {
	mu       sync.RWMutex
	sessions map[string]*previewWorkerSession
	byHash   map[string]string
}

var globalPreviewSessions = &previewSessionCache{
	sessions: make(map[string]*previewWorkerSession),
	byHash:   make(map[string]string),
}

func previewSessionHashKey(ownerID, sourceHash string) string {
	return ownerID + "\x00" + sourceHash
}

func (s *previewSessionCache) getByIDForOwner(id, ownerID string) (*previewWorkerSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrPreviewSessionNotFound
	}
	if session.OwnerID != ownerID {
		return nil, ErrPreviewSessionForbidden
	}
	return session, nil
}

func (s *previewSessionCache) getByHash(ownerID, hash string) (*previewWorkerSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessionID, ok := s.byHash[previewSessionHashKey(ownerID, hash)]
	if !ok {
		return nil, false
	}

	session, ok := s.sessions[sessionID]
	return session, ok
}

func (s *previewSessionCache) put(session *previewWorkerSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[session.ID] = session
	s.byHash[previewSessionHashKey(session.OwnerID, session.SourceHash)] = session.ID
}

func (s *previewSessionCache) touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if session, ok := s.sessions[id]; ok {
		session.LastAccessed = time.Now()
	}
}

func (s *previewSessionCache) deleteByID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return
	}

	delete(s.sessions, id)

	cacheKey := previewSessionHashKey(session.OwnerID, session.SourceHash)
	if mappedID, ok := s.byHash[cacheKey]; ok && mappedID == id {
		delete(s.byHash, cacheKey)
	}
}

func (s *previewSessionCache) deleteByHash(ownerID, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cacheKey := previewSessionHashKey(ownerID, hash)
	sessionID, ok := s.byHash[cacheKey]
	if !ok {
		return
	}

	delete(s.byHash, cacheKey)
	delete(s.sessions, sessionID)
}

func (s *ConversionService) ConvertPageToImageStream(
	ctx context.Context,
	fileHeader *multipart.FileHeader,
	pageNum int,
	scale float64,
	ownerID string,
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if fileHeader == nil {
		return nil, fmt.Errorf("preview file is required")
	}

	if pageNum < 1 {
		return nil, fmt.Errorf("page number must be greater than or equal to 1")
	}

	if scale <= 0 {
		return nil, fmt.Errorf("preview scale must be greater than 0")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("preview identity is required")
	}

	_, targetPdfPath, cleanup, err := s.preparePreviewPdf(
		ctx,
		fileHeader,
	)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	sourceHash, err := hashFile(targetPdfPath)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to fingerprint preview PDF: %w",
			err,
		)
	}

	workerBaseURL := os.Getenv("PDFNEST_WORKER_URL")
	if workerBaseURL == "" {
		workerBaseURL = "http://localhost:8000"
	}

	session, ok := globalPreviewSessions.getByHash(ownerID, sourceHash)

	if !ok {
		workerSession, createErr := s.createWorkerPreviewSession(
			ctx,
			workerBaseURL,
			targetPdfPath,
		)
		if createErr != nil {
			return nil, createErr
		}

		session = &previewWorkerSession{
			ID:           workerSession.ID,
			OwnerID:      ownerID,
			SourceHash:   sourceHash,
			PageCount:    workerSession.PageCount,
			CreatedAt:    time.Now(),
			LastAccessed: time.Now(),
		}

		globalPreviewSessions.put(session)
	}

	globalPreviewSessions.touch(session.ID)

	dpi := 72.0 * scale

	imageBytes, err := s.renderWorkerSessionPage(
		ctx,
		workerBaseURL,
		session.ID,
		pageNum,
		dpi,
	)
	if err == nil {
		globalPreviewSessions.touch(session.ID)
		return imageBytes, nil
	}

	globalPreviewSessions.deleteByHash(ownerID, sourceHash)

	workerSession, recreateErr := s.createWorkerPreviewSession(
		ctx,
		workerBaseURL,
		targetPdfPath,
	)
	if recreateErr != nil {
		return nil, recreateErr
	}

	newSession := &previewWorkerSession{
		ID:           workerSession.ID,
		OwnerID:      ownerID,
		SourceHash:   sourceHash,
		PageCount:    workerSession.PageCount,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
	}

	globalPreviewSessions.put(newSession)

	return s.renderWorkerSessionPage(
		ctx,
		workerBaseURL,
		workerSession.ID,
		pageNum,
		dpi,
	)
}

func (s *ConversionService) preparePreviewPdf(
	ctx context.Context,
	fileHeader *multipart.FileHeader,
) (
	string,
	string,
	func(),
	error,
) {
	src, err := fileHeader.Open()
	if err != nil {
		return "", "", nil, fmt.Errorf(
			"failed to read uploaded file payload stream: %w",
			err,
		)
	}

	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext == "" {
		ext = ".pdf"
	}

	tempFileName := fmt.Sprintf(
		"pdfnest-preview-%s%s",
		uuid.New().String(),
		ext,
	)

	tempFilePath := filepath.Join(
		os.TempDir(),
		tempFileName,
	)

	dst, err := os.Create(tempFilePath)
	if err != nil {
		_ = src.Close()

		return "", "", nil, fmt.Errorf(
			"failed to allocate temporary preview storage: %w",
			err,
		)
	}

	_, copyErr := io.Copy(dst, src)

	closeDstErr := dst.Close()
	closeSrcErr := src.Close()

	if copyErr != nil {
		_ = os.Remove(tempFilePath)

		return "", "", nil, fmt.Errorf(
			"failed to persist preview upload: %w",
			copyErr,
		)
	}

	if closeDstErr != nil {
		_ = os.Remove(tempFilePath)

		return "", "", nil, fmt.Errorf(
			"failed to close temporary preview file: %w",
			closeDstErr,
		)
	}

	if closeSrcErr != nil {
		_ = os.Remove(tempFilePath)

		return "", "", nil, fmt.Errorf(
			"failed to close uploaded preview file: %w",
			closeSrcErr,
		)
	}

	targetPdfPath := tempFilePath

	if ext != ".pdf" {
		compiledPdfPath, err := s.OfficeToPdf(
			ctx,
			tempFilePath,
		)
		if err != nil {
			_ = os.Remove(tempFilePath)

			return "", "", nil, fmt.Errorf(
				"failed to compile office document for preview: %w",
				err,
			)
		}

		targetPdfPath = compiledPdfPath

		cleanup := func() {
			_ = os.Remove(tempFilePath)
			_ = os.Remove(compiledPdfPath)
		}

		return tempFilePath, targetPdfPath, cleanup, nil
	}

	cleanup := func() {
		_ = os.Remove(tempFilePath)
	}

	return tempFilePath, targetPdfPath, cleanup, nil
}

func (s *ConversionService) createWorkerPreviewSession(
	ctx context.Context,
	workerBaseURL string,
	pdfPath string,
) (previewRenderSessionInfo, error) {
	var empty previewRenderSessionInfo
	pdfFile, err := os.Open(pdfPath)
	if err != nil {
		return empty, fmt.Errorf(
			"failed to open preview PDF: %w",
			err,
		)
	}

	defer pdfFile.Close()

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		defer pdfFile.Close()

		part, err := writer.CreateFormFile(
			"file",
			filepath.Base(pdfPath),
		)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(part, pdfFile); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		_ = pw.Close()
	}()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(
			workerBaseURL,
			"/",
		)+"/api/v1/render/sessions",
		pr,
	)
	if err != nil {
		return empty, fmt.Errorf(
			"failed to build render session request: %w",
			err,
		)
	}

	req.Header.Set(
		"Content-Type",
		contentType,
	)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return empty, fmt.Errorf(
			"render session creation failed: %w",
			err,
		)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return empty, fmt.Errorf(
			"render session creation failed: status=%s body=%s",
			resp.Status,
			strings.TrimSpace(string(body)),
		)
	}

	var response struct {
		SessionID string `json:"session_id"`
		PageCount int    `json:"page_count"`
		FileSize  int64  `json:"file_size"`
	}

	if err := json.NewDecoder(
		resp.Body,
	).Decode(&response); err != nil {
		return empty, fmt.Errorf(
			"failed to decode render session response: %w",
			err,
		)
	}

	if response.SessionID == "" {
		return empty, fmt.Errorf(
			"render worker returned an empty session ID",
		)
	}

	return previewRenderSessionInfo{
		ID:        response.SessionID,
		PageCount: response.PageCount,
	}, nil
}

type previewRenderSessionInfo struct {
	ID        string
	PageCount int
}

func (s *ConversionService) renderWorkerSessionPage(
	ctx context.Context,
	workerBaseURL string,
	sessionID string,
	pageNum int,
	dpi float64,
) ([]byte, error) {
	url := fmt.Sprintf(
		"%s/api/v1/render/sessions/%s/page/%d?dpi=%s",
		strings.TrimRight(workerBaseURL, "/"),
		sessionID,
		pageNum,
		strconv.FormatFloat(dpi, 'f', -1, 64),
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		url,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to build render page request: %w",
			err,
		)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf(
			"page render failed: %w",
			err,
		)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf(
			"page render failed: status=%s body=%s",
			resp.Status,
			strings.TrimSpace(string(body)),
		)
	}

	imageBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to read rendered image bytes: %w",
			err,
		)
	}

	if _, err := jpeg.Decode(bytes.NewReader(imageBytes)); err != nil {
		return nil, fmt.Errorf(
			"rendered image is not a valid jpeg: %w",
			err,
		)
	}

	return imageBytes, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer file.Close()

	hasher := sha256.New()

	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(
		hasher.Sum(nil),
	), nil
}

func (s *ConversionService) CreatePreviewSession(
	ctx context.Context,
	fileHeader *multipart.FileHeader,
	ownerID string,
) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if fileHeader == nil {
		return nil, fmt.Errorf("preview file is required")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("preview identity is required")
	}

	_, targetPdfPath, cleanup, err := s.preparePreviewPdf(
		ctx,
		fileHeader,
	)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	sourceHash, err := hashFile(targetPdfPath)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to fingerprint preview PDF: %w",
			err,
		)
	}

	workerBaseURL := os.Getenv("PDFNEST_WORKER_URL")
	if workerBaseURL == "" {
		workerBaseURL = "http://localhost:8000"
	}

	if existing, ok := globalPreviewSessions.getByHash(ownerID, sourceHash); ok {
		globalPreviewSessions.touch(existing.ID)

		return map[string]any{
			"session_id": existing.ID,
			"page_count": existing.PageCount,
		}, nil
	}

	workerSession, err := s.createWorkerPreviewSession(
		ctx,
		workerBaseURL,
		targetPdfPath,
	)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	globalPreviewSessions.put(
		&previewWorkerSession{
			ID:           workerSession.ID,
			OwnerID:      ownerID,
			SourceHash:   sourceHash,
			PageCount:    workerSession.PageCount,
			CreatedAt:    now,
			LastAccessed: now,
		},
	)

	return map[string]any{
		"session_id": workerSession.ID,
		"page_count": workerSession.PageCount,
	}, nil
}

func (s *ConversionService) GetPreviewSessionPage(
	ctx context.Context,
	sessionID string,
	pageNum int,
	scale float64,
	ownerID string,
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if sessionID == "" {
		return nil, fmt.Errorf("preview session ID is required")
	}

	if pageNum < 1 {
		return nil, fmt.Errorf(
			"page number must be greater than or equal to 1",
		)
	}

	if scale <= 0 {
		return nil, fmt.Errorf(
			"preview scale must be greater than 0",
		)
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("preview identity is required")
	}

	session, err := globalPreviewSessions.getByIDForOwner(sessionID, ownerID)
	if err != nil {
		return nil, err
	}

	workerBaseURL := os.Getenv("PDFNEST_WORKER_URL")
	if workerBaseURL == "" {
		workerBaseURL = "http://localhost:8000"
	}

	globalPreviewSessions.touch(sessionID)

	dpi := 72.0 * scale

	imageBytes, err := s.renderWorkerSessionPage(
		ctx,
		workerBaseURL,
		session.ID,
		pageNum,
		dpi,
	)
	if err != nil {
		globalPreviewSessions.deleteByID(sessionID)

		return nil, err
	}

	globalPreviewSessions.touch(sessionID)

	return imageBytes, nil
}

func (s *ConversionService) DeletePreviewSession(
	ctx context.Context,
	sessionID string,
	ownerID string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if sessionID == "" {
		return fmt.Errorf("preview session ID is required")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("preview identity is required")
	}

	session, err := globalPreviewSessions.getByIDForOwner(sessionID, ownerID)
	if errors.Is(err, ErrPreviewSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	workerBaseURL := os.Getenv("PDFNEST_WORKER_URL")
	if workerBaseURL == "" {
		workerBaseURL = "http://localhost:8000"
	}

	url := fmt.Sprintf(
		"%s/api/v1/render/sessions/%s",
		strings.TrimRight(workerBaseURL, "/"),
		session.ID,
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodDelete,
		url,
		nil,
	)
	if err != nil {
		return fmt.Errorf(
			"failed to build preview session delete request: %w",
			err,
		)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		return fmt.Errorf(
			"failed to delete worker preview session: %w",
			err,
		)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusNoContent &&
		resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)

		return fmt.Errorf(
			"worker preview session deletion failed: status=%s body=%s",
			resp.Status,
			strings.TrimSpace(string(body)),
		)
	}

	globalPreviewSessions.deleteByID(sessionID)

	return nil
}

package ast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/parsers"
	"pdfnest-backend/internal/worker"
)

const (
	DefaultPythonWorkerURL     = "http://localhost:8000"
	DefaultPythonTimeout       = 5 * time.Second
	MaxPythonCandidateFiles    = 20
	MaxPythonFileSizeBytes     = 512 * 1024       // 500 KB
	MaxPythonTotalPayloadBytes = 10 * 1024 * 1024 // 10 MB
)

// PythonFilePayload holds a single candidate Python source file for static AST extraction.
type PythonFilePayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// PythonASTRequest defines the contract sent to pdfnest-worker for static AST analysis.
type PythonASTRequest struct {
	ProtocolVersion string              `json:"protocolVersion"`
	TaskID          string              `json:"taskId"`
	SessionID       string              `json:"sessionId"`
	Files           []PythonFilePayload `json:"files"`
	Extractors      []string            `json:"extractors"`
}

// ErrorDetail captures structured failures from the Python AST worker.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PythonASTResponse represents the structured deterministic facts returned by pdfnest-worker.
type PythonASTResponse struct {
	ProtocolVersion string                `json:"protocolVersion"`
	TaskID          string                `json:"taskId"`
	Status          string                `json:"status"` // "SUCCESS" | "ERROR"
	DurationMs      int64                 `json:"durationMs"`
	NodesProcessed  int64                 `json:"nodesProcessed"`
	Routes          []engine.ApiRouteItem `json:"routes"`
	Models          []ModelItem           `json:"models"`
	EnvReferences   []EnvironmentUsage    `json:"envReferences"`
	Evidence        []engine.EvidenceItem `json:"evidence"`
	Diagnostics     []DiagnosticItem      `json:"diagnostics"`
	Error           *ErrorDetail          `json:"error,omitempty"`
}

// PythonClient handles internal HTTP transport to the Python worker AST endpoint.
type PythonClient struct {
	httpClient   *http.Client
	baseURL      string
	sharedSecret string
}

// NewPythonClient creates a Python AST client with configured endpoint URL and authentication.
func NewPythonClient(baseURL string, secret string, timeout time.Duration) *PythonClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = os.Getenv("PDFNEST_ANALYZER_PYTHON_URL")
		if strings.TrimSpace(baseURL) == "" {
			baseURL = os.Getenv("WORKER_URL")
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = DefaultPythonWorkerURL
		}
	}
	baseURL = strings.TrimRight(baseURL, "/")

	if strings.TrimSpace(secret) == "" {
		secret = worker.GetWorkerSharedSecret()
	}

	if timeout <= 0 {
		timeout = DefaultPythonTimeout
	}

	return &PythonClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		baseURL:      baseURL,
		sharedSecret: secret,
	}
}

// SelectPythonCandidates filters and reads candidate Python files with strict size limits and sandbox bounds.
func SelectPythonCandidates(
	sandboxRoot string,
	targetFiles []string,
	maxFiles int,
	maxFileSize int64,
) ([]PythonFilePayload, []DiagnosticItem) {
	if maxFiles <= 0 {
		maxFiles = MaxPythonCandidateFiles
	}
	if maxFileSize <= 0 {
		maxFileSize = MaxPythonFileSizeBytes
	}

	cleanRoot := filepath.Clean(sandboxRoot)
	var candidates []string
	var diagnostics []DiagnosticItem

	// 1. Filter for .py files and sort deterministically
	for _, f := range targetFiles {
		cleanRel := filepath.Clean(filepath.ToSlash(f))
		if strings.HasSuffix(strings.ToLower(cleanRel), ".py") {
			candidates = append(candidates, cleanRel)
		}
	}
	sort.Strings(candidates)

	if len(candidates) > maxFiles {
		diagnostics = append(diagnostics, DiagnosticItem{
			Code:     "CANDIDATE_LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("bounded Python candidate files to %d", maxFiles),
			Severity: "info",
		})
		candidates = candidates[:maxFiles]
	}

	var payloads []PythonFilePayload
	for _, relPath := range candidates {
		if strings.HasPrefix(relPath, "../") || strings.HasPrefix(relPath, "/") {
			diagnostics = append(diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "SANDBOX_ESCAPE_REJECTED",
				Message:    "path traversal rejected",
				Severity:   "warning",
			})
			continue
		}

		absPath := filepath.Join(cleanRoot, filepath.FromSlash(relPath))
		if !strings.HasPrefix(absPath, cleanRoot) {
			diagnostics = append(diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "SANDBOX_ESCAPE_REJECTED",
				Message:    "resolved path escapes sandbox root",
				Severity:   "warning",
			})
			continue
		}

		fileInfo, statErr := os.Stat(absPath)
		if statErr != nil || fileInfo.IsDir() {
			continue
		}

		if fileInfo.Size() > maxFileSize {
			diagnostics = append(diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "FILE_SIZE_LIMIT_EXCEEDED",
				Message:    fmt.Sprintf("file size %d exceeds %d bytes limit", fileInfo.Size(), maxFileSize),
				Severity:   "info",
			})
			continue
		}

		f, openErr := os.Open(absPath)
		if openErr != nil {
			diagnostics = append(diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "FILE_OPEN_ERROR",
				Message:    openErr.Error(),
				Severity:   "warning",
			})
			continue
		}

		// Bounded read using LimitReader
		contentBytes, readErr := io.ReadAll(io.LimitReader(f, maxFileSize+1))
		f.Close()

		if readErr != nil {
			diagnostics = append(diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "FILE_READ_ERROR",
				Message:    readErr.Error(),
				Severity:   "warning",
			})
			continue
		}

		if int64(len(contentBytes)) > maxFileSize {
			diagnostics = append(diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "FILE_SIZE_LIMIT_EXCEEDED",
				Message:    "file size exceeds limit during bounded read",
				Severity:   "info",
			})
			continue
		}

		payloads = append(payloads, PythonFilePayload{
			Path:    relPath,
			Content: string(contentBytes),
		})
	}

	return payloads, diagnostics
}

// AnalyzePython dispatches a static AST analysis request to the Python worker service.
func (c *PythonClient) AnalyzePython(ctx context.Context, req PythonASTRequest) (*PythonASTResponse, error) {
	if req.ProtocolVersion == "" {
		req.ProtocolVersion = "1.0.0"
	}
	if len(req.Extractors) == 0 {
		req.Extractors = []string{"routes", "models", "env_references", "imports"}
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if len(reqBody) > MaxPythonTotalPayloadBytes {
		return nil, fmt.Errorf("payload size %d exceeds limit of %d bytes", len(reqBody), MaxPythonTotalPayloadBytes)
	}

	endpoint := c.baseURL + "/api/v1/analyzer/python-ast"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.sharedSecret != "" {
		_ = worker.SignRequestWithSecret(httpReq, c.sharedSecret)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("python worker request failed: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response with bounded limit
	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxPythonTotalPayloadBytes))
	if err != nil {
		return nil, fmt.Errorf("read python response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp PythonASTResponse
		if jsonErr := json.Unmarshal(respBytes, &errResp); jsonErr == nil && errResp.Error != nil {
			return &errResp, fmt.Errorf("python worker error (%s): %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("python worker returned HTTP %d: %s", httpResp.StatusCode, string(respBytes))
	}

	var pyResp PythonASTResponse
	if err := json.Unmarshal(respBytes, &pyResp); err != nil {
		return nil, fmt.Errorf("unmarshal python response: %w", err)
	}

	if pyResp.ProtocolVersion != "1.0.0" {
		return nil, fmt.Errorf("unsupported python protocol version '%s'", pyResp.ProtocolVersion)
	}

	return &pyResp, nil
}

// EnrichWithPythonAST additively merges Python AST findings into Phase 3 AnalysisFacts.
func EnrichWithPythonAST(facts *parsers.AnalysisFacts, pyRes *PythonASTResponse) {
	if facts == nil || pyRes == nil || pyRes.Status != "SUCCESS" {
		return
	}

	// 1. Merge Routes
	routeMap := make(map[string]*engine.ApiRouteItem)
	for i := range facts.Routes {
		r := &facts.Routes[i]
		key := strings.ToUpper(r.Method) + ":" + r.Path
		routeMap[key] = r
	}

	for _, pyRoute := range pyRes.Routes {
		key := strings.ToUpper(pyRoute.Method) + ":" + pyRoute.Path
		if existing, ok := routeMap[key]; ok {
			if pyRoute.InferredHandler != nil && *pyRoute.InferredHandler != "" {
				existing.InferredHandler = pyRoute.InferredHandler
			}
			if pyRoute.LineNumber != nil && *pyRoute.LineNumber > 0 {
				existing.LineNumber = pyRoute.LineNumber
			}
			if pyRoute.AuthRequired {
				existing.AuthRequired = true
			}
		} else {
			facts.Routes = append(facts.Routes, pyRoute)
			routeMap[key] = &pyRoute
		}
	}

	sort.Slice(facts.Routes, func(i, j int) bool {
		if facts.Routes[i].Method != facts.Routes[j].Method {
			return facts.Routes[i].Method < facts.Routes[j].Method
		}
		if facts.Routes[i].Path != facts.Routes[j].Path {
			return facts.Routes[i].Path < facts.Routes[j].Path
		}
		return facts.Routes[i].SourceFile < facts.Routes[j].SourceFile
	})

	// 2. Merge Environment References
	envRefMap := make(map[string][]string)
	for _, ref := range pyRes.EnvReferences {
		upperName := strings.ToUpper(ref.Name)
		envRefMap[upperName] = append(envRefMap[upperName], ref.SourceFile)
	}

	for i := range facts.Environment {
		envVar := &facts.Environment[i]
		upperName := strings.ToUpper(envVar.Name)
		if refs, ok := envRefMap[upperName]; ok {
			seen := make(map[string]bool)
			for _, r := range envVar.References {
				seen[r] = true
			}
			for _, r := range refs {
				if !seen[r] {
					seen[r] = true
					envVar.References = append(envVar.References, r)
				}
			}
			sort.Strings(envVar.References)
		}
	}

	// 3. Merge Evidence
	if len(pyRes.Evidence) > 0 && len(facts.Technologies) > 0 {
		for i := range facts.Technologies {
			tech := &facts.Technologies[i]
			for _, ev := range pyRes.Evidence {
				if techMatchesEvidence(tech.Name, ev.Detail) {
					tech.Evidence = append(tech.Evidence, ev)
				}
			}
			sort.Slice(tech.Evidence, func(x, y int) bool {
				if tech.Evidence[x].FilePath != tech.Evidence[y].FilePath {
					return tech.Evidence[x].FilePath < tech.Evidence[y].FilePath
				}
				return tech.Evidence[x].Detail < tech.Evidence[y].Detail
			})
		}
	}
}

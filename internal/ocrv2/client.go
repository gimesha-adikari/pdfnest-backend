package ocrv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"pdfnest-backend/internal/worker"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	HTTP    HTTPDoer
	BaseURL string
	Signer  func(*http.Request) error
}

func NewWorkerClient() *Client {
	return &Client{HTTP: worker.Client, BaseURL: worker.GetWorkerURL()}
}

func NewClientForTest(httpClient HTTPDoer, baseURL string, signer func(*http.Request) error) *Client {
	return &Client{HTTP: httpClient, BaseURL: strings.TrimRight(baseURL, "/"), Signer: signer}
}

func (c *Client) Execute(ctx context.Context, inputPath string, request TextRequest) (*TextResponse, error) {
	if c == nil || c.HTTP == nil {
		return nil, &WorkerError{Code: ErrWorkerAuthentication, Message: "worker client is not configured"}
	}
	fields := map[string]string{
		"request_id":     request.RequestID,
		"profile":        request.Profile,
		"language":       request.Language,
		"routing_policy": string(request.RoutingPolicy),
	}
	if request.LanguageMode != "" {
		fields["language_mode"] = request.LanguageMode
	}
	if request.PageIndex != nil {
		fields["page_index"] = strconv.Itoa(*request.PageIndex)
	}
	body, contentType, err := worker.CreateMultipartRequest(inputPath, func(writer *multipart.Writer) error {
		for key, value := range fields {
			if err := writer.WriteField(key, value); err != nil {
				return err
			}
		}
		for _, language := range request.Languages {
			if err := writer.WriteField("languages", language); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/ocr/v2/text", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Request-ID", request.RequestID)
	if c.Signer != nil {
		if err := c.Signer(req); err != nil {
			return nil, err
		}
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	var decoded TextResponse
	if json.Unmarshal(data, &decoded) != nil {
		var envelope struct {
			Detail Error `json:"detail"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Detail.Code != "" {
			return nil, &WorkerError{Code: envelope.Detail.Code, HTTPStatus: response.StatusCode, Message: envelope.Detail.Message}
		}
		if response.StatusCode == http.StatusUnauthorized {
			return nil, &WorkerError{Code: ErrWorkerAuthentication, HTTPStatus: response.StatusCode, Message: "worker authentication failed"}
		}
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: response.StatusCode, Message: "worker returned malformed OCR V2 response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &decoded, &WorkerError{Code: errorCodeFromResponse(decoded.Error, response.StatusCode), HTTPStatus: response.StatusCode, Message: safeWorkerMessage(decoded.Error), Response: &decoded}
	}
	if decoded.Status == "FAILED" {
		return &decoded, &WorkerError{Code: errorCodeFromResponse(decoded.Error, response.StatusCode), HTTPStatus: response.StatusCode, Message: safeWorkerMessage(decoded.Error), Response: &decoded}
	}
	return &decoded, nil
}

func (c *Client) Preview(ctx context.Context, inputPath string, request TextRequest) (*MarkupPreviewResponse, error) {
	if c == nil || c.HTTP == nil {
		return nil, &WorkerError{Code: ErrWorkerAuthentication, Message: "worker client is not configured"}
	}
	fields := map[string]string{
		"request_id":     request.RequestID,
		"profile":        request.Profile,
		"language":       request.Language,
		"routing_policy": string(request.RoutingPolicy),
	}
	if request.LanguageMode != "" {
		fields["language_mode"] = request.LanguageMode
	}
	if request.PageIndex != nil {
		fields["page_index"] = strconv.Itoa(*request.PageIndex)
	}
	body, contentType, err := worker.CreateMultipartRequest(inputPath, func(writer *multipart.Writer) error {
		for key, value := range fields {
			if err := writer.WriteField(key, value); err != nil {
				return err
			}
		}
		for _, language := range request.Languages {
			if err := writer.WriteField("languages", language); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/ocr/v2/markup/preview", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Request-ID", request.RequestID)
	if c.Signer != nil {
		if err := c.Signer(req); err != nil {
			return nil, err
		}
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeWorkerJSONError(data, response.StatusCode)
	}
	var decoded MarkupPreviewResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: response.StatusCode, Message: "worker returned malformed OCR markup preview"}
	}
	return &decoded, nil
}

func (c *Client) GetCapabilities(ctx context.Context) (*Capabilities, error) {
	data, status, err := c.doJSON(ctx, http.MethodGet, "/internal/ocr/v2/capabilities", "", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeWorkerJSONError(data, status)
	}
	var capabilities Capabilities
	if err := json.Unmarshal(data, &capabilities); err != nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: status, Message: "worker returned malformed OCR V2 capabilities"}
	}
	return &capabilities, nil
}

func (c *Client) SubmitJob(ctx context.Context, request JobSubmitRequest) (*JobStatus, error) {
	payload := map[string]any{
		"request_id":     request.RequestID,
		"profile":        request.Profile,
		"language":       request.Language,
		"routing_policy": string(request.RoutingPolicy),
		"source_key":     request.SourceKey,
		"source_name":    request.SourceName,
		"owner_identity": request.OwnerIdentity,
		"total_pages":    request.TotalPages,
	}
	if request.LanguageMode != "" {
		payload["language_mode"] = request.LanguageMode
	}
	if len(request.Languages) > 0 {
		payload["languages"] = request.Languages
	}
	if len(request.LanguageUsage) > 0 {
		payload["language_usage"] = request.LanguageUsage
	}
	if request.Markup != nil {
		payload["markup_action"] = request.Markup.Action
		payload["markup_mode"] = request.Markup.Mode
		payload["markup_query"] = request.Markup.Query
		payload["markup_color"] = request.Markup.Color
		if request.Markup.Selection != nil {
			payload["markup_selection"] = request.Markup.Selection
		}
	}
	if len(request.SourceFiles) > 0 {
		payload["source_files"] = request.SourceFiles
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	responseData, status, err := c.doJSON(ctx, http.MethodPost, "/internal/ocr/v2/jobs", request.RequestID, data)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeWorkerJSONError(responseData, status)
	}
	var statusResponse JobStatus
	if err := json.Unmarshal(responseData, &statusResponse); err != nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: status, Message: "worker returned malformed OCR V2 job status"}
	}
	return &statusResponse, nil
}

func (c *Client) GetArtifact(ctx context.Context, jobID string) (*ArtifactResult, error) {
	if c == nil || c.HTTP == nil {
		return nil, &WorkerError{Code: ErrWorkerAuthentication, Message: "worker client is not configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/internal/ocr/v2/jobs/"+jobID+"/result", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Request-ID", jobID)
	if c.Signer != nil {
		if err := c.Signer(req); err != nil {
			return nil, err
		}
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeWorkerJSONError(data, response.StatusCode)
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/pdf"
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "application/pdf") || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: response.StatusCode, Message: "worker returned a non-PDF searchable artifact"}
	}
	return &ArtifactResult{Bytes: data, Filename: contentDispositionFilename(response.Header.Get("Content-Disposition")), ContentType: contentType}, nil
}

func contentDispositionFilename(value string) string {
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "filename=") {
			return strings.Trim(strings.TrimSpace(part[len("filename="):]), "\"")
		}
	}
	return "document-searchable.pdf"
}

func (c *Client) GetJob(ctx context.Context, jobID string) (*JobStatus, error) {
	data, status, err := c.doJSON(ctx, http.MethodGet, "/internal/ocr/v2/jobs/"+jobID, jobID, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeWorkerJSONError(data, status)
	}
	var statusResponse JobStatus
	if err := json.Unmarshal(data, &statusResponse); err != nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: status, Message: "worker returned malformed OCR V2 job status"}
	}
	return &statusResponse, nil
}

func (c *Client) GetResult(ctx context.Context, jobID string) (*TextResponse, error) {
	data, status, err := c.doJSON(ctx, http.MethodGet, "/internal/ocr/v2/jobs/"+jobID+"/result", jobID, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeWorkerJSONError(data, status)
	}
	var response TextResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: status, Message: "worker returned malformed OCR V2 result"}
	}
	return &response, nil
}

func (c *Client) GetStructuredResult(ctx context.Context, jobID string) (json.RawMessage, error) {
	data, status, err := c.doJSON(ctx, http.MethodGet, "/internal/ocr/v2/jobs/"+jobID+"/result", jobID, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeWorkerJSONError(data, status)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: status, Message: "worker returned malformed structured OCR result"}
	}
	return json.RawMessage(data), nil
}

func (c *Client) CancelJob(ctx context.Context, jobID, ownerIdentity string) (*JobStatus, error) {
	data, err := json.Marshal(map[string]string{"owner_identity": ownerIdentity})
	if err != nil {
		return nil, err
	}
	responseData, status, err := c.doJSON(ctx, http.MethodPost, "/internal/ocr/v2/jobs/"+jobID+"/cancel", jobID, data)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeWorkerJSONError(responseData, status)
	}
	var statusResponse JobStatus
	if err := json.Unmarshal(responseData, &statusResponse); err != nil {
		return nil, &WorkerError{Code: ErrInvalidEngineOutput, HTTPStatus: status, Message: "worker returned malformed OCR V2 cancellation status"}
	}
	return &statusResponse, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, requestID string, body []byte) ([]byte, int, error) {
	if c == nil || c.HTTP == nil {
		return nil, 0, &WorkerError{Code: ErrWorkerAuthentication, Message: "worker client is not configured"}
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	if c.Signer != nil {
		if err := c.Signer(req); err != nil {
			return nil, 0, err
		}
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, err
	}
	return data, response.StatusCode, nil
}

func decodeWorkerJSONError(data []byte, status int) error {
	var envelope struct {
		Detail  Error  `json:"detail"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if envelope.Detail.Code != "" {
			return &WorkerError{Code: envelope.Detail.Code, HTTPStatus: status, Message: envelope.Detail.Message}
		}
		if envelope.Code != "" {
			return &WorkerError{Code: envelope.Code, HTTPStatus: status, Message: envelope.Message}
		}
	}
	return &WorkerError{Code: errorCodeFromResponse(nil, status), HTTPStatus: status, Message: fmt.Sprintf("worker rejected OCR V2 job request (%d)", status)}
}

func errorCodeFromResponse(apiError *Error, status int) string {
	if apiError != nil && apiError.Code != "" {
		return apiError.Code
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ErrTimeout
	case http.StatusServiceUnavailable:
		return ErrEngineUnavailable
	case http.StatusUnauthorized:
		return ErrWorkerAuthentication
	default:
		return ErrEngineFailure
	}
}

func safeWorkerMessage(apiError *Error) string {
	if apiError == nil || strings.TrimSpace(apiError.Message) == "" {
		return "worker rejected OCR V2 execution"
	}
	return apiError.Message
}

package edit

import "net/http"

type Service interface {
	ExtractLayout(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
	CompileLayout(sourceKey string, pagesJSONKey string, sourceName string) (*WorkerJobSubmission, error)
	GetJobStatus(jobID string) (*WorkerJobRecord, error)
	CancelJob(jobID string) (*WorkerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

type EditorLanguageRequest struct {
	Mode      string   `json:"language_mode"`
	Languages []string `json:"languages"`
}

type StudioEditorLanguageService interface {
	ExtractLayoutV2WithLanguage(sourceKey, filePassword, sourceName string, language EditorLanguageRequest) (*WorkerJobSubmission, error)
}

type GeneralEditorOCRV2LanguageService interface {
	ExtractLayoutV2ForGeneralEditorWithLanguage(sourceKey, filePassword, sourceName string, language EditorLanguageRequest) (*WorkerJobSubmission, error)
}

// LegacyEditorService is the explicit seam for the ordinary /edit-pdf
// product. It is separate from the generic ExtractLayout method so Studio
// compatibility callers cannot inherit the legacy editor selector.
type LegacyEditorService interface {
	ExtractLayoutForLegacyEditor(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
}

// OCRV2Service is an explicit opt-in seam for the V2 editor integration. The
// Studio path uses this method and remains on the worker's internal engine.
type OCRV2Service interface {
	ExtractLayoutV2(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
}

// GeneralEditorOCRV2Service is the General Editor-specific V2 seam. It is
// separate from OCRV2Service because Studio shares the worker endpoint but is
// intentionally not part of this consumer migration.
type GeneralEditorOCRV2Service interface {
	ExtractLayoutV2ForGeneralEditor(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

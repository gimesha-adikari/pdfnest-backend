package edit

import "net/http"

type Service interface {
	ExtractLayout(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
	CompileLayout(sourceKey string, pagesJSONKey string, sourceName string) (*WorkerJobSubmission, error)
	GetJobStatus(jobID string) (*WorkerJobRecord, error)
	CancelJob(jobID string) (*WorkerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

// OCRV2Service is an explicit opt-in seam for the V2 editor integration. The
// legacy Service contract remains unchanged for V1 callers.
type OCRV2Service interface {
	ExtractLayoutV2(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

package edit

import "net/http"

type Service interface {
	ExtractLayout(sourceKey string, filePassword string, sourceName string) (*WorkerJobSubmission, error)
	CompileLayout(sourceKey string, pagesJSONKey string, sourceName string) (*WorkerJobSubmission, error)
	GetJobStatus(jobID string) (*WorkerJobRecord, error)
	CancelJob(jobID string) (*WorkerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

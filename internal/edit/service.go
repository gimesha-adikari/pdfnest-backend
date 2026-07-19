package edit

import "net/http"

type Service interface {
	ExtractLayout(sourceKey string, filePassword string, sourceName string) (*workerJobSubmission, error)
	CompileLayout(sourceKey string, pagesJSONKey string, sourceName string) (*workerJobSubmission, error)
	GetJobStatus(jobID string) (*workerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

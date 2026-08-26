package markup

import "net/http"

type Service interface {
	HighlightPDF(sourceKey string, payloadKey string, sourceName string) (*WorkerJobSubmission, error)
	UnderlinePDF(sourceKey string, payloadKey string, sourceName string) (*WorkerJobSubmission, error)
	StrikeoutPDF(sourceKey string, payloadKey string, sourceName string) (*WorkerJobSubmission, error)
	GetJobStatus(jobID string) (*WorkerJobRecord, error)
	CancelJob(jobID string) (*WorkerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

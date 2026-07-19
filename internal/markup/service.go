package markup

import "net/http"

type Service interface {
	HighlightPDF(sourceKey string, payloadKey string, sourceName string) (*workerJobSubmission, error)
	UnderlinePDF(sourceKey string, payloadKey string, sourceName string) (*workerJobSubmission, error)
	StrikeoutPDF(sourceKey string, payloadKey string, sourceName string) (*workerJobSubmission, error)
	GetJobStatus(jobID string) (*workerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

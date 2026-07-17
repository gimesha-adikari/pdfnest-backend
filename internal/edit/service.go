package edit

import "net/http"

type Service interface {
	ExtractLayout(pdfPath string, filePassword string) (*workerJobSubmission, error)
	CompileLayout(originalPdf string, payload []byte) (*workerJobSubmission, error)
	GetJobStatus(jobID string) (*workerJobRecord, error)
	GetJobDownload(jobID string) (*http.Response, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

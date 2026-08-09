package ocr

import "context"

type R2ImageRef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type Service interface {
	ExtractTextFromPDF(ctx context.Context, inputPath string, lang string) (string, error)
	ImageToTextPDF(ctx context.Context, imagePaths []string, lang string) (string, error)

	ImageToTextPDFFromR2(ctx context.Context, files []R2ImageRef, lang string) (string, error)
}

type ocrService struct{}

func NewService() Service {
	return &ocrService{}
}

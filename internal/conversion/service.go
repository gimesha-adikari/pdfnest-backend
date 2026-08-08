package conversion

import (
	"context"
	"mime/multipart"
)

type Service interface {
	ImagesToPDF(imagePaths []string) (string, error)
	PdfToImagesBackend(ctx context.Context, inputPath string, imageType string) (string, error)
	CustomImagesToPDF(imagePaths []string, layout []CanvasLayoutItem) (string, error)
	ConvertPageToImageStream(ctx context.Context, fileHeader *multipart.FileHeader, pageNum int, scale float64) ([]byte, error)
	OfficeToPdf(ctx context.Context, inputPath string) (string, error)
	HtmlToPdf(ctx context.Context, targetURL string, opts PrintOptions) (string, error)
	MarkdownToPdf(ctx context.Context, inputMdPath string, opts PrintOptions) (string, error)
	CodeToPdf(ctx context.Context, inputCodePath string, fileName string, opts PrintOptions) (string, error)
}

type ConversionService struct{}

func NewService() Service {
	return &ConversionService{}
}

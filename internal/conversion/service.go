package conversion

import "mime/multipart"

type Service interface {
	ImagesToPDF(imagePaths []string) (string, error)
	PdfToImagesBackend(inputPath string) (string, error)
	CustomImagesToPDF(imagePaths []string, layout []CanvasLayoutItem) (string, error)
	ConvertPageToImageStream(fileHeader *multipart.FileHeader, pageNum int, scale float64) ([]byte, error)
	OfficeToPdf(inputPath string) (string, error)
	HtmlToPdf(targetURL string, opts PrintOptions) (string, error)
	MarkdownToPdf(inputMdPath string, opts PrintOptions) (string, error)
	CodeToPdf(inputCodePath string, fileName string, opts PrintOptions) (string, error)
}

type ConversionService struct{}

func NewService() Service {
	return &ConversionService{}
}

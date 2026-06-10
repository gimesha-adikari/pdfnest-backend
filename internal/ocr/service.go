package ocr

type Service interface {
	ExtractTextFromPDF(inputPath string) (string, error)
	ImageToTextPDF(imagePaths []string) (string, error)
}

type ocrService struct{}

func NewService() Service {
	return &ocrService{}
}

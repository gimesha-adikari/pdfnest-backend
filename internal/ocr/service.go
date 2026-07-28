package ocr

type Service interface {
	ExtractTextFromPDF(inputPath string, lang string) (string, error)
	ImageToTextPDF(imagePaths []string, lang string) (string, error)
}

type ocrService struct{}

func NewService() Service {
	return &ocrService{}
}

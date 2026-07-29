package ocr

type R2ImageRef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type Service interface {
	ExtractTextFromPDF(inputPath string, lang string) (string, error)
	ImageToTextPDF(imagePaths []string, lang string) (string, error)

	ImageToTextPDFFromR2(files []R2ImageRef, lang string) (string, error)
}

type ocrService struct{}

func NewService() Service {
	return &ocrService{}
}

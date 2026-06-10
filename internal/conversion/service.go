package conversion

type Service interface {
	ImagesToPDF(imagePaths []string) (string, error)
	PdfToImagesBackend(inputPath string) (string, error)
}

type imagesService struct{}

func NewService() Service {
	return &imagesService{}
}

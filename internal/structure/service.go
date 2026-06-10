package structure

type Service interface {
	MergePDFs(inputPaths []string) (string, error)
	SplitPDF(inputPath string, pageSelection []string) (string, error)
	RotatePDF(inputPath string, rotations map[string]int) (string, error)
	DeletePDFPages(inputPath string, pagesToDelete []string) (string, error)
	ReorderPDFPages(inputPath string, sequence []string) (string, error)
	WatermarkPDF(inputPath string, text string, imagePath string, description string) (string, error)
	AddPageNumbersPDF(inputPath string, description string) (string, error)
	UpdateMetadataPDF(inputPath string, metadata map[string]string) (string, error)
}

type structureService struct{}

func NewService() Service {
	return &structureService{}
}

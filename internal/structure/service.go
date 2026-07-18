package structure

type Service interface {
	MergePDFs(inputPaths []string) (string, error)
	SplitPDF(inputPath string, pageSelection []string) (string, error)
	RotatePDF(inputPath string, rotations map[string]int) (string, error)
	DeletePDFPages(inputPath string, pagesToDelete []string) (string, error)
	ReorderPDFPages(inputPath string, sequence []string) (string, error)
	WatermarkPDF(inputPath string, text string, imagePath string, description string) (string, error)
	AddPageNumbersPDF(inputPath string, description string) (string, error)
	UpdateMetadataPDF(inputPath string, metadata map[string]string, password string) (string, error)
	GetMetadataPDF(inputPath string, password string) (map[string]string, error)
	CropPDF(inputPath string, cropBoxDesc string) (string, error)
	DuplicatePDFPages(inputPath string, pageSelection string, copies int) (string, error)
	InsertBlankPages(inputPath string, insertAt string, targetPage int, count int) (string, error)
	AddTextToPDF(inputPath string, elements []TextElement) (string, error)
	AnalyzePDF(inputPath, filePassword string) (*PDFAnalysis, error)
}
type structureService struct{}

func NewService() Service {
	return &structureService{}
}

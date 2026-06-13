package security

type Service interface {
	EncryptPDF(inputPath string, password string) (string, error)
	DecryptPDF(inputPath string, password string) (string, error)
	RedactPageText(inputPath string, outputPath string, keywords []string, boxesStr string) (string, error)
}

type securityService struct{}

func NewService() Service {
	return &securityService{}
}

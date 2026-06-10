package security

type Service interface {
	EncryptPDF(inputPath string, password string) (string, error)
	DecryptPDF(inputPath string, password string) (string, error)
}

type securityService struct{}

func NewService() Service {
	return &securityService{}
}

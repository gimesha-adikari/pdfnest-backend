package edit

type CompileRequest struct {
	HTML string `json:"html"`
}

type ExtractResponse struct {
	Success bool   `json:"success"`
	HTML    string `json:"html"`
	Error   string `json:"error"`
}

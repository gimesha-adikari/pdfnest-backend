package structure

import (
	"bytes"
	"fmt"
	"os/exec"
)

func SignPdfMulti(
	inputPdfPath,
	signatureImgPath,
	outputPath,
	stampsJson string,
) error {

	cmd := exec.Command(
		"./venv/bin/python",
		"./scripts/sign_pdf.py",
		inputPdfPath,
		signatureImgPath,
		outputPath,
		stampsJson,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"python signing failed: %w\n%s",
			err,
			stderr.String(),
		)
	}

	return nil
}

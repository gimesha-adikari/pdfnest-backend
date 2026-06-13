// file: internal/edit/python.go
package edit

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

func runPythonScript(script string, args ...string) ([]byte, error) {
	pythonExec := filepath.Join(".", "venv", "bin", "python")

	cmdArgs := append([]string{script}, args...)

	cmd := exec.Command(pythonExec, cmdArgs...)

	output, err := cmd.CombinedOutput()

	if err != nil {
		return nil, fmt.Errorf(
			"python execution failed: %s",
			string(output),
		)
	}

	return output, nil
}

package conversion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"pdfnest-backend/internal/process"
)

const codeHighlightWorkerCommand = "code-highlight-worker"

// Chroma includes eager/delegating lexers that cannot observe a Go context.
// Run them in the same executable's private worker mode so the existing process
// runner can terminate all CPU work on cancellation/deadline. No goroutine is
// left tokenizing after the request ends, and no separate binary is required.
func runCodeHighlighter(ctx context.Context, source []byte, language string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, markdownRenderTimeout)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	workspace, err := os.MkdirTemp("", "code-highlight-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workspace)
	output := filepath.Join(workspace, "tokens.html")
	command := exec.Command(executable, codeHighlightWorkerCommand, language, output)
	command.Stdin = bytes.NewReader(source)
	if _, err := (process.Runner{}).RunCommand(ctx, 0, command); err != nil {
		return "", fmt.Errorf("code highlighting worker: %w", err)
	}
	file, err := os.Open(output)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > markdownMaxHTMLBytes {
		return "", fmt.Errorf("invalid highlighted code output")
	}
	data, err := io.ReadAll(io.LimitReader(file, markdownMaxHTMLBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > markdownMaxHTMLBytes {
		return "", fmt.Errorf("highlighted code exceeds 16 MiB")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}

// RunCodeHighlightWorker dispatches only the explicit internal worker command.
// The server calls it before initialization; normal invocations are untouched.
// Input arrives on stdin and only the parent's private output file is created.
func RunCodeHighlightWorker(args []string) (bool, error) {
	if len(args) == 0 || args[0] != codeHighlightWorkerCommand {
		return false, nil
	}
	if len(args) != 3 {
		return true, fmt.Errorf("invalid code highlighting worker arguments")
	}
	language := args[1]
	allowed := language == "plaintext"
	for _, value := range codeLanguages {
		allowed = allowed || language == value
	}
	if !allowed {
		return true, fmt.Errorf("invalid code highlighting language")
	}
	source, err := io.ReadAll(io.LimitReader(os.Stdin, markdownMaxHTMLBytes+1))
	if err != nil {
		return true, err
	}
	if len(source) > markdownMaxHTMLBytes {
		return true, fmt.Errorf("code document exceeds 16 MiB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), markdownRenderTimeout)
	defer cancel()
	fragment, err := highlightCode(ctx, string(source), language)
	if err != nil {
		return true, err
	}
	if len(fragment) > markdownMaxHTMLBytes {
		return true, fmt.Errorf("highlighted code exceeds 16 MiB")
	}
	file, err := os.OpenFile(args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return true, err
	}
	if _, err := io.WriteString(file, fragment); err != nil {
		file.Close()
		return true, err
	}
	return true, file.Close()
}

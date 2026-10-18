package conversion

import (
	"context"
	"fmt"
	"io"
	"os"
)

func (s *ConversionService) CodeToPdf(ctx context.Context, inputCodePath string, fileName string, opts PrintOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, markdownRenderTimeout)
	defer cancel()
	source, err := readCodeSource(ctx, inputCodePath)
	if err != nil {
		return "", fmt.Errorf("reading source code: %w", err)
	}
	document, err := prepareCodeDocument(ctx, source, fileName)
	if err != nil {
		return "", err
	}

	// Reuse the offline browser unchanged, with no authorized image resources.
	// All highlighting is static: document scripts remain disabled throughout.
	browser, err := newMarkdownBrowser(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("starting code renderer: %w", err)
	}
	defer browser.close()
	if err := browser.setDocument(document); err != nil {
		return "", fmt.Errorf("rendering code: %w", err)
	}
	file, err := os.CreateTemp("", "code-compiled-*.pdf")
	if err != nil {
		return "", err
	}
	return completeMarkdownPDFOutput(ctx, file,
		func(writer io.Writer) error { return browser.printPDF(writer, opts) },
		browser.sealPolicyEvents,
	)
}

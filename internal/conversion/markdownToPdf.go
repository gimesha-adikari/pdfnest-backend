package conversion

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const markdownRenderTimeout = 35 * time.Second

func (s *ConversionService) MarkdownToPdf(ctx context.Context, inputMdPath string, opts PrintOptions) (output string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, markdownRenderTimeout)
	defer cancel()

	source, err := readMarkdownSource(ctx, inputMdPath)
	if err != nil {
		return "", fmt.Errorf("reading Markdown: %w", err)
	}
	document, err := prepareMarkdownDocument(ctx, source)
	if err != nil {
		return "", err
	}

	browser, err := newMarkdownBrowser(ctx, document.images)
	if err != nil {
		return "", err
	}
	defer browser.close()
	if err := browser.setDocument(document.html); err != nil {
		return "", err
	}

	// The output stays private until streaming, PDF validation and the browser
	// policy-event boundary have all succeeded.
	file, err := os.CreateTemp("", "md-compiled-*.pdf")
	if err != nil {
		return "", err
	}
	output, err = completeMarkdownPDFOutput(ctx, file,
		func(writer io.Writer) error { return browser.printPDF(writer, opts) },
		browser.sealPolicyEvents,
	)
	if err != nil {
		return "", err
	}
	return output, nil
}

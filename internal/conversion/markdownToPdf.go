package conversion

import (
	"context"
	"fmt"
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

	// The output is owned here until every print, byte-budget and security
	// check has succeeded. Never leave a partially written successful result.
	file, err := os.CreateTemp("", "md-compiled-*.pdf")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if err = browser.printPDF(file, opts); err != nil {
		return "", err
	}
	if err = browser.policyError(); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

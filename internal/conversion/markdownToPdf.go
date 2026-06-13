package conversion

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"github.com/yuin/goldmark"
)

func (s *ConversionService) MarkdownToPdf(inputMdPath string, opts PrintOptions) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	finalPdfPath := filepath.Join(tempDir, "md-compiled-"+sessionID+".pdf")

	mdData, err := os.ReadFile(inputMdPath)
	if err != nil {
		return "", fmt.Errorf("failed reading source markdown payload file: %w", err)
	}

	var htmlBuf bytes.Buffer
	if err := goldmark.Convert(mdData, &htmlBuf); err != nil {
		return "", fmt.Errorf("goldmark markdown processing token failure: %w", err)
	}

	styledHtml := fmt.Sprintf(`<!DOCTYPE html>
	<html>
	<head>
		<meta charset="UTF-8">
		<title>Markdown Export</title>
		<link href="https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/themes/prism-tomorrow.min.css" rel="stylesheet" />
		<style>
			*, *:before, *:after {
				box-sizing: border-box !important;
			}
			html, body { 
				/* MATCHED: Applied image_6c40da.png editor color variables */
				background-color: #1e1e24 !important; 
				color: #e3e3e6 !important; 
				font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
				padding: 3rem;
				margin: 0;
				width: 100%%;
			}
			@media print {
				html, body { background-color: #1e1e24 !important; color: #e3e3e6 !important; }
				pre { page-break-inside: avoid; }
			}
			.content-wrapper { 
				width: 100%%;
				max-w: 48rem; 
				margin-left: auto; 
				margin-right: auto;
				padding-left: 0.5rem;
				padding-right: 0.5rem;
			}
			h1 { font-size: 2.25rem; font-weight: 800; margin-top: 0; margin-bottom: 1.5rem; color: #ffffff; border-bottom: 1px solid #2d2d34; padding-bottom: 0.5rem; }
			h2 { font-size: 1.5rem; font-weight: 700; margin-top: 2rem; margin-bottom: 1rem; color: #f3f4f6; }
			h3 { font-size: 1.25rem; font-weight: 600; margin-top: 1.5rem; margin-bottom: 0.75rem; color: #e5e7eb; }
			p { margin-bottom: 1.25rem; line-height: 1.75; color: #cbcbd0; font-size: 1rem; word-break: break-word; }
			
			code { background-color: #18181c; padding: 0.2rem 0.4rem; border-radius: 0.375rem; font-family: monospace; font-size: 0.875rem; color: #f43f5e; border: 1px solid #2d2d34; }
			
			pre[class*="language-"] { 
				background-color: #18181c !important; 
				padding: 1.25rem; 
				border-radius: 0.75rem; 
				overflow-x: auto; 
				margin-bottom: 1.5rem; 
				border: 1px solid #2d2d34;
				width: auto !important;
				max-width: 100%% !important;
			}
			pre[class*="language-"] code { background-color: transparent !important; padding: 0; color: #e3e3e6; font-size: 0.875rem; border: none; }
			ul { list-style-type: disc; padding-left: 1.5rem; margin-bottom: 1.25rem; color: #cbcbd0; }
			ol { list-style-type: decimal; padding-left: 1.5rem; margin-bottom: 1.25rem; color: #cbcbd0; }
			li { margin-bottom: 0.5rem; }
			blockquote { border-left: 4px solid #6366f1; padding-left: 1rem; font-style: italic; color: #9ca3af; margin-bottom: 1.5rem; margin-top: 0; }
		</style>
	</head>
	<body>
		<div class="content-wrapper">
			%s
		</div>
		<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/components/prism-core.min.js"></script>
		<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/plugins/autoloader/prism-autoloader.min.js"></script>
	</body>
	</html>`, htmlBuf.String())

	tempHtmlPath := filepath.Join(tempDir, "md-render-"+sessionID+".html")
	if err := os.WriteFile(tempHtmlPath, []byte(styledHtml), 0644); err != nil {
		return "", fmt.Errorf("failed writing intermediate render layout to disk: %w", err)
	}
	defer func() { _ = os.Remove(tempHtmlPath) }()

	pWidth, pHeight := 8.27, 11.69
	switch strings.ToLower(opts.PaperSize) {
	case "letter":
		pWidth, pHeight = 8.5, 11.0
	case "legal":
		pWidth, pHeight = 8.5, 14.0
	}

	chromeOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox, chromedp.DisableGPU)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), chromeOpts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 35*time.Second)
	defer cancelTimeout()

	var buf []byte
	fileURL := "file://" + tempHtmlPath

	err = chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(1200, 950, 1.0, false),
		chromedp.Navigate(fileURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Sleep(800*time.Millisecond),

		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithMedia("screen").Do(ctx)
		}),

		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			printParams := page.PrintToPDF()
			printParams.PrintBackground = true
			printParams.Landscape = false
			printParams.PaperWidth = pWidth
			printParams.PaperHeight = pHeight
			printParams.MarginTop = opts.MarginTop
			printParams.MarginBottom = opts.MarginBottom
			printParams.MarginLeft = opts.MarginLeft
			printParams.MarginRight = opts.MarginRight
			printParams.PreferCSSPageSize = false

			buf, _, err = printParams.Do(ctx)
			return err
		}),
	)

	if err != nil {
		return "", fmt.Errorf("markdown custom generation session crashed: %w", err)
	}

	if err := os.WriteFile(finalPdfPath, buf, 0644); err != nil {
		return "", fmt.Errorf("failed writing browser pdf stream to workspace disk: %w", err)
	}

	return finalPdfPath, nil
}

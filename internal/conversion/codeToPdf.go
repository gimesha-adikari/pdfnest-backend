package conversion

import (
	"context"
	"fmt"
	"html" // Standard library to safely sanitize source code syntax characters
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
)

// CodeToPdf reads any raw plain text or source script file, applies syntax color highlighting, and prints to PDF
func (s *ConversionService) CodeToPdf(inputCodePath string, fileName string, opts PrintOptions) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	finalPdfPath := filepath.Join(tempDir, "code-compiled-"+sessionID+".pdf")

	// 1. Read input script code file into a safe byte array
	codeBytes, err := os.ReadFile(inputCodePath)
	if err != nil {
		return "", fmt.Errorf("failed reading source code asset file: %w", err)
	}

	// 2. Sanitize source syntax symbols (<, >, &) to keep HTML parsing structural integrity safe
	escapedCode := html.EscapeString(string(codeBytes))

	// 3. Match the file extension parameter to load the correct syntax highlighting parser engine token
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))
	langClass := "language-" + ext
	if ext == "txt" || ext == "" {
		langClass = "language-plaintext"
	}

	// 4. Compile raw syntax text into the absolute #1e1e24 background matching your image snapshot
	styledHtml := fmt.Sprintf(`<!DOCTYPE html>
	<html>
	<head>
		<meta charset="UTF-8">
		<title>Code Export</title>
		<link href="https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/themes/prism-tomorrow.min.css" rel="stylesheet" />
		<style>
			/* Global box sizing control setup */
			*, *:before, *:after {
				box-sizing: border-box !important;
			}
			html, body { 
				/* MATCHED: Applied exact image_6c40da.png editor palette */
				background-color: #1e1e24 !important; 
				color: #e3e3e6 !important; 
				font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
				padding: 0;
				margin: 0;
				width: 100%%;
			}
			@media print {
				html, body { 
					background-color: #1e1e24 !important; 
					color: #e3e3e6 !important; 
				}
				.code-wrapper {
					padding-top: 2rem !important;
					padding-bottom: 2rem !important;
				}
				pre { 
					page-break-inside: auto !important; 
				}
				tr, p, pre, code {
					page-break-inside: avoid;
				}
			}

			.code-wrapper {
				width: 100%%;
				max-w: 100%%;
				padding: 2.5rem;
				margin: 0 auto;
			}

			/* Reverted structure format matching original setup while updating background attributes */
			pre[class*="language-"] { 
				background-color: #18181c !important; 
				padding: 1.5rem !important; 
				border-radius: 0.75rem; 
				border: 1px solid #2d2d34;
				font-size: 13px !important;
				line-height: 1.6 !important;
				width: 100%% !important;
				max-w: 100%% !important;
				white-space: pre-wrap !important;       /* Wraps scripts past margins safely */
				word-wrap: break-word !important;       
				word-break: break-all !important;
				box-sizing: border-box !important;
			}
			
			code[class*="language-"] {
				color: #e3e3e6 !important;
				font-family: inherit !important;
				white-space: pre-wrap !important;       
				word-break: break-all !important;
			}
		</style>
	</head>
	<body>
		<div class="code-wrapper">
			<pre class="%s"><code>%s</code></pre>
		</div>
		<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/components/prism-core.min.js"></script>
		<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/plugins/autoloader/prism-autoloader.min.js"></script>
	</body>
	</html>`, langClass, escapedCode)

	tempHtmlPath := filepath.Join(tempDir, "code-render-"+sessionID+".html")
	if err := os.WriteFile(tempHtmlPath, []byte(styledHtml), 0644); err != nil {
		return "", fmt.Errorf("failed writing intermediate code layout to disk: %w", err)
	}
	defer func() { _ = os.Remove(tempHtmlPath) }()

	pWidth, pHeight := 8.27, 11.69
	switch strings.ToLower(opts.PaperSize) {
	case "letter":
		pWidth, pHeight = 8.5, 11.0
	case "legal":
		pWidth, pHeight = 8.5, 14.0
	}

	chromeOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.DisableGPU,
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), chromeOpts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 35*time.Second)
	defer cancelTimeout()

	var buf []byte
	fileURL := "file://" + tempHtmlPath

	err = chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(1200, 1000, 1.0, false),
		chromedp.Navigate(fileURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Sleep(1000*time.Millisecond),

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
		return "", fmt.Errorf("code-to-pdf translation engine crashed: %w", err)
	}

	if err := os.WriteFile(finalPdfPath, buf, 0644); err != nil {
		return "", fmt.Errorf("failed writing browser pdf block stream to disk: %w", err)
	}

	return finalPdfPath, nil
}

package conversion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
)

// HtmlToPdf captures any remote web URL into a high-fidelity continuous portrait PDF using custom page boundaries
func (s *ConversionService) HtmlToPdf(targetURL string, opts PrintOptions) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	finalPdfPath := filepath.Join(tempDir, "web-compiled-"+sessionID+".pdf")

	chromeOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), chromeOpts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second) // 60s accommodates scrolling and rendering large sites safely
	defer cancelTimeout()

	var buf []byte

	err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(1920, 1080, 1.0, false),

		chromedp.Navigate(targetURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond),

		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithMedia("screen").Do(ctx)
		}),

		// Human-mimicking scroll loop simulation to activate hidden lazy loaded components safely
		chromedp.Evaluate(`
			new Promise((resolve) => {
				let totalHeight = 0;
				const distance = 80; 
				const timer = setInterval(() => {
					const scrollHeight = document.body.scrollHeight;
					window.scrollBy(0, distance);
					totalHeight += distance;

					if (totalHeight >= scrollHeight) {
						clearInterval(timer);
						setTimeout(() => {
							window.scrollTo(0, 0); 
							resolve();
						}, 1000);
					}
				}, 45); 
			});
		`, nil),

		chromedp.Sleep(2000*time.Millisecond),

		// Injected layout visibility sanitizer fallback rules
		chromedp.Evaluate(`
			const style = document.createElement('style');
			style.innerHTML = `+"`"+`
				@media print {
					html, body {
						background-color: #030712 !important; 
						color: #f3f4f6 !important;
					}
					div, section, article, main, p, span, h1, h2, h3, a {
						display: unset !important;
						visibility: visible !important;
						opacity: 1 !important;
						transform: none !important;
						transition: none !important;
						animation: none !important;
					}
					.grid, [class*="grid"], .flex, [class*="flex"] {
						display: flex !important;
						flex-direction: row !important;
						flex-wrap: wrap !important;
					}
				}
			`+"`"+`;
			document.head.appendChild(style);
		`, nil),

		chromedp.Sleep(500*time.Millisecond),

		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error

			printParams := page.PrintToPDF()
			printParams.PrintBackground = true
			printParams.Landscape = false
			printParams.PaperWidth = 11.0  // Retains full desktop layout scale aspect metrics
			printParams.PaperHeight = 16.5 // Extra vertical breathing room per page block

			// Dynamic client-side layout adjustments
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
		return "", fmt.Errorf("high-fidelity webpage extraction process failed: %w", err)
	}

	if err := os.WriteFile(finalPdfPath, buf, 0644); err != nil {
		return "", fmt.Errorf("failed writing browser pdf stream to workspace disk: %w", err)
	}

	return finalPdfPath, nil
}

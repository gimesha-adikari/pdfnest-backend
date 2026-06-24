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

const DebugHtmlToPdf = false

func saveDebugHTML(debugDir string, html string) {
	if !DebugHtmlToPdf {
		return
	}

	_ = os.WriteFile(
		filepath.Join(debugDir, "page.html"),
		[]byte(html),
		0644,
	)
}

func saveDebugScreenshot(debugDir string, data []byte) {
	if !DebugHtmlToPdf {
		return
	}

	_ = os.WriteFile(
		filepath.Join(debugDir, "screenshot.png"),
		data,
		0644,
	)
}

func saveDebugPDF(debugDir string, data []byte) {
	if !DebugHtmlToPdf {
		return
	}

	_ = os.WriteFile(
		filepath.Join(debugDir, "output.pdf"),
		data,
		0644,
	)
}

func (s *ConversionService) HtmlToPdf(targetURL string, opts PrintOptions) (string, error) {

	tempDir := os.TempDir()
	sessionID := uuid.New().String()

	debugDir := filepath.Join(
		tempDir,
		"pdfnest-debug-"+sessionID,
	)

	if DebugHtmlToPdf {
		_ = os.MkdirAll(debugDir, 0755)
	}

	finalPdfPath := filepath.Join(
		tempDir,
		"web-compiled-"+sessionID+".pdf",
	)

	chromeOpts := append(
		chromedp.DefaultExecAllocatorOptions[:],

		chromedp.NoSandbox,
		chromedp.DisableGPU,

		chromedp.Flag("headless", true),
		chromedp.Flag("disable-dev-shm-usage", true),

		chromedp.UserAgent(
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+
				"AppleWebKit/537.36 (KHTML, like Gecko) "+
				"Chrome/137.0.0.0 Safari/537.36",
		),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(
		context.Background(),
		chromeOpts...,
	)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(
		ctx,
		90*time.Second,
	)
	defer cancelTimeout()

	var (
		buf        []byte
		html       string
		screenshot []byte
	)

	err := chromedp.Run(
		ctx,

		emulation.SetDeviceMetricsOverride(
			1920,
			1080,
			1.0,
			false,
		),

		chromedp.Navigate(targetURL),

		chromedp.WaitVisible(
			"body",
			chromedp.ByQuery,
		),

		chromedp.Sleep(4*time.Second),

		chromedp.OuterHTML("html", &html),
		chromedp.Evaluate(`
(() => {

    const style = document.createElement('style');

    style.innerHTML =
        "*,*::before,*::after{" +
        "opacity:1 !important;" +
        "visibility:visible !important;" +
        "filter:none !important;" +
        "backdrop-filter:none !important;" +
        "transform:none !important;" +
        "animation:none !important;" +
        "transition:none !important;" +
        "}" +
        "html{" +
        "scroll-behavior:auto !important;" +
        "}";

    document.head.appendChild(style);

})();
`, nil),

		chromedp.Evaluate(`
window.scrollTo(
    0,
    document.body.scrollHeight
);
`, nil),

		chromedp.Sleep(3000*time.Millisecond),

		chromedp.Evaluate(`
window.scrollTo(0, 0);
`, nil),

		chromedp.Sleep(2000*time.Millisecond),

		chromedp.Evaluate(`
document.querySelectorAll('*').forEach(el => {
	el.style.opacity = '1';
	el.style.visibility = 'visible';
});
`, nil),

		chromedp.FullScreenshot(
			&screenshot,
			90,
		),

		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.
				SetEmulatedMedia().
				WithMedia("screen").
				Do(ctx)
		}),

		chromedp.Evaluate(`
			new Promise((resolve) => {

				let previousHeight = -1;

				const run = async () => {

					while (true) {

						window.scrollTo(
							0,
							document.body.scrollHeight
						);

						await new Promise(
							r => setTimeout(r, 1200)
						);

						const currentHeight =
							document.body.scrollHeight;

						if (
							currentHeight === previousHeight
						) {
							break;
						}

						previousHeight =
							currentHeight;
					}

					window.scrollTo(0, 0);

					setTimeout(resolve, 1500);
				};

				run();
			});
		`, nil),

		chromedp.Sleep(2*time.Second),

		chromedp.ActionFunc(func(ctx context.Context) error {

			var err error

			printParams := page.PrintToPDF()

			printParams.PrintBackground = true

			printParams.Landscape = false

			printParams.MarginTop = opts.MarginTop
			printParams.MarginBottom = opts.MarginBottom
			printParams.MarginLeft = opts.MarginLeft
			printParams.MarginRight = opts.MarginRight

			printParams.PreferCSSPageSize = true

			buf, _, err = printParams.Do(ctx)

			return err
		}),
	)

	if DebugHtmlToPdf {

		saveDebugHTML(
			debugDir,
			html,
		)

		saveDebugScreenshot(
			debugDir,
			screenshot,
		)
	}

	if err != nil {
		return "", fmt.Errorf(
			"html to pdf conversion failed: %w",
			err,
		)
	}

	if err := os.WriteFile(
		finalPdfPath,
		buf,
		0644,
	); err != nil {
		return "", fmt.Errorf(
			"failed writing pdf: %w",
			err,
		)
	}

	if DebugHtmlToPdf {

		saveDebugPDF(
			debugDir,
			buf,
		)

		fmt.Println(
			"[PDFNEST DEBUG] artifacts saved:",
			debugDir,
		)
	}

	return finalPdfPath, nil
}

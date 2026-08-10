package conversion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/process"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
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

const (
	defaultHTMLPDFReadyDeadline  = 8 * time.Second
	defaultHTMLPDFScrollDeadline = 4 * time.Second
	defaultHTMLPDFPrintDeadline  = 2 * time.Second
)

func htmlPDFReadyDeadlineMs(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}

	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return fallback
	}

	return time.Duration(ms) * time.Millisecond
}

func waitForPageQuiet(actCtx context.Context, deadline time.Duration) error {
	if deadline <= 0 {
		deadline = defaultHTMLPDFReadyDeadline
	}

	deadlineAt := time.Now().Add(deadline)

	const jsCheck = `(() => {
		const resources = performance.getEntriesByType('resource');
		const incompleteResources = resources.filter(
			(e) => !e.responseEnd || e.responseEnd === 0
		).length;
		const pendingImages = Array.from(document.images || []).filter(
			(img) => !img.complete
		).length;
		const fontsLoaded = !document.fonts || document.fonts.status === 'loaded';
		return document.readyState === 'complete' &&
			incompleteResources === 0 &&
			pendingImages === 0 &&
			fontsLoaded;
	})()`

	for {
		if time.Now().After(deadlineAt) {
			return nil
		}

		var quiet bool
		if err := chromedp.Evaluate(jsCheck, &quiet).Do(actCtx); err != nil {
			// The page may still be navigating; treat evaluation failures as
			// "not quiet" and keep polling until the deadline.
			quiet = false
		}
		if quiet {
			return nil
		}

		select {
		case <-actCtx.Done():
			return actCtx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (s *ConversionService) HtmlToPdf(ctx context.Context, targetURL string, opts PrintOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

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

	allocCtx, allocCancel := process.NewHardenedExecAllocator(
		ctx,
		chromeOpts...,
	)
	defer allocCancel()

	chromeCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	chromeCtx, cancelTimeout := context.WithTimeout(
		chromeCtx,
		90*time.Second,
	)
	defer cancelTimeout()

	var (
		buf        []byte
		html       string
		screenshot []byte
	)

	err := chromedp.Run(
		chromeCtx,

		emulation.SetDeviceMetricsOverride(
			1920,
			1080,
			1.0,
			false,
		),

		// Inject timer tracking script before navigation so setTimeout/clearTimeout
		// calls scheduled by page JS are observed by the readiness loop.
		chromedp.ActionFunc(func(actCtx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`(() => {
				window.__activeTimers = 0;
				const origSetTimeout = window.setTimeout;
				const origClearTimeout = window.clearTimeout;
				window.setTimeout = function(fn, delay, ...args) {
					window.__activeTimers++;
					const id = origSetTimeout.call(this, () => {
						try {
							fn.apply(this, args);
						} finally {
							window.__activeTimers--;
						}
					}, delay);
					return id;
				};
				window.clearTimeout = function(id) {
					origClearTimeout.call(this, id);
					window.__activeTimers--;
				};
			})()`).Do(actCtx)
			return err
		}),

		chromedp.Navigate(targetURL),

		chromedp.WaitVisible(
			"body",
			chromedp.ByQuery,
		),

		chromedp.ActionFunc(func(actCtx context.Context) error {
			return waitForPageQuiet(
				actCtx,
				htmlPDFReadyDeadlineMs(
					"HTML_PDF_READY_DEADLINE_MS",
					defaultHTMLPDFReadyDeadline,
				),
			)
		}),

		chromedp.ActionFunc(func(actCtx context.Context) error {
			if DebugHtmlToPdf {
				return chromedp.OuterHTML("html", &html).Do(actCtx)
			}
			return nil
		}),
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

		chromedp.ActionFunc(func(actCtx context.Context) error {
			return waitForPageQuiet(
				actCtx,
				htmlPDFReadyDeadlineMs(
					"HTML_PDF_SCROLL_DEADLINE_MS",
					defaultHTMLPDFScrollDeadline,
				),
			)
		}),

		chromedp.Evaluate(`
window.scrollTo(0, 0);
`, nil),

		chromedp.ActionFunc(func(actCtx context.Context) error {
			return waitForPageQuiet(
				actCtx,
				htmlPDFReadyDeadlineMs(
					"HTML_PDF_SCROLL_DEADLINE_MS",
					defaultHTMLPDFScrollDeadline,
				),
			)
		}),

		chromedp.Evaluate(`
document.querySelectorAll('*').forEach(el => {
	el.style.opacity = '1';
	el.style.visibility = 'visible';
});
`, nil),

		chromedp.ActionFunc(func(actCtx context.Context) error {
			if DebugHtmlToPdf {
				return chromedp.FullScreenshot(&screenshot, 90).Do(actCtx)
			}
			return nil
		}),

		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.
				SetEmulatedMedia().
				WithMedia("screen").
				Do(ctx)
		}),

		// Wait for page layout, pending images, active timers, and DOM mutations to
		// settle before printing. Strategy 6 tracks active timers via CDPTimer
		// interception and DOM mutations via MutationObserver so that static pages
		// convert fast (~2.7s) while pages with late JS timers (6s, 8s, 9s) or DOM
		// mutations are reliably captured without arbitrary sleeping.
		chromedp.Evaluate(`
			new Promise((resolve) => {
				let lastChangeTime = Date.now();

				const observer = new MutationObserver(() => {
					lastChangeTime = Date.now();
				});

				const root = document.body || document.documentElement;
				if (root) {
					observer.observe(root, {
						childList: true,
						subtree: true,
						attributes: true,
						characterData: true
					});
				}

				const quietMs = 1200;
				const staticFloorMs = 1500;
				const maxScanMs = 10000;
				const scanStart = Date.now();

				let previousHeight = document.body ? document.body.scrollHeight : 0;

				const poll = async () => {
					while (true) {
						const now = Date.now();
						const elapsed = now - scanStart;
						if (elapsed >= maxScanMs) {
							break;
						}

						window.scrollTo(0, document.body ? document.body.scrollHeight : 0);
						await new Promise(r => setTimeout(r, 400));

						const currentHeight = document.body ? document.body.scrollHeight : 0;
						if (currentHeight !== previousHeight) {
							previousHeight = currentHeight;
							lastChangeTime = Date.now();
						}

						const activeTimers = window.__activeTimers || 0;
						const pendingImages = Array.from(document.images || []).filter(img => !img.complete).length;
						const timeSinceChange = Date.now() - lastChangeTime;

						const hasActiveWork = (activeTimers > 0) || (pendingImages > 0);

						if (!hasActiveWork && elapsed >= staticFloorMs && timeSinceChange >= quietMs) {
							break;
						}
					}

					observer.disconnect();
					window.scrollTo(0, 0);
					setTimeout(resolve, 300);
				};

				poll();
			});
		`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),

		chromedp.ActionFunc(func(actCtx context.Context) error {
			return waitForPageQuiet(
				actCtx,
				htmlPDFReadyDeadlineMs(
					"HTML_PDF_PRINT_DEADLINE_MS",
					defaultHTMLPDFPrintDeadline,
				),
			)
		}),

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

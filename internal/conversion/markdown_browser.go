package conversion

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/audits"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	cdpio "github.com/chromedp/cdproto/io"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"pdfnest-backend/internal/process"
)

// markdownDenyProxy never resolves or dials any upstream, including CONNECT.
// It is deliberately separate from HTML-to-PDF's public-destination netguard.
type markdownDenyProxy struct {
	url    string
	server *http.Server
	done   chan struct{}
	once   sync.Once
}

func newMarkdownDenyProxy(ctx context.Context) (*markdownDenyProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &markdownDenyProxy{url: "http://" + listener.Addr().String(), done: make(chan struct{})}
	p.server = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Connection", "close")
			http.Error(w, "offline Markdown networking is disabled", http.StatusForbidden)
		}),
		ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 16 << 10,
	}
	go func() { _ = p.server.Serve(listener) }()
	go func() {
		select {
		case <-ctx.Done():
			p.close()
		case <-p.done:
		}
	}()
	return p, nil
}

func (p *markdownDenyProxy) close() {
	p.once.Do(func() { _ = p.server.Close(); close(p.done) })
}

type markdownBrowser struct {
	ctx             context.Context
	cancel          context.CancelFunc
	allocatorCancel context.CancelFunc
	targetCancel    context.CancelFunc
	proxy           *markdownDenyProxy
	workspace       string
	workspaceDone   chan struct{}
	images          map[string]struct{}
	mu              sync.Mutex
	violation       error
}

func newMarkdownBrowser(parent context.Context, images map[string]struct{}) (*markdownBrowser, error) {
	ctx, cancel := context.WithTimeout(parent, markdownRenderTimeout)
	b := &markdownBrowser{cancel: cancel, images: images}
	proxy, err := newMarkdownDenyProxy(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	b.proxy = proxy
	// Chrome also creates auxiliary sockets outside user-data-dir. Confine
	// those to the same private workspace and remove it after process exit.
	b.workspace, err = os.MkdirTemp("", "md-browser-")
	if err != nil {
		b.close()
		return nil, err
	}
	b.workspaceDone = make(chan struct{})
	go b.watchWorkspace(ctx)
	options := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox, chromedp.DisableGPU,
		chromedp.UserDataDir(filepath.Join(b.workspace, "profile")),
		chromedp.Env("TMPDIR="+b.workspace, "TMP="+b.workspace, "TEMP="+b.workspace),
		chromedp.Flag("proxy-server", proxy.url),
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"),
		chromedp.Flag("disable-quic", true),
		chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("disk-cache-size", "1"), chromedp.Flag("media-cache-size", "1"),
		chromedp.Flag("disable-application-cache", true),
	)
	allocCtx, allocCancel := process.NewHardenedExecAllocator(ctx, options...)
	b.allocatorCancel = allocCancel
	b.ctx, b.targetCancel = chromedp.NewContext(allocCtx)
	// Register before initializing the target or inserting document content.
	chromedp.ListenTarget(b.ctx, b.handleEvent)
	err = b.run(
		network.Enable(), page.Enable(), audits.Enable(),
		network.SetCacheDisabled(true), network.SetBypassServiceWorker(true),
		emulation.SetScriptExecutionDisabled(true),
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}),
	)
	if err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}

func (b *markdownBrowser) fail(reason string) {
	b.mu.Lock()
	if b.violation == nil {
		b.violation = markdownPolicyError(reason)
	}
	b.mu.Unlock()
	b.cancel()
}

func (b *markdownBrowser) policyError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.violation
}

func (b *markdownBrowser) permitsImage(raw string) bool {
	_, ok := b.images[raw]
	return ok
}

func (b *markdownBrowser) handleEvent(event any) {
	switch e := event.(type) {
	case *fetch.EventRequestPaused:
		if e.ResourceType == network.ResourceTypeImage && b.permitsImage(e.Request.URL) {
			// CDP listener callbacks cannot issue synchronous CDP commands.
			go func() {
				err := chromedp.Run(b.ctx, fetch.ContinueRequest(e.RequestID))
				if err != nil && b.ctx.Err() == nil {
					b.fail("could not authorize validated image")
				}
			}()
		} else {
			b.fail("browser attempted a forbidden resource or navigation")
		}
	case *network.EventRequestWillBeSent:
		if !(e.Type == network.ResourceTypeImage && b.permitsImage(e.Request.URL)) && e.Request.URL != "about:blank" {
			b.fail("browser attempted an unvalidated resource")
		}
	case *network.EventLoadingFailed:
		if e.BlockedReason != "" {
			b.fail("browser blocked a document resource")
		}
	case *network.EventWebSocketCreated:
		b.fail("WebSockets are forbidden")
	case *audits.EventIssueAdded:
		if e.Issue != nil && e.Issue.Code == audits.InspectorIssueCodeContentSecurityPolicyIssue {
			b.fail("content security policy rejected a document operation")
		}
	case *page.EventFrameStartedNavigating:
		if e.URL != "about:blank" {
			b.fail("browser navigation is forbidden")
		}
	case *page.EventFrameNavigated:
		if e.Frame.URL != "about:blank" {
			b.fail("browser left the in-memory document")
		}
	case *page.EventWindowOpen:
		b.fail("new browser windows are forbidden")
	}
}

func (b *markdownBrowser) run(actions ...chromedp.Action) error {
	if err := b.policyError(); err != nil {
		return err
	}
	if err := b.checkWorkspace(); err != nil {
		b.fail("browser workspace exceeds its budget or cannot be verified")
		return b.policyError()
	}
	err := chromedp.Run(b.ctx, actions...)
	if policyErr := b.policyError(); policyErr != nil {
		return policyErr
	}
	if err == nil {
		if budgetErr := b.checkWorkspace(); budgetErr != nil {
			b.fail("browser workspace exceeds its budget or cannot be verified")
			return b.policyError()
		}
	}
	return err
}

const markdownMaxWorkspaceBytes int64 = 64 << 20

// This is a fail-closed observation threshold, not an OS disk quota. Check at
// phase boundaries as well as during long CDP calls; transient peak usage can
// exceed the threshold until the next observation and process-group teardown.
func (b *markdownBrowser) checkWorkspace() error {
	var size int64
	entries := 0
	return filepath.WalkDir(b.workspace, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			if path == b.workspace {
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		entries++
		if entries > 4096 {
			return markdownPolicyError("browser workspace exceeds 4096 entries")
		}
		// WalkDir does not follow symlinks, including Chrome's socket links.
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			size += info.Size()
			if size > markdownMaxWorkspaceBytes {
				return markdownPolicyError("browser workspace exceeds 64 MiB")
			}
		}
		return nil
	})
}

func (b *markdownBrowser) watchWorkspace(ctx context.Context) {
	defer close(b.workspaceDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.checkWorkspace(); err != nil && ctx.Err() == nil {
				b.fail("browser workspace exceeds its budget or cannot be verified")
				return
			}
		}
	}
}

func (b *markdownBrowser) setDocument(document string) error {
	// The caller validates all final HTML before starting the browser. The
	// browser still enforces denial independently if a future caller errs.
	return b.run(
		emulation.SetDeviceMetricsOverride(1200, 950, 1, false),
		// An explicit controlled blank navigation also initializes chromedp's
		// frame/DOM tracking before Page.setDocumentContent replaces the DOM.
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			if tree.Frame.URL != "about:blank" {
				return markdownPolicyError("expected a fresh blank document")
			}
			return page.SetDocumentContent(tree.Frame.ID, document).Do(ctx)
		}),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Poll(`Array.from(document.images).every(image => image.complete)`, nil, chromedp.WithPollingInterval(20*time.Millisecond)),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var decoded bool
			if err := chromedp.Evaluate(`Array.from(document.images).every(image => image.naturalWidth > 0 && image.naturalHeight > 0)`, &decoded).Do(ctx); err != nil {
				return err
			}
			if !decoded {
				b.fail("browser could not decode a validated image")
				return b.policyError()
			}
			return nil
		}),
	)
}

func (b *markdownBrowser) printPDF(writer io.Writer, options PrintOptions) error {
	return b.run(chromedp.ActionFunc(func(ctx context.Context) error {
		if err := emulation.SetEmulatedMedia().WithMedia("screen").Do(ctx); err != nil {
			return err
		}
		width, height := 8.27, 11.69
		switch strings.ToLower(options.PaperSize) {
		case "letter":
			width, height = 8.5, 11
		case "legal":
			width, height = 8.5, 14
		}
		// Keep existing portrait/options semantics. Stream instead of receiving
		// an unbounded complete base64 PDF response in application memory.
		_, stream, err := page.PrintToPDF().WithPrintBackground(true).WithLandscape(false).
			WithPaperWidth(width).WithPaperHeight(height).WithMarginTop(options.MarginTop).
			WithMarginBottom(options.MarginBottom).WithMarginLeft(options.MarginLeft).
			WithMarginRight(options.MarginRight).WithPreferCSSPageSize(false).
			WithTransferMode(page.PrintToPDFTransferModeReturnAsStream).Do(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = cdpio.Close(stream).Do(ctx) }()
		if err := b.checkWorkspace(); err != nil {
			b.fail("browser PDF spool exceeds its workspace budget")
			return b.policyError()
		}
		total := 0
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			// cdproto.IO.read's convenience Do discards base64Encoded. Preserve
			// that flag explicitly; arbitrary PDF bytes are usually base64.
			var chunk struct {
				Base64 bool   `json:"base64Encoded"`
				Data   string `json:"data"`
				EOF    bool   `json:"eof"`
			}
			if err := cdp.Execute(ctx, cdpio.CommandRead, cdpio.Read(stream).WithSize(64<<10), &chunk); err != nil {
				return err
			}
			data := []byte(chunk.Data)
			if chunk.Base64 {
				data, err = base64.StdEncoding.DecodeString(chunk.Data)
				if err != nil {
					return err
				}
			}
			total, err = writeMarkdownPDFChunk(writer, data, total)
			if err != nil {
				return err
			}
			if chunk.EOF {
				break
			}
			if len(data) == 0 {
				return fmt.Errorf("browser returned an empty PDF stream chunk")
			}
		}
		return b.policyError()
	}))
}

func writeMarkdownPDFChunk(writer io.Writer, data []byte, total int) (int, error) {
	if len(data) > markdownMaxPDFBytes-total {
		return total, markdownPolicyError("PDF exceeds 32 MiB")
	}
	if total == 0 && !strings.HasPrefix(string(data), "%PDF-") {
		return total, fmt.Errorf("browser returned an invalid PDF stream")
	}
	written, err := writer.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return total + written, err
}

func (b *markdownBrowser) close() {
	b.cancel()
	if b.workspaceDone != nil {
		<-b.workspaceDone
	}
	if b.targetCancel != nil {
		b.targetCancel()
	}
	if b.allocatorCancel != nil {
		b.allocatorCancel()
	}
	if b.proxy != nil {
		b.proxy.close()
	}
	if b.workspace != "" {
		_ = os.RemoveAll(b.workspace)
	}
}

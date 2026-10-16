package conversion

import (
	"crypto/sha256"
	"encoding/base64"
)

// Existing Markdown layout, with static escaped code instead of remote Prism.
const markdownStyles = `
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
				width: 100%;
			}
			@media print {
				html, body { background-color: #1e1e24 !important; color: #e3e3e6 !important; }
				pre { page-break-inside: avoid; }
			}
			.content-wrapper {
				width: 100%;
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

			pre {
				background-color: #18181c !important;
				padding: 1.25rem;
				border-radius: 0.75rem;
				overflow-x: auto;
				margin-bottom: 1.5rem;
				border: 1px solid #2d2d34;
				width: auto !important;
				max-width: 100% !important;
			}
			pre code { background-color: transparent !important; padding: 0; color: #e3e3e6; font-size: 0.875rem; border: none; }
			ul { list-style-type: disc; padding-left: 1.5rem; margin-bottom: 1.25rem; color: #cbcbd0; }
			ol { list-style-type: decimal; padding-left: 1.5rem; margin-bottom: 1.25rem; color: #cbcbd0; }
			li { margin-bottom: 0.5rem; }
			blockquote { border-left: 4px solid #6366f1; padding-left: 1rem; font-style: italic; color: #9ca3af; margin-bottom: 1.5rem; margin-top: 0; }
		`

var markdownCSP = func() string {
	digest := sha256.Sum256([]byte(markdownStyles))
	return "default-src 'none'; script-src 'none'; connect-src 'none'; img-src data:; style-src 'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'; font-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; media-src 'none'; base-uri 'none'; form-action 'none'"
}()

func markdownHTML(fragment string) string {
	return `<!DOCTYPE html><html><head><meta charset="UTF-8"><meta http-equiv="Content-Security-Policy" content="` + markdownCSP + `"><title>Markdown Export</title><style>` + markdownStyles + `</style></head><body><div class="content-wrapper">` + fragment + `</div></body></html>`
}

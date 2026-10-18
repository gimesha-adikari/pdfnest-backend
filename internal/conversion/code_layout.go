package conversion

import (
	"crypto/sha256"
	"encoding/base64"
	"html"
)

// Preserve the Code export layout; token colors are produced locally.
const codeStyles = `
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
				width: 100%;
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
				width: 100%;
				max-w: 100%;
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
				width: 100% !important;
				max-w: 100% !important;
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
 .token-comment { color: #999; }
 .token-keyword, .token-operator { color: #cc99cd; }
 .token-string { color: #7ec699; }
 .token-number { color: #f08d49; }
 .token-name { color: #f8c555; }
`

var codeCSP = func() string {
	digest := sha256.Sum256([]byte(codeStyles))
	return "default-src 'none'; script-src 'none'; connect-src 'none'; img-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'; font-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; media-src 'none'; base-uri 'none'; form-action 'none'"
}()

func codeHTML(language, fragment string) string {
	return `<!DOCTYPE html><html><head><meta charset="UTF-8"><meta http-equiv="Content-Security-Policy" content="` + html.EscapeString(codeCSP) + `"><title>Code Export</title><style>` + codeStyles + `</style></head><body><div class="code-wrapper"><pre class="` + html.EscapeString("language-"+language) + `"><code>` + fragment + `</code></pre></div></body></html>`
}

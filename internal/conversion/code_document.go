package conversion

import (
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	htmlparser "golang.org/x/net/html"
)

// Keep the existing HTTP request ceiling. HTML, PDF, browser workspace and
// execution remain subject to the shared offline-renderer budgets.
const codeMaxSourceBytes = 100 << 20

// Only these canonical identifiers may reach either a lexer or an attribute.
// Never use lexer filename guessing or content analysis on caller input.
var codeLanguages = map[string]string{
	"go": "go", "py": "python", "pyw": "python", "js": "javascript", "mjs": "javascript", "cjs": "javascript", "jsx": "jsx",
	"ts": "typescript", "tsx": "tsx", "java": "java", "c": "c", "h": "c", "cc": "cpp", "cpp": "cpp", "cxx": "cpp", "hpp": "cpp", "cs": "csharp",
	"rs": "rust", "rb": "ruby", "php": "php", "swift": "swift", "kt": "kotlin", "kts": "kotlin", "scala": "scala", "dart": "dart",
	"html": "html", "htm": "html", "xml": "xml", "svg": "xml", "css": "css", "scss": "scss", "sass": "sass", "less": "css",
	"json": "json", "yaml": "yaml", "yml": "yaml", "toml": "toml", "ini": "ini", "sql": "sql", "sh": "bash", "bash": "bash", "zsh": "bash",
	"ps1": "powershell", "bat": "batch", "cmd": "batch", "r": "r", "lua": "lua", "pl": "perl", "pm": "perl", "ex": "elixir", "exs": "elixir",
	"erl": "erlang", "hrl": "erlang", "hs": "haskell", "lhs": "haskell", "clj": "clojure", "cljs": "clojure", "lisp": "common-lisp", "jl": "julia",
	"md": "markdown", "markdown": "markdown", "tex": "latex", "diff": "diff", "patch": "diff", "vue": "vue", "svelte": "svelte",
}

func readCodeSource(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > codeMaxSourceBytes {
		return nil, fmt.Errorf("code source must be a regular file no larger than 100 MiB")
	}
	source, err := io.ReadAll(io.LimitReader(file, codeMaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(source) > codeMaxSourceBytes {
		return nil, fmt.Errorf("code source exceeds 100 MiB")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return source, nil
}

func prepareCodeDocument(ctx context.Context, source []byte, filename string) (document string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(source) > codeMaxSourceBytes {
		return "", fmt.Errorf("code source exceeds 100 MiB")
	}
	// Escaping cannot shrink source text; reject impossible HTML budgets before
	// tokenization or allocating expanded escaped output.
	if len(source) > markdownMaxHTMLBytes {
		return "", fmt.Errorf("code document exceeds 16 MiB")
	}
	language := codeLanguages[strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))]
	if language == "" {
		language = "plaintext"
	}
	fragment, err := runCodeHighlighter(ctx, source, language)
	if err != nil {
		return "", err
	}
	document = codeHTML(language, fragment)
	if len(document) > markdownMaxHTMLBytes {
		return "", fmt.Errorf("code document exceeds 16 MiB")
	}
	if err := validateCodeHTML(ctx, document); err != nil {
		return "", err
	}
	return document, nil
}

// Emit only fixed token classes and escaped text, never lexer-supplied HTML.
// Recover lexer errors as failed conversions, without an unvalidated fallback.
func highlightCode(ctx context.Context, source, language string) (fragment string, err error) {
	defer func() {
		if recover() != nil {
			fragment = ""
			err = fmt.Errorf("code highlighting failed")
		}
	}()
	if language == "plaintext" {
		return html.EscapeString(source), nil
	}
	lexer := lexers.Get(language)
	if lexer == nil {
		return "", fmt.Errorf("missing bundled code lexer %q", language)
	}
	iterator, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", Nested: true}, source)
	if err != nil {
		return "", fmt.Errorf("tokenizing code: %w", err)
	}
	var output strings.Builder
	for token := iterator(); token != chroma.EOF; token = iterator() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		class := ""
		switch {
		case token.Type.InCategory(chroma.Comment):
			class = "token-comment"
		case token.Type.InCategory(chroma.Keyword):
			class = "token-keyword"
		case token.Type.InSubCategory(chroma.LiteralString):
			class = "token-string"
		case token.Type.InSubCategory(chroma.LiteralNumber):
			class = "token-number"
		case token.Type.InCategory(chroma.Operator):
			class = "token-operator"
		case token.Type.InCategory(chroma.Name):
			class = "token-name"
		}
		text := html.EscapeString(token.Value)
		if class != "" {
			output.WriteString(`<span class="` + html.EscapeString(class) + `">` + text + `</span>`)
		} else {
			output.WriteString(text)
		}
		if output.Len() > markdownMaxHTMLBytes {
			return "", fmt.Errorf("highlighted code exceeds 16 MiB")
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return output.String(), nil
}

// Validate the final document independently of the generator. Markdown's HTML
// acceptance policy stays unchanged; Code permits only static token spans.
func validateCodeHTML(ctx context.Context, document string) error {
	root, err := htmlparser.Parse(strings.NewReader(document))
	if err != nil {
		return err
	}
	counts := map[string]int{}
	var walk func(*htmlparser.Node) error
	walk = func(n *htmlparser.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.Type == htmlparser.ElementNode {
			counts[n.Data]++
			switch n.Data {
			case "html", "head", "body", "title", "code":
				if len(n.Attr) != 0 {
					return fmt.Errorf("unexpected code document attribute")
				}
			case "meta":
				charset := len(n.Attr) == 1 && n.Attr[0].Key == "charset" && n.Attr[0].Val == "UTF-8"
				csp := len(n.Attr) == 2 && n.Attr[0].Key == "http-equiv" && n.Attr[0].Val == "Content-Security-Policy" && n.Attr[1].Key == "content" && n.Attr[1].Val == codeCSP
				if !charset && !csp {
					return fmt.Errorf("unexpected code metadata")
				}
			case "style":
				if len(n.Attr) != 0 || n.FirstChild == nil || n.FirstChild.Type != htmlparser.TextNode || n.FirstChild.Data != codeStyles || n.FirstChild.NextSibling != nil {
					return fmt.Errorf("unexpected code stylesheet")
				}
			case "div", "pre", "span":
				if len(n.Attr) != 1 || n.Attr[0].Key != "class" {
					return fmt.Errorf("unexpected code layout attribute")
				}
				class := n.Attr[0].Val
				allowed := n.Data == "div" && class == "code-wrapper"
				if n.Data == "pre" {
					allowed = class == "language-plaintext"
					for _, language := range codeLanguages {
						allowed = allowed || class == "language-"+language
					}
				}
				if n.Data == "span" {
					switch class {
					case "token-comment", "token-keyword", "token-string", "token-number", "token-operator", "token-name":
						allowed = true
					}
				}
				if !allowed {
					return fmt.Errorf("unexpected code layout class")
				}
			default:
				return fmt.Errorf("forbidden code element %q", n.Data)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return err
	}
	for _, element := range []string{"html", "head", "body", "title", "style", "div", "pre", "code"} {
		if counts[element] != 1 {
			return fmt.Errorf("missing or duplicate code document %s", element)
		}
	}
	if counts["meta"] != 2 {
		return fmt.Errorf("missing or duplicate code document metadata")
	}
	return nil
}

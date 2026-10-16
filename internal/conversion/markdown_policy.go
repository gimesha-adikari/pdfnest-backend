package conversion

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
)

const (
	markdownMaxSourceBytes       = 2 << 20
	markdownMaxImages            = 32
	markdownMaxEncodedImageBytes = 1 << 20
	markdownMaxDecodedImageBytes = 768 << 10
	markdownMaxImageDimension    = 4096
	markdownMaxImagePixels       = 4_000_000
	markdownMaxAggregatePixels   = 16_000_000
	markdownMaxHTMLBytes         = 16 << 20
	markdownMaxPDFBytes          = 32 << 20
)

// ErrMarkdownPolicy is returned before publishing output when an offline
// resource or resource-budget requirement cannot be satisfied.
var ErrMarkdownPolicy = errors.New("markdown resource policy violation")

func markdownPolicyError(reason string) error {
	return fmt.Errorf("%w: %s", ErrMarkdownPolicy, reason)
}

// The async caller checks size before copying the upload or reserving a task.
// The renderer repeats this check on its opened file and still bounds its read.
func checkMarkdownSourceFile(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	return checkMarkdownSourceInfo(stat)
}

func checkMarkdownSourceInfo(stat os.FileInfo) error {
	if !stat.Mode().IsRegular() || stat.Size() > markdownMaxSourceBytes {
		return markdownPolicyError("source must be a regular file no larger than 2 MiB")
	}
	return nil
}

type markdownDocument struct {
	html   string
	images map[string]struct{}
}

func readMarkdownSource(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkMarkdownSourceInfo(stat); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, markdownMaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > markdownMaxSourceBytes {
		return nil, markdownPolicyError("source exceeds 2 MiB")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func prepareMarkdownDocument(ctx context.Context, source []byte) (markdownDocument, error) {
	if err := ctx.Err(); err != nil {
		return markdownDocument{}, err
	}
	if len(source) > markdownMaxSourceBytes {
		return markdownDocument{}, markdownPolicyError("source exceeds 2 MiB")
	}
	// Keep default Goldmark parsing: no unsafe HTML, attributes or new extensions.
	md := goldmark.New()
	root := md.Parser().Parse(text.NewReader(source))
	images := make(map[string]struct{})
	count, pixels := 0, 0
	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if err := ctx.Err(); err != nil {
			return ast.WalkStop, err
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.RawHTML:
			if markdownHTMLCommentsOnly(n.Segments.Value(source)) {
				return ast.WalkContinue, nil
			}
			return ast.WalkStop, markdownPolicyError("raw HTML is unsupported in offline Markdown")
		case *ast.HTMLBlock:
			raw := n.Lines().Value(source)
			if n.HasClosure() {
				raw = append(raw, n.ClosureLine.Value(source)...)
			}
			if markdownHTMLCommentsOnly(raw) {
				return ast.WalkContinue, nil
			}
			// Reject rather than silently omit resource-bearing raw HTML. Raw HTML
			// has never been an enabled Markdown rendering feature here. Harmless
			// comments above retain Goldmark's existing omission behavior.
			return ast.WalkStop, markdownPolicyError("raw HTML is unsupported in offline Markdown")
		case *ast.Image:
			count++
			if count > markdownMaxImages {
				return ast.WalkStop, markdownPolicyError("more than 32 embedded images")
			}
			// This is the same normalization Goldmark applies at the HTML sink,
			// including escaped punctuation and HTML character references.
			destination := string(util.URLEscape(n.Destination, true))
			canonical, imagePixels, err := validateMarkdownImage(ctx, destination, markdownMaxAggregatePixels-pixels)
			if err != nil {
				return ast.WalkStop, err
			}
			pixels += imagePixels
			n.Destination = []byte(canonical)
			images[canonical] = struct{}{}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return markdownDocument{}, err
	}
	fragment := &markdownBoundedBuffer{ctx: ctx, limit: markdownMaxHTMLBytes}
	if err := md.Renderer().Render(fragment, source, root); err != nil {
		return markdownDocument{}, err
	}
	document := markdownHTML(fragment.String())
	if len(document) > markdownMaxHTMLBytes {
		return markdownDocument{}, markdownPolicyError("generated HTML exceeds 16 MiB")
	}
	if err := validateMarkdownHTML(document, images); err != nil {
		return markdownDocument{}, err
	}
	return markdownDocument{html: document, images: images}, ctx.Err()
}

func markdownHTMLCommentsOnly(raw []byte) bool {
	tokenizer := html.NewTokenizer(bytes.NewReader(raw))
	comment := false
	for {
		switch tokenizer.Next() {
		case html.CommentToken:
			comment = true
		case html.TextToken:
			if len(bytes.TrimSpace(tokenizer.Text())) != 0 {
				return false
			}
		case html.ErrorToken:
			return comment && tokenizer.Err() == io.EOF
		default:
			return false
		}
	}
}

func validateMarkdownImage(ctx context.Context, destination string, remainingPixels int) (string, int, error) {
	var format, payload string
	switch {
	case strings.HasPrefix(destination, "data:image/png;base64,"):
		format, payload = "png", strings.TrimPrefix(destination, "data:image/png;base64,")
	case strings.HasPrefix(destination, "data:image/jpeg;base64,"):
		format, payload = "jpeg", strings.TrimPrefix(destination, "data:image/jpeg;base64,")
	default:
		return "", 0, markdownPolicyError("images must be embedded PNG/JPEG base64 data; remote and filesystem resources are forbidden")
	}
	if len(payload) == 0 || len(payload) > markdownMaxEncodedImageBytes {
		return "", 0, markdownPolicyError("encoded image exceeds 1 MiB or is empty")
	}
	// DecodeString ignores CR/LF even in strict mode. Reject them and every
	// alternate URL encoding explicitly before allocating the decoded payload.
	for _, c := range []byte(payload) {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return "", 0, markdownPolicyError("malformed image base64")
		}
	}
	decodedSize := base64.StdEncoding.DecodedLen(len(payload))
	if strings.HasSuffix(payload, "==") {
		decodedSize -= 2
	} else if strings.HasSuffix(payload, "=") {
		decodedSize--
	}
	if decodedSize <= 0 || decodedSize > markdownMaxDecodedImageBytes {
		return "", 0, markdownPolicyError("decoded image exceeds 768 KiB or is empty")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		return "", 0, markdownPolicyError("malformed image base64")
	}
	var config image.Config
	if format == "png" {
		config, err = png.DecodeConfig(bytes.NewReader(raw))
	} else {
		config, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	}
	if err != nil {
		return "", 0, markdownPolicyError("invalid image format or header")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > markdownMaxImageDimension || config.Height > markdownMaxImageDimension || config.Width*config.Height > markdownMaxImagePixels {
		return "", 0, markdownPolicyError("image dimensions exceed 4096 per axis or 4 million pixels")
	}
	pixels := config.Width * config.Height
	if pixels > remainingPixels {
		return "", 0, markdownPolicyError("aggregate image pixels exceed 16 million")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	var raster image.Image
	if format == "png" {
		raster, err = png.Decode(bytes.NewReader(raw))
	} else {
		raster, err = jpeg.Decode(bytes.NewReader(raw))
	}
	if err != nil {
		return "", 0, markdownPolicyError("image does not fully decode")
	}
	if format == "jpeg" {
		// JPEG has no animation frames. Preserve its validated bytes, including
		// EXIF orientation and color metadata, without a lossy second encoding.
		return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw), pixels, ctx.Err()
	}
	// Normalize PNG to a static raster; discard animation chunks, metadata
	// and trailing bytes instead of passing them to a second image parser.
	canonical := &markdownBoundedBuffer{ctx: ctx, limit: markdownMaxDecodedImageBytes}
	err = png.Encode(canonical, raster)
	if err != nil {
		return "", 0, fmt.Errorf("canonical image: %w", err)
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(canonical.Bytes()), pixels, nil
}

type markdownBoundedBuffer struct {
	bytes.Buffer
	ctx   context.Context
	limit int
}

func (b *markdownBoundedBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.limit-b.Len() {
		return 0, markdownPolicyError("generated resource exceeds byte budget")
	}
	return b.Buffer.Write(p)
}

func (b *markdownBoundedBuffer) WriteString(s string) (int, error) {
	return b.Write([]byte(s))
}

func (b *markdownBoundedBuffer) WriteByte(c byte) error {
	_, err := b.Write([]byte{c})
	return err
}

// Validate the actual final HTML independently of the Markdown AST. No user
// styles, event handlers, srcset/ping, frames, SVG, scripts or external assets
// may be introduced by a renderer change or future parser configuration.
func validateMarkdownHTML(document string, images map[string]struct{}) error {
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return markdownPolicyError("invalid generated HTML")
	}
	var visit func(*html.Node) error
	visit = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			allowed := " html head body title div h1 h2 h3 h4 h5 h6 p ul ol li blockquote pre code em strong hr br a img style meta "
			if n.Namespace != "" || !strings.Contains(allowed, " "+n.Data+" ") {
				return markdownPolicyError("unsupported generated HTML element")
			}
			for _, a := range n.Attr {
				ok := false
				if a.Namespace != "" {
					return markdownPolicyError("unsupported generated HTML namespace")
				}
				switch n.Data {
				case "div":
					ok = a.Key == "class" && a.Val == "content-wrapper"
				case "code":
					ok = a.Key == "class" && strings.HasPrefix(a.Val, "language-")
				case "ol":
					ok = a.Key == "start"
				case "a":
					ok = a.Key == "href" || a.Key == "title"
				case "img":
					ok = a.Key == "alt" || a.Key == "title"
					if a.Key == "src" {
						_, ok = images[a.Val]
					}
				case "meta":
					ok = a.Key == "charset" && a.Val == "UTF-8" || a.Key == "http-equiv" && a.Val == "Content-Security-Policy" || a.Key == "content" && a.Val == markdownCSP
				}
				if !ok {
					return markdownPolicyError("unsupported generated HTML attribute or image")
				}
			}
			if n.Data == "img" {
				hasSource := false
				for _, a := range n.Attr {
					if a.Key == "src" {
						hasSource = true
					}
				}
				if !hasSource {
					return markdownPolicyError("generated image has no validated source")
				}
			}
			if n.Data == "style" && (n.FirstChild == nil || n.FirstChild.Type != html.TextNode || n.FirstChild.Data != markdownStyles || n.FirstChild.NextSibling != nil) {
				return markdownPolicyError("untrusted generated stylesheet")
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root)
}

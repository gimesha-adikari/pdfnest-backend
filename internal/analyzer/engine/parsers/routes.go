package parsers

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

// RouteExtractor extracts static REST/RPC routes from supported framework source files.
type RouteExtractor struct{}

// NewRouteExtractor creates a new route extractor instance.
func NewRouteExtractor() *RouteExtractor {
	return &RouteExtractor{}
}

// ExtractRoutes inspects repository files for Next.js, Express, FastAPI, Flask, Fiber, and Gin route definitions.
func (e *RouteExtractor) ExtractRoutes(
	rootDir string,
	inv *inventory.ScopeInventory,
) []engine.ApiRouteItem {
	routes := make([]engine.ApiRouteItem, 0, 64)

	// Regexp definitions for supported frameworks
	reExpress := regexp.MustCompile(`(?:app|router|api|v\d+)\.(get|post|put|delete|patch|options|head)\s*\(\s*["']([^"']+)["'](?:\s*,\s*([a-zA-Z0-9_]+))?`)
	reFastAPI := regexp.MustCompile(`@(?:app|router|api)\.(get|post|put|delete|patch)\s*\(\s*["']([^"']+)["']`)
	reFlask := regexp.MustCompile(`@(?:app|blueprint|bp)\.route\s*\(\s*["']([^"']+)["'](?:.*methods=\[([^\]]+)\])?`)
	reFiber := regexp.MustCompile(`(?:app|group|api|v\d+|router)\.(Get|Post|Put|Delete|Patch|Options|Head)\s*\(\s*["']([^"']+)["'](?:\s*,\s*([a-zA-Z0-9_.]+))?`)
	reGin := regexp.MustCompile(`(?:r|router|group|api|v\d+)\.(GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD)\s*\(\s*["']([^"']+)["'](?:\s*,\s*([a-zA-Z0-9_.]+))?`)
	rePyDef := regexp.MustCompile(`(?:async\s+)?def\s+([a-zA-Z0-9_]+)\s*\(`)

	for _, file := range inv.Files {
		if file.IsExcluded || file.Category != inventory.CategorySource {
			continue
		}
		if file.Size > 512*1024 {
			continue
		}

		normPath := strings.ToLower(file.RelPath)
		absPath := filepath.Join(rootDir, file.RelPath)

		// 1. Next.js App Router (app/**/route.ts, app/**/route.js)
		if strings.HasSuffix(normPath, "/route.ts") || strings.HasSuffix(normPath, "/route.js") ||
			strings.HasSuffix(normPath, "/route.tsx") || strings.HasSuffix(normPath, "/route.jsx") {
			extracted := parseNextAppRoute(absPath, file.RelPath)
			routes = append(routes, extracted...)
			continue
		}

		// 2. Next.js Pages Router (pages/api/**/*.ts, pages/api/**/*.js)
		if strings.Contains(normPath, "pages/api/") {
			extracted := parseNextPagesRoute(absPath, file.RelPath)
			routes = append(routes, extracted...)
			continue
		}

		// Scan standard backend files
		content, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(bytes.NewReader(content))
		lineIdx := 0
		var pendingDecoratorMethod string
		var pendingDecoratorPath string
		var pendingDecoratorLine int

		for scanner.Scan() {
			lineIdx++
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
				continue
			}

			// Python Function Handler Attachment for @app.get(...)
			if pendingDecoratorMethod != "" {
				if m := rePyDef.FindStringSubmatch(line); len(m) == 2 {
					handler := m[1]
					routes = append(routes, engine.ApiRouteItem{
						Method:          pendingDecoratorMethod,
						Path:            pendingDecoratorPath,
						SourceFile:      file.RelPath,
						LineNumber:      &pendingDecoratorLine,
						InferredHandler: &handler,
						AuthRequired:    checkAuthPresence(line),
					})
					pendingDecoratorMethod = ""
					pendingDecoratorPath = ""
					continue
				}
			}

			// Express
			if m := reExpress.FindStringSubmatch(line); len(m) >= 3 {
				lineNum := lineIdx
				method := strings.ToUpper(m[1])
				path := m[2]
				var handler *string
				if len(m) >= 4 && m[3] != "" {
					h := m[3]
					handler = &h
				}
				routes = append(routes, engine.ApiRouteItem{
					Method:          method,
					Path:            path,
					SourceFile:      file.RelPath,
					LineNumber:      &lineNum,
					InferredHandler: handler,
					AuthRequired:    checkAuthPresence(line),
				})
				continue
			}

			// FastAPI
			if m := reFastAPI.FindStringSubmatch(line); len(m) == 3 {
				pendingDecoratorMethod = strings.ToUpper(m[1])
				pendingDecoratorPath = m[2]
				pendingDecoratorLine = lineIdx
				continue
			}

			// Flask
			if m := reFlask.FindStringSubmatch(line); len(m) >= 2 {
				methods := []string{"GET"}
				if len(m) >= 3 && m[2] != "" {
					methods = parseFlaskMethods(m[2])
				}
				for _, method := range methods {
					pendingDecoratorMethod = method
					pendingDecoratorPath = m[1]
					pendingDecoratorLine = lineIdx
				}
				continue
			}

			// Fiber
			if m := reFiber.FindStringSubmatch(line); len(m) >= 3 {
				lineNum := lineIdx
				method := strings.ToUpper(m[1])
				path := m[2]
				var handler *string
				if len(m) >= 4 && m[3] != "" {
					h := m[3]
					handler = &h
				}
				routes = append(routes, engine.ApiRouteItem{
					Method:          method,
					Path:            path,
					SourceFile:      file.RelPath,
					LineNumber:      &lineNum,
					InferredHandler: handler,
					AuthRequired:    checkAuthPresence(line),
				})
				continue
			}

			// Gin
			if m := reGin.FindStringSubmatch(line); len(m) >= 3 {
				lineNum := lineIdx
				method := strings.ToUpper(m[1])
				path := m[2]
				var handler *string
				if len(m) >= 4 && m[3] != "" {
					h := m[3]
					handler = &h
				}
				routes = append(routes, engine.ApiRouteItem{
					Method:          method,
					Path:            path,
					SourceFile:      file.RelPath,
					LineNumber:      &lineNum,
					InferredHandler: handler,
					AuthRequired:    checkAuthPresence(line),
				})
				continue
			}
		}
	}

	// Guarantee deterministic sort order by method then path
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})

	return routes
}

func parseNextAppRoute(absPath string, relPath string) []engine.ApiRouteItem {
	results := make([]engine.ApiRouteItem, 0)
	dir := filepath.Dir(relPath)
	routePath := deriveNextPath(dir, "app")

	content, err := os.ReadFile(absPath)
	if err != nil {
		return results
	}

	reNextFn := regexp.MustCompile(`export\s+(?:async\s+)?function\s+(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s*\(`)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineIdx := 0

	for scanner.Scan() {
		lineIdx++
		line := strings.TrimSpace(scanner.Text())
		if m := reNextFn.FindStringSubmatch(line); len(m) == 2 {
			lineNum := lineIdx
			method := m[1]
			handler := method
			results = append(results, engine.ApiRouteItem{
				Method:          method,
				Path:            routePath,
				SourceFile:      relPath,
				LineNumber:      &lineNum,
				InferredHandler: &handler,
				AuthRequired:    checkAuthPresence(line),
			})
		}
	}

	return results
}

func parseNextPagesRoute(absPath string, relPath string) []engine.ApiRouteItem {
	results := make([]engine.ApiRouteItem, 0)
	dir := relPath
	ext := filepath.Ext(dir)
	dir = strings.TrimSuffix(dir, ext)
	routePath := deriveNextPath(dir, "pages")

	handler := "handler"
	lineNum := 1
	results = append(results, engine.ApiRouteItem{
		Method:          "ALL",
		Path:            routePath,
		SourceFile:      relPath,
		LineNumber:      &lineNum,
		InferredHandler: &handler,
		AuthRequired:    false,
	})

	return results
}

func deriveNextPath(relDir string, baseFolder string) string {
	norm := filepath.ToSlash(relDir)
	idx := strings.Index(norm, baseFolder+"/")
	if idx != -1 {
		norm = norm[idx+len(baseFolder):]
	}
	norm = strings.TrimPrefix(norm, "/")

	// Replace [param] with :param
	reParam := regexp.MustCompile(`\[([^\]]+)\]`)
	norm = reParam.ReplaceAllString(norm, ":$1")

	if !strings.HasPrefix(norm, "/") {
		norm = "/" + norm
	}
	return norm
}

func parseFlaskMethods(methodsStr string) []string {
	parts := strings.Split(methodsStr, ",")
	results := make([]string, 0, len(parts))
	for _, p := range parts {
		cleaned := strings.Trim(strings.TrimSpace(p), "\"'")
		if cleaned != "" {
			results = append(results, strings.ToUpper(cleaned))
		}
	}
	if len(results) == 0 {
		results = append(results, "GET")
	}
	return results
}

func checkAuthPresence(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "auth") ||
		strings.Contains(lower, "jwt") ||
		strings.Contains(lower, "protect") ||
		strings.Contains(lower, "require_user") ||
		strings.Contains(lower, "authenticated") ||
		strings.Contains(lower, "bearer")
}

package ast

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
)

var (
	// Imports
	reTSImportESM     = regexp.MustCompile(`(?m)^[ \t]*import\s+(?:(?:[\w*\s{},]+)\s+from\s+)?['"]([^'"]+)['"]`)
	reTSImportRequire = regexp.MustCompile(`(?m)(?:const|let|var)\s+[\w{}\s,:]+\s*=\s*require\(\s*['"]([^'"]+)['"]\s*\)`)

	// Environment variable access
	reTSEnvDot = regexp.MustCompile(`process\.env\.([A-Za-z_][A-Za-z0-9_]*)`)
	reTSEnvIdx = regexp.MustCompile(`process\.env\[['"]([A-Za-z_][A-Za-z0-9_]*)['"]\]`)

	// Express/Fastify/Koa routes
	reTSRouteMethod = regexp.MustCompile(`(?m)(?:app|router|fastify|server|api)\s*\.\s*(get|post|put|delete|patch|options|head)\s*\(\s*['"]([^'"]+)['"]\s*,\s*(?:async\s*)?(?:function\s*([a-zA-Z0-9_]+)?|\(([a-zA-Z0-9_,\s:]*)\)|([a-zA-Z0-9_]+))`)

	// Next.js App Router HTTP handlers
	reNextAppRouterHandler = regexp.MustCompile(`(?m)^[ \t]*export\s+(?:async\s+)?(?:function\s+(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)|const\s+(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s*=)`)

	// Exported functions, classes, interfaces
	reTSExportFunc  = regexp.MustCompile(`(?m)^[ \t]*export\s+(?:async\s+)?function\s+([a-zA-Z0-9_]+)\s*\(([^)]*)\)`)
	reTSExportClass = regexp.MustCompile(`(?m)^[ \t]*export\s+(?:default\s+)?class\s+([a-zA-Z0-9_]+)`)
	reTSExportIface = regexp.MustCompile(`(?m)^[ \t]*export\s+interface\s+([a-zA-Z0-9_]+)`)
	reTSExportType  = regexp.MustCompile(`(?m)^[ \t]*export\s+type\s+([a-zA-Z0-9_]+)`)

	// Prisma schema model regex
	rePrismaModel = regexp.MustCompile(`(?m)^[ \t]*model\s+([a-zA-Z0-9_]+)\s*\{([^}]+)\}`)
)

// parseTSJSFile parses a TypeScript/JavaScript source file and extracts deterministic static AST facts.
func parseTSJSFile(
	ctx context.Context,
	relPath string,
	content []byte,
	limits *ResourceLimits,
) (res *fileASTResult, err error) {
	res = &fileASTResult{}

	// Panic Safety Boundary
	defer func() {
		if r := recover(); r != nil {
			res.Diagnostics = append(res.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "PANIC_RECOVERED",
				Message:    fmt.Sprintf("recovered from TS/JS parser panic: %v", r),
				Severity:   "warning",
			})
			err = nil
		}
	}()

	normPath := filepath.ToSlash(relPath)
	lowerPath := strings.ToLower(normPath)

	// 1. Next.js App Router Route Detection (app/**/route.ts|js)
	if strings.Contains(lowerPath, "app/") && (strings.HasSuffix(lowerPath, "/route.ts") || strings.HasSuffix(lowerPath, "/route.js") || strings.HasSuffix(lowerPath, "/route.tsx") || strings.HasSuffix(lowerPath, "/route.jsx")) {
		inspectNextAppRouter(normPath, content, res)
	}

	// 2. Next.js Pages Router Route Detection (pages/api/**/*.ts|js)
	if strings.Contains(lowerPath, "pages/api/") && (strings.HasSuffix(lowerPath, ".ts") || strings.HasSuffix(lowerPath, ".js") || strings.HasSuffix(lowerPath, ".tsx") || strings.HasSuffix(lowerPath, ".jsx")) {
		inspectNextPagesRouter(normPath, content, res)
	}

	// 3. Line-by-Line Token & AST Scanning
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNum := 0
	var nodeCount int64

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		lineNum++
		nodeCount++
		res.NodesProcessed++

		if nodeCount > limits.MaxNodesPerFile {
			res.Diagnostics = append(res.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "MAX_NODES_EXCEEDED",
				Message:    fmt.Sprintf("node scan ceiling reached at line %d", lineNum),
				Severity:   "info",
			})
			break
		}

		lineText := scanner.Text()

		// Imports (ESM)
		if matches := reTSImportESM.FindStringSubmatch(lineText); len(matches) > 1 {
			impPath := matches[1]
			res.Imports = append(res.Imports, ImportItem{
				SourceFile: relPath,
				ImportPath: impPath,
				LineNumber: lineNum,
			})
			if techName := mapTSImportToTechnology(impPath); techName != "" {
				res.Evidence = append(res.Evidence, engine.EvidenceItem{
					FilePath: relPath,
					RuleType: "source_import",
					Detail:   fmt.Sprintf("import %s", impPath),
					LineNumber: func() *int {
						l := lineNum
						return &l
					}(),
				})
			}
		}

		// Imports (CommonJS require)
		if matches := reTSImportRequire.FindStringSubmatch(lineText); len(matches) > 1 {
			impPath := matches[1]
			res.Imports = append(res.Imports, ImportItem{
				SourceFile: relPath,
				ImportPath: impPath,
				LineNumber: lineNum,
			})
		}

		// Environment Variables (process.env.KEY or process.env['KEY'])
		for _, matches := range reTSEnvDot.FindAllStringSubmatch(lineText, -1) {
			if len(matches) > 1 && matches[1] != "" {
				res.EnvironmentReferences = append(res.EnvironmentReferences, EnvironmentUsage{
					Name:       matches[1],
					SourceFile: relPath,
					LineNumber: lineNum,
					AccessType: "process.env",
				})
			}
		}
		for _, matches := range reTSEnvIdx.FindAllStringSubmatch(lineText, -1) {
			if len(matches) > 1 && matches[1] != "" {
				res.EnvironmentReferences = append(res.EnvironmentReferences, EnvironmentUsage{
					Name:       matches[1],
					SourceFile: relPath,
					LineNumber: lineNum,
					AccessType: "process.env[]",
				})
			}
		}

		// Express/Fastify/Koa routes (app.get("/path", ...))
		if matches := reTSRouteMethod.FindStringSubmatch(lineText); len(matches) > 2 {
			method := strings.ToUpper(matches[1])
			pathVal := matches[2]
			handler := matches[3]
			if handler == "" {
				handler = matches[5]
			}
			res.Routes = append(res.Routes, engine.ApiRouteItem{
				Method:     method,
				Path:       pathVal,
				SourceFile: relPath,
				LineNumber: &lineNum,
				InferredHandler: func() *string {
					if handler != "" {
						return &handler
					}
					return nil
				}(),
				AuthRequired: strings.Contains(strings.ToLower(lineText), "auth") || strings.Contains(strings.ToLower(lineText), "protect"),
			})
		}

		// Exported Functions
		if matches := reTSExportFunc.FindStringSubmatch(lineText); len(matches) > 1 {
			res.Symbols = append(res.Symbols, SymbolItem{
				Name:       matches[1],
				Kind:       SymbolKindFunction,
				SourceFile: relPath,
				LineNumber: lineNum,
				Signature:  "(" + matches[2] + ")",
				Exported:   true,
			})
		}

		// Exported Classes
		if matches := reTSExportClass.FindStringSubmatch(lineText); len(matches) > 1 {
			res.Symbols = append(res.Symbols, SymbolItem{
				Name:       matches[1],
				Kind:       SymbolKindClass,
				SourceFile: relPath,
				LineNumber: lineNum,
				Exported:   true,
			})
		}

		// Exported Interfaces
		if matches := reTSExportIface.FindStringSubmatch(lineText); len(matches) > 1 {
			res.Symbols = append(res.Symbols, SymbolItem{
				Name:       matches[1],
				Kind:       SymbolKindInterface,
				SourceFile: relPath,
				LineNumber: lineNum,
				Exported:   true,
			})
		}

		// Exported Types
		if matches := reTSExportType.FindStringSubmatch(lineText); len(matches) > 1 {
			res.Symbols = append(res.Symbols, SymbolItem{
				Name:       matches[1],
				Kind:       SymbolKindType,
				SourceFile: relPath,
				LineNumber: lineNum,
				Exported:   true,
			})
		}
	}

	// 4. Prisma Schema Model Detection
	if strings.HasSuffix(lowerPath, "schema.prisma") {
		inspectPrismaSchema(relPath, content, res)
	}

	return res, nil
}

func inspectNextAppRouter(relPath string, content []byte, res *fileASTResult) {
	// Convert app/api/users/[id]/route.ts -> /api/users/:id
	idx := strings.Index(relPath, "app/")
	if idx < 0 {
		return
	}
	sub := relPath[idx+4:]
	sub = strings.TrimSuffix(sub, "/route.ts")
	sub = strings.TrimSuffix(sub, "/route.js")
	sub = strings.TrimSuffix(sub, "/route.tsx")
	sub = strings.TrimSuffix(sub, "/route.jsx")

	// Convert [param] to :param
	pathParts := strings.Split(sub, "/")
	for i, p := range pathParts {
		if strings.HasPrefix(p, "[...") && strings.HasSuffix(p, "]") {
			pathParts[i] = "*" + p[4:len(p)-1]
		} else if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") {
			pathParts[i] = ":" + p[1:len(p)-1]
		}
	}
	routePath := "/" + strings.Join(pathParts, "/")

	// Scan for exported methods (GET, POST, PUT, DELETE, PATCH)
	matches := reNextAppRouterHandler.FindAllStringSubmatch(string(content), -1)
	for _, m := range matches {
		method := m[1]
		if method == "" {
			method = m[2]
		}
		if method != "" {
			line := 1
			handler := method
			res.Routes = append(res.Routes, engine.ApiRouteItem{
				Method:          method,
				Path:            routePath,
				SourceFile:      relPath,
				LineNumber:      &line,
				InferredHandler: &handler,
				AuthRequired:    false,
			})
		}
	}
}

func inspectNextPagesRouter(relPath string, content []byte, res *fileASTResult) {
	idx := strings.Index(relPath, "pages/api/")
	if idx < 0 {
		return
	}
	sub := relPath[idx+6:]
	sub = strings.TrimSuffix(sub, ".ts")
	sub = strings.TrimSuffix(sub, ".js")
	sub = strings.TrimSuffix(sub, ".tsx")
	sub = strings.TrimSuffix(sub, ".jsx")

	if strings.HasSuffix(sub, "/index") {
		sub = strings.TrimSuffix(sub, "/index")
	}

	pathParts := strings.Split(sub, "/")
	for i, p := range pathParts {
		if strings.HasPrefix(p, "[...") && strings.HasSuffix(p, "]") {
			pathParts[i] = "*" + p[4:len(p)-1]
		} else if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") {
			pathParts[i] = ":" + p[1:len(p)-1]
		}
	}
	routePath := "/" + strings.Join(pathParts, "/")

	line := 1
	handler := "handler"
	res.Routes = append(res.Routes, engine.ApiRouteItem{
		Method:          "GET",
		Path:            routePath,
		SourceFile:      relPath,
		LineNumber:      &line,
		InferredHandler: &handler,
		AuthRequired:    false,
	})
}

func inspectPrismaSchema(relPath string, content []byte, res *fileASTResult) {
	matches := rePrismaModel.FindAllStringSubmatch(string(content), -1)
	for _, m := range matches {
		if len(m) > 2 {
			modelName := m[1]
			body := m[2]
			model := ModelItem{
				Name:       modelName,
				SourceFile: relPath,
				LineNumber: 1,
				Framework:  "prisma",
			}
			for _, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "@@") {
					continue
				}
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 {
					fieldName := parts[0]
					fieldType := parts[1]
					tag := ""
					if len(parts) > 2 {
						tag = strings.Join(parts[2:], " ")
					}
					model.Fields = append(model.Fields, ModelField{
						Name:     fieldName,
						Type:     fieldType,
						Tag:      tag,
						Required: !strings.HasSuffix(fieldType, "?"),
					})
				}
			}
			res.ModelStructures = append(res.ModelStructures, model)
		}
	}
}

func mapTSImportToTechnology(imp string) string {
	lower := strings.ToLower(imp)
	switch {
	case strings.Contains(lower, "next/"):
		return "Next.js"
	case strings.Contains(lower, "react"):
		return "React"
	case strings.Contains(lower, "express"):
		return "Express"
	case strings.Contains(lower, "fastify"):
		return "Fastify"
	case strings.Contains(lower, "@prisma/client"):
		return "Prisma"
	case strings.Contains(lower, "typeorm"):
		return "TypeORM"
	case strings.Contains(lower, "mongoose"):
		return "MongoDB"
	case strings.Contains(lower, "ioredis") || lower == "redis":
		return "Redis"
	case strings.Contains(lower, "pg") || strings.Contains(lower, "postgres"):
		return "PostgreSQL"
	default:
		return ""
	}
}

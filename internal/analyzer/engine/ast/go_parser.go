package ast

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
)

type fileASTResult struct {
	Routes                []engine.ApiRouteItem
	Symbols               []SymbolItem
	Imports               []ImportItem
	EnvironmentReferences []EnvironmentUsage
	ModelStructures       []ModelItem
	Evidence              []engine.EvidenceItem
	NodesProcessed        int64
	Diagnostics           []DiagnosticItem
}

// parseGoFile parses a single Go source file and extracts AST facts with strict limit enforcement.
func parseGoFile(
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
				Message:    fmt.Sprintf("recovered from parser panic: %v", r),
				Severity:   "warning",
			})
			err = nil // Do not fail analysis; return recovered partial/clean state
		}
	}()

	fset := token.NewFileSet()
	fileNode, parseErr := parser.ParseFile(fset, relPath, content, parser.ParseComments)
	if parseErr != nil {
		res.Diagnostics = append(res.Diagnostics, DiagnosticItem{
			SourceFile: relPath,
			Code:       "PARSE_SYNTAX_ERROR",
			Message:    parseErr.Error(),
			Severity:   "warning",
		})
		return res, nil
	}

	// 1. Process File Imports
	for _, imp := range fileNode.Imports {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if imp.Path != nil {
			importPath, _ := strconv.Unquote(imp.Path.Value)
			alias := ""
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			line := fset.Position(imp.Pos()).Line
			res.Imports = append(res.Imports, ImportItem{
				SourceFile: relPath,
				ImportPath: importPath,
				Alias:      alias,
				LineNumber: line,
			})

			// Technology Evidence from Import
			if techName := mapGoImportToTechnology(importPath); techName != "" {
				res.Evidence = append(res.Evidence, engine.EvidenceItem{
					FilePath: relPath,
					RuleType: "source_import",
					Detail:   fmt.Sprintf("import %s", importPath),
					LineNumber: func() *int {
						l := line
						return &l
					}(),
				})
			}
		}
	}

	// 2. Recursive AST Traversal via Visitor Pattern
	visitor := &goASTVisitor{
		ctx:     ctx,
		fset:    fset,
		relPath: relPath,
		limits:  limits,
		res:     res,
		depth:   1,
	}
	ast.Walk(visitor, fileNode)

	return res, nil
}

type goASTVisitor struct {
	ctx             context.Context
	fset            *token.FileSet
	relPath         string
	limits          *ResourceLimits
	res             *fileASTResult
	nodeCount       int64
	depth           int
	maxDepthReached bool
	maxNodesReached bool
}

func (v *goASTVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}

	select {
	case <-v.ctx.Done():
		return nil
	default:
	}

	v.nodeCount++
	v.res.NodesProcessed++

	if v.depth > v.limits.MaxDepth {
		if !v.maxDepthReached {
			v.maxDepthReached = true
			v.res.Diagnostics = append(v.res.Diagnostics, DiagnosticItem{
				SourceFile: v.relPath,
				Code:       "MAX_DEPTH_EXCEEDED",
				Message:    fmt.Sprintf("AST depth exceeded limit %d", v.limits.MaxDepth),
				Severity:   "info",
			})
		}
		return nil
	}

	if v.nodeCount > v.limits.MaxNodesPerFile {
		if !v.maxNodesReached {
			v.maxNodesReached = true
			v.res.Diagnostics = append(v.res.Diagnostics, DiagnosticItem{
				SourceFile: v.relPath,
				Code:       "MAX_NODES_EXCEEDED",
				Message:    fmt.Sprintf("AST node count exceeded limit %d", v.limits.MaxNodesPerFile),
				Severity:   "info",
			})
		}
		return nil
	}

	switch n := node.(type) {
	case *ast.TypeSpec:
		inspectGoTypeSpec(v.fset, v.relPath, n, v.res)
	case *ast.FuncDecl:
		inspectGoFuncDecl(v.fset, v.relPath, n, v.res)
	case *ast.CallExpr:
		inspectGoCallExpr(v.fset, v.relPath, n, v.res)
	}

	next := *v
	next.depth = v.depth + 1
	return &next
}

func inspectGoTypeSpec(fset *token.FileSet, relPath string, ts *ast.TypeSpec, res *fileASTResult) {
	if ts.Name == nil {
		return
	}
	line := fset.Position(ts.Pos()).Line
	name := ts.Name.Name
	exported := ast.IsExported(name)

	switch t := ts.Type.(type) {
	case *ast.StructType:
		res.Symbols = append(res.Symbols, SymbolItem{
			Name:       name,
			Kind:       SymbolKindStruct,
			SourceFile: relPath,
			LineNumber: line,
			Exported:   exported,
		})

		// Model extraction if struct contains fields or tags
		model := ModelItem{
			Name:       name,
			SourceFile: relPath,
			LineNumber: line,
		}
		if t.Fields != nil {
			for _, field := range t.Fields.List {
				tag := ""
				if field.Tag != nil {
					tag, _ = strconv.Unquote(field.Tag.Value)
					if strings.Contains(tag, "gorm:") {
						model.Framework = "gorm"
					}
				}
				fieldType := exprToString(field.Type)
				if len(field.Names) == 0 {
					// Embedded field
					model.Fields = append(model.Fields, ModelField{
						Name:     fieldType,
						Type:     fieldType,
						Tag:      tag,
						Required: false,
					})
				} else {
					for _, fn := range field.Names {
						model.Fields = append(model.Fields, ModelField{
							Name:     fn.Name,
							Type:     fieldType,
							Tag:      tag,
							Required: !strings.HasPrefix(fieldType, "*"),
						})
					}
				}
			}
		}
		if len(model.Fields) > 0 {
			res.ModelStructures = append(res.ModelStructures, model)
		}

	case *ast.InterfaceType:
		res.Symbols = append(res.Symbols, SymbolItem{
			Name:       name,
			Kind:       SymbolKindInterface,
			SourceFile: relPath,
			LineNumber: line,
			Exported:   exported,
		})
	}
}

func inspectGoFuncDecl(fset *token.FileSet, relPath string, fd *ast.FuncDecl, res *fileASTResult) {
	if fd.Name == nil {
		return
	}
	line := fset.Position(fd.Pos()).Line
	name := fd.Name.Name
	exported := ast.IsExported(name)

	receiver := ""
	kind := SymbolKindFunction
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		kind = SymbolKindMethod
		receiver = exprToString(fd.Recv.List[0].Type)
	}

	sig := buildFuncSignature(fd.Type)

	res.Symbols = append(res.Symbols, SymbolItem{
		Name:       name,
		Kind:       kind,
		SourceFile: relPath,
		LineNumber: line,
		Receiver:   receiver,
		Signature:  sig,
		Exported:   exported,
	})
}

func inspectGoCallExpr(fset *token.FileSet, relPath string, call *ast.CallExpr, res *fileASTResult) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return
	}

	methodName := sel.Sel.Name
	line := fset.Position(call.Pos()).Line

	// 1. Environment lookups: os.Getenv("KEY"), os.LookupEnv("KEY")
	if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "os" {
		if (methodName == "Getenv" || methodName == "LookupEnv") && len(call.Args) >= 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				key, _ := strconv.Unquote(lit.Value)
				if key != "" {
					res.EnvironmentReferences = append(res.EnvironmentReferences, EnvironmentUsage{
						Name:       key,
						SourceFile: relPath,
						LineNumber: line,
						AccessType: "os." + methodName,
					})
				}
			}
		}
	}

	// 2. HTTP Routing Calls: Fiber (Get, Post, Put, Delete), Gin (GET, POST, PUT, DELETE), Chi, net/http
	upperMethod := strings.ToUpper(methodName)
	switch upperMethod {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		if len(call.Args) >= 1 {
			if pathLit, ok := call.Args[0].(*ast.BasicLit); ok && pathLit.Kind == token.STRING {
				pathVal, _ := strconv.Unquote(pathLit.Value)
				if strings.HasPrefix(pathVal, "/") {
					handlerName := ""
					if len(call.Args) >= 2 {
						handlerName = exprToString(call.Args[len(call.Args)-1])
					}
					res.Routes = append(res.Routes, engine.ApiRouteItem{
						Method:     upperMethod,
						Path:       pathVal,
						SourceFile: relPath,
						LineNumber: &line,
						InferredHandler: func() *string {
							if handlerName != "" {
								return &handlerName
							}
							return nil
						}(),
						AuthRequired: false,
					})
				}
			}
		}
	}

	// HandleFunc / Handle
	if methodName == "HandleFunc" || methodName == "Handle" {
		if len(call.Args) >= 1 {
			if pathLit, ok := call.Args[0].(*ast.BasicLit); ok && pathLit.Kind == token.STRING {
				pathVal, _ := strconv.Unquote(pathLit.Value)
				if strings.HasPrefix(pathVal, "/") {
					handlerName := ""
					if len(call.Args) >= 2 {
						handlerName = exprToString(call.Args[1])
					}
					res.Routes = append(res.Routes, engine.ApiRouteItem{
						Method:     "GET",
						Path:       pathVal,
						SourceFile: relPath,
						LineNumber: &line,
						InferredHandler: func() *string {
							if handlerName != "" {
								return &handlerName
							}
							return nil
						}(),
						AuthRequired: false,
					})
				}
			}
		}
	}
}

func mapGoImportToTechnology(imp string) string {
	lower := strings.ToLower(imp)
	switch {
	case strings.Contains(lower, "gofiber/fiber"):
		return "Fiber"
	case strings.Contains(lower, "gin-gonic/gin"):
		return "Gin"
	case strings.Contains(lower, "labstack/echo"):
		return "Echo"
	case strings.Contains(lower, "go-chi/chi"):
		return "Chi"
	case strings.Contains(lower, "gorm.io/gorm"):
		return "GORM"
	case strings.Contains(lower, "redis/go-redis"):
		return "Redis"
	case strings.Contains(lower, "jackc/pgx"):
		return "PostgreSQL"
	case strings.Contains(lower, "lib/pq"):
		return "PostgreSQL"
	case strings.Contains(lower, "mattn/go-sqlite3"):
		return "SQLite"
	default:
		return ""
	}
}

func exprToString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprToString(e.Elt)
	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", exprToString(e.Key), exprToString(e.Value))
	default:
		return "any"
	}
}

func buildFuncSignature(ft *ast.FuncType) string {
	if ft == nil {
		return "()"
	}
	params := make([]string, 0)
	if ft.Params != nil {
		for _, p := range ft.Params.List {
			pt := exprToString(p.Type)
			if len(p.Names) == 0 {
				params = append(params, pt)
			} else {
				for _, n := range p.Names {
					params = append(params, n.Name+" "+pt)
				}
			}
		}
	}

	results := make([]string, 0)
	if ft.Results != nil {
		for _, r := range ft.Results.List {
			rt := exprToString(r.Type)
			results = append(results, rt)
		}
	}

	resStr := ""
	if len(results) == 1 {
		resStr = " " + results[0]
	} else if len(results) > 1 {
		resStr = " (" + strings.Join(results, ", ") + ")"
	}

	return fmt.Sprintf("(%s)%s", strings.Join(params, ", "), resStr)
}

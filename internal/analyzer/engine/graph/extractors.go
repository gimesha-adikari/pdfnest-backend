package graph

import (
	"fmt"
	"path/filepath"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/ast"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

// ExtractionResult holds the extracted entities and edges.
type ExtractionResult struct {
	Entities []*GraphEntity
	Edges    []*GraphEdge
}

func ptrInt(i int) *int { return &i }

func ExtractInventoryEntitiesAndEdges(inv *inventory.ScopeInventory, facts *parsers.AnalysisFacts) *ExtractionResult {
	res := &ExtractionResult{}
	if inv == nil {
		return res
	}

	dirEntities := make(map[string]*GraphEntity)

	// Create entities for files and directories
	for _, f := range inv.Files {
		if f.IsExcluded {
			continue
		}

		if f.IsDirectory {
			id := fmt.Sprintf("dir:%s", f.RelPath)
			ent := &GraphEntity{
				ID:   id,
				Kind: EntityDirectory,
				Name: filepath.Base(f.RelPath),
				Path: f.RelPath,
			}
			res.Entities = append(res.Entities, ent)
			dirEntities[f.RelPath] = ent
			continue
		}

		id := fmt.Sprintf("file:%s", f.RelPath)
		ent := &GraphEntity{
			ID:   id,
			Kind: EntityFile,
			Name: filepath.Base(f.RelPath),
			Path: f.RelPath,
			Properties: map[string]any{
				"category": string(f.Category),
				"size":     f.Size,
			},
		}
		res.Entities = append(res.Entities, ent)

		// CONTAINS edge from parent dir to file
		dirPath := filepath.Dir(f.RelPath)
		if dirPath != "." && dirPath != "" {
			parentID := fmt.Sprintf("dir:%s", dirPath)
			edgeID := fmt.Sprintf("edge:contains:%s:%s", parentID, id)
			res.Edges = append(res.Edges, &GraphEdge{
				ID:       edgeID,
				SourceID: parentID,
				TargetID: id,
				Type:     "contains",
				Confidence: engine.EpistemicConfidenceConfirmed,
				Provenance: RelationshipProvenance{
					Kind:     RelationshipKindDirect,
					Detector: "inventory_extractor",
				},
			})
		}
	}

	// For packages (dependencies)
	if facts != nil {
		for _, dep := range facts.RuntimeDeps {
			id := fmt.Sprintf("pkg:%s:%s", dep.Manager, dep.Name)
			ent := &GraphEntity{
				ID:   id,
				Kind: EntityPackage,
				Name: dep.Name,
				Properties: map[string]any{
					"manager": dep.Manager,
					"version": dep.Version,
				},
			}
			res.Entities = append(res.Entities, ent)
		}
		for _, dep := range facts.DevDeps {
			id := fmt.Sprintf("pkg:%s:%s", dep.Manager, dep.Name)
			ent := &GraphEntity{
				ID:   id,
				Kind: EntityPackage,
				Name: dep.Name,
				Properties: map[string]any{
					"manager": dep.Manager,
					"version": dep.Version,
					"isDev":   true,
				},
			}
			res.Entities = append(res.Entities, ent)
		}
	}

	return res
}

func ExtractASTEntitiesAndEdges(astFacts *ast.ASTAnalysisResult) *ExtractionResult {
	res := &ExtractionResult{}
	if astFacts == nil {
		return res
	}

	// Symbols
	for _, sym := range astFacts.Symbols {
		fileID := fmt.Sprintf("file:%s", sym.SourceFile)
		symID := fmt.Sprintf("sym:%s:%s", sym.SourceFile, sym.Name)

		ent := &GraphEntity{
			ID:   symID,
			Kind: EntitySymbol,
			Name: sym.Name,
			Path: sym.SourceFile,
			Properties: map[string]any{
				"kind":     string(sym.Kind),
				"line":     sym.LineNumber,
				"exported": sym.Exported,
			},
		}
		res.Entities = append(res.Entities, ent)

		// DEFINES edge File -> Symbol
		edgeID := fmt.Sprintf("edge:defines:%s:%s", fileID, symID)
		res.Edges = append(res.Edges, &GraphEdge{
			ID:       edgeID,
			SourceID: fileID,
			TargetID: symID,
			Type:     RelDefines,
			Confidence: engine.EpistemicConfidenceConfirmed,
			Provenance: RelationshipProvenance{
				Kind:     RelationshipKindDirect,
				Detector: "ast_extractor",
			},
		})
	}

	// Imports
	for _, imp := range astFacts.Imports {
		fileID := fmt.Sprintf("file:%s", imp.SourceFile)
		// Try to resolve if it's a file or package, we'll just treat it as abstract target for now
		targetID := fmt.Sprintf("pkg:%s", imp.ImportPath) // Placeholder for actual ID resolution later if needed
		
		edgeID := fmt.Sprintf("edge:imports:%s:%s", fileID, targetID)
		res.Edges = append(res.Edges, &GraphEdge{
			ID:       edgeID,
			SourceID: fileID,
			TargetID: targetID,
			Type:     RelImports,
			Confidence: engine.EpistemicConfidenceConfirmed,
			Provenance: RelationshipProvenance{
				Kind:     RelationshipKindDirect,
				Detector: "ast_extractor",
			},
		})
	}

	return res
}

func ExtractRouteEntitiesAndEdges(facts *parsers.AnalysisFacts) *ExtractionResult {
	res := &ExtractionResult{}
	if facts == nil {
		return res
	}

	for _, route := range facts.Routes {
		routeID := fmt.Sprintf("route:%s:%s", route.Method, route.Path)
		ent := &GraphEntity{
			ID:   routeID,
			Kind: EntityRoute,
			Name: route.Path,
			Path: route.SourceFile,
			Properties: map[string]any{
				"method": route.Method,
			},
		}
		res.Entities = append(res.Entities, ent)

		fileID := fmt.Sprintf("file:%s", route.SourceFile)
		edgeID := fmt.Sprintf("edge:exposes:%s:%s", fileID, routeID)
		
		evidence := []engine.Evidence{}
		if route.LineNumber != nil {
			evidence = append(evidence, engine.Evidence{
				FilePath: route.SourceFile,
				LineStart: route.LineNumber,
				Detector: "route_extractor",
				Confidence: engine.EpistemicConfidenceConfirmed,
				Description: fmt.Sprintf("Route %s %s exposed in %s", route.Method, route.Path, route.SourceFile),
			})
		}
		
		res.Edges = append(res.Edges, &GraphEdge{
			ID:       edgeID,
			SourceID: fileID,
			TargetID: routeID,
			Type:     RelExposes,
			Confidence: engine.EpistemicConfidenceConfirmed,
			Provenance: RelationshipProvenance{
				Kind:     RelationshipKindDirect,
				Detector: "route_extractor",
			},
			Evidence: evidence,
		})
	}
	return res
}

func ExtractModelEntitiesAndEdges(astFacts *ast.ASTAnalysisResult) *ExtractionResult {
	res := &ExtractionResult{}
	if astFacts == nil {
		return res
	}
	
	for _, model := range astFacts.ModelStructures {
		modelID := fmt.Sprintf("model:%s:%s", model.SourceFile, model.Name)
		ent := &GraphEntity{
			ID:   modelID,
			Kind: EntityModel,
			Name: model.Name,
			Path: model.SourceFile,
			Properties: map[string]any{
				"framework": model.Framework,
			},
		}
		res.Entities = append(res.Entities, ent)

		fileID := fmt.Sprintf("file:%s", model.SourceFile)
		edgeID := fmt.Sprintf("edge:defines_model:%s:%s", fileID, modelID)
		res.Edges = append(res.Edges, &GraphEdge{
			ID:       edgeID,
			SourceID: fileID,
			TargetID: modelID,
			Type:     RelDefines,
			Confidence: engine.EpistemicConfidenceConfirmed,
			Provenance: RelationshipProvenance{
				Kind:     RelationshipKindDirect,
				Detector: "model_extractor",
			},
		})
	}
	
	return res
}

func ExtractConfigEntitiesAndEdges(facts *parsers.AnalysisFacts) *ExtractionResult {
	res := &ExtractionResult{}
	if facts == nil {
		return res
	}

	for _, env := range facts.Environment {
		envID := fmt.Sprintf("config:env:%s", env.Name)
		ent := &GraphEntity{
			ID:   envID,
			Kind: EntityConfig,
			Name: env.Name,
		}
		res.Entities = append(res.Entities, ent)

		for _, ref := range env.References {
			fileID := fmt.Sprintf("file:%s", ref)
			edgeID := fmt.Sprintf("edge:configures:%s:%s", envID, fileID)
			res.Edges = append(res.Edges, &GraphEdge{
				ID:       edgeID,
				SourceID: envID,
				TargetID: fileID,
				Type:     RelConfigures,
				Confidence: engine.EpistemicConfidenceStronglyInferred,
				Provenance: RelationshipProvenance{
					Kind:     RelationshipKindInferred,
					Detector: "config_extractor",
				},
			})
		}
	}
	return res
}

func ExtractTestEntitiesAndEdges(inv *inventory.ScopeInventory, facts *parsers.AnalysisFacts) *ExtractionResult {
	res := &ExtractionResult{}
	if inv == nil {
		return res
	}

	for _, f := range inv.Files {
		if f.Category == inventory.CategoryTest {
			testID := fmt.Sprintf("file:%s", f.RelPath)
			
			// Try to infer tested file based on naming convention
			baseName := strings.TrimSuffix(filepath.Base(f.RelPath), filepath.Ext(f.RelPath))
			testedBase := strings.TrimSuffix(baseName, "_test")
			testedBase = strings.TrimSuffix(testedBase, ".test")
			testedBase = strings.TrimSuffix(testedBase, ".spec")
			
			if testedBase != baseName {
				targetFile := filepath.Join(filepath.Dir(f.RelPath), testedBase+filepath.Ext(f.RelPath))
				targetID := fmt.Sprintf("file:%s", targetFile)
				
				edgeID := fmt.Sprintf("edge:tests:%s:%s", testID, targetID)
				res.Edges = append(res.Edges, &GraphEdge{
					ID:       edgeID,
					SourceID: testID,
					TargetID: targetID,
					Type:     RelTests,
					Confidence: engine.EpistemicConfidenceStronglyInferred,
					Provenance: RelationshipProvenance{
						Kind:     RelationshipKindInferred,
						Detector: "test_extractor",
					},
				})
			}
		}
	}

	return res
}

func ExtractDeploymentEntitiesAndEdges(facts *parsers.AnalysisFacts) *ExtractionResult {
	res := &ExtractionResult{}
	if facts == nil {
		return res
	}

	for _, path := range facts.Deployment.DockerfilePaths {
		depID := fmt.Sprintf("deploy:docker:%s", path)
		ent := &GraphEntity{
			ID:   depID,
			Kind: EntityDeployment,
			Name: "Dockerfile",
			Path: path,
		}
		res.Entities = append(res.Entities, ent)

		// Just a generic DEPLOYS edge to the repo root / main service
		// Usually we'd map to a specific service
	}

	for _, wf := range facts.Deployment.CIWorkflows {
		depID := fmt.Sprintf("deploy:ci:%s", wf.Path)
		ent := &GraphEntity{
			ID:   depID,
			Kind: EntityDeployment,
			Name: wf.Name,
			Path: wf.Path,
		}
		res.Entities = append(res.Entities, ent)
	}

	return res
}
